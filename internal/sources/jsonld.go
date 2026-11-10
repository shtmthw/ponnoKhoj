package sources

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
)

// RawOffer is one price observed on an online shop, before it is matched to
// a canonical product.
type RawOffer struct {
	Title   string
	URL     string
	Price   domain.Money
	InStock bool
}

// Limits that keep a hostile or broken page from costing unbounded work.
const (
	maxJSONLDBlocks = 64
	maxJSONLDDepth  = 32
	maxOffersPerDoc = 500
	maxTitleRunes   = 300
)

// ParseResult is what a structured-data page yielded.
type ParseResult struct {
	Offers []RawOffer
	// Skipped counts products that were present but unusable: no price, a
	// price range, a foreign currency, an unparsable price, or a bad URL.
	Skipped int
}

// ParseStructuredData extracts schema.org Product/Offer data from the
// JSON-LD blocks of an HTML page. Relative URLs are resolved against base.
// It never returns a guessed price: products without one clear BDT price are
// counted in Skipped.
func ParseStructuredData(page []byte, base *url.URL) (ParseResult, error) {
	blocks, err := extractJSONLD(bytes.NewReader(page))
	if err != nil {
		return ParseResult{}, err
	}
	var res ParseResult
	seen := map[string]bool{}
	for _, block := range blocks {
		dec := json.NewDecoder(strings.NewReader(block))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			res.Skipped++ // a broken block does not spoil the other blocks
			continue
		}
		walkJSONLD(v, 0, base, &res, seen)
	}
	return res, nil
}

// extractJSONLD returns the text of every <script type="application/ld+json">.
func extractJSONLD(r io.Reader) ([]string, error) {
	z := html.NewTokenizer(r)
	var blocks []string
	for {
		switch z.Next() {
		case html.ErrorToken:
			if errors.Is(z.Err(), io.EOF) {
				return blocks, nil
			}
			return blocks, z.Err()
		case html.StartTagToken:
			name, hasAttr := z.TagName()
			if string(name) != "script" || !hasAttr {
				continue
			}
			isLD := false
			for {
				key, val, more := z.TagAttr()
				if string(key) == "type" && strings.EqualFold(strings.TrimSpace(string(val)), "application/ld+json") {
					isLD = true
				}
				if !more {
					break
				}
			}
			if !isLD {
				continue
			}
			if z.Next() == html.TextToken && len(blocks) < maxJSONLDBlocks {
				blocks = append(blocks, string(z.Text()))
			}
		}
	}
}

func walkJSONLD(v any, depth int, base *url.URL, res *ParseResult, seen map[string]bool) {
	if depth > maxJSONLDDepth || len(res.Offers) >= maxOffersPerDoc {
		return
	}
	switch node := v.(type) {
	case []any:
		for _, item := range node {
			walkJSONLD(item, depth+1, base, res, seen)
		}
	case map[string]any:
		if hasType(node, "Product") {
			addProduct(node, base, res, seen)
			return
		}
		// Containers: @graph, ItemList.itemListElement[].item, mainEntity...
		for _, child := range node {
			switch child.(type) {
			case []any, map[string]any:
				walkJSONLD(child, depth+1, base, res, seen)
			}
		}
	}
}

func hasType(node map[string]any, want string) bool {
	switch t := node["@type"].(type) {
	case string:
		return t == want || strings.HasSuffix(t, "/"+want)
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok && (s == want || strings.HasSuffix(s, "/"+want)) {
				return true
			}
		}
	}
	return false
}

func addProduct(node map[string]any, base *url.URL, res *ParseResult, seen map[string]bool) {
	title := cleanTitle(stringField(node, "name"))
	offers := offerNodes(node["offers"])
	if title == "" || len(offers) == 0 {
		res.Skipped++
		return
	}
	productURL := stringField(node, "url")
	for _, offer := range offers {
		price, ok := offerPrice(offer)
		if !ok {
			res.Skipped++
			continue
		}
		raw := stringField(offer, "url")
		if raw == "" {
			raw = productURL
		}
		abs, ok := resolveURL(base, raw)
		if !ok {
			res.Skipped++
			continue
		}
		key := abs + "\x00" + title
		if seen[key] {
			continue
		}
		seen[key] = true
		res.Offers = append(res.Offers, RawOffer{Title: title, URL: abs, Price: price, InStock: inStock(offer)})
		if len(res.Offers) >= maxOffersPerDoc {
			return
		}
	}
}

// offerNodes normalizes the "offers" property (object or array) to a list.
func offerNodes(v any) []map[string]any {
	switch o := v.(type) {
	case map[string]any:
		return []map[string]any{o}
	case []any:
		var out []map[string]any
		for _, item := range o {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// offerPrice returns the offer's single BDT price, if it has exactly one.
func offerPrice(offer map[string]any) (domain.Money, bool) {
	text := scalarField(offer, "price")
	currency := strings.ToUpper(strings.TrimSpace(stringField(offer, "priceCurrency")))
	if spec, ok := offer["priceSpecification"].(map[string]any); ok {
		if text == "" {
			text = scalarField(spec, "price")
		}
		if currency == "" {
			currency = strings.ToUpper(strings.TrimSpace(stringField(spec, "priceCurrency")))
		}
	}
	if text == "" && hasType(offer, "AggregateOffer") {
		// An aggregate is usable only when it collapses to one price.
		low, high := scalarField(offer, "lowPrice"), scalarField(offer, "highPrice")
		if low != "" && low == high {
			text = low
		}
	}
	if text == "" {
		return 0, false
	}
	// The currency must be known to be BDT: declared, or marked in the text.
	if currency != "BDT" && (currency != "" || !HasBDTMarker(text)) {
		return 0, false
	}
	price, err := ParsePrice(text)
	if err != nil {
		return 0, false
	}
	return price, true
}

// inStock reports availability. Only an explicit out-of-stock style value
// marks an offer unavailable; schema.org availability is optional and most
// shops omit it for items they are selling.
func inStock(offer map[string]any) bool {
	a := strings.ToLower(stringField(offer, "availability"))
	for _, out := range []string{"outofstock", "soldout", "discontinued"} {
		if strings.Contains(a, out) {
			return false
		}
	}
	return true
}

func stringField(node map[string]any, key string) string {
	if s, ok := node[key].(string); ok {
		return s
	}
	return ""
}

// scalarField reads a string or JSON number as its literal text.
func scalarField(node map[string]any, key string) string {
	switch v := node[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	}
	return ""
}

func cleanTitle(s string) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(s, " ")), " ")
	if rs := []rune(s); len(rs) > maxTitleRunes {
		s = string(rs[:maxTitleRunes])
	}
	return s
}

func resolveURL(base *url.URL, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	u.Fragment = ""
	return u.String(), true
}
