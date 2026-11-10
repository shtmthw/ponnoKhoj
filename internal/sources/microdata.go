package sources

import (
	"bytes"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// Limits for the microdata walk, matching the JSON-LD ones in spirit.
const (
	maxMicrodataDepth = 256
	maxMicrodataItems = 2000
)

// microItem is one schema.org microdata item: an element with itemscope.
type microItem struct {
	types []string
	props map[string][]any // values are string or *microItem
}

// parseMicrodata extracts the top-level schema.org microdata items of a
// page (https://html.spec.whatwg.org/multipage/microdata.html), converted to
// the same generic map form as JSON-LD so both feed one Product/Offer
// reader. Only the parts of the algorithm schema.org product pages use are
// implemented: itemscope, itemtype, itemprop, and the meta/link/a/img/time
// value rules. itemref is not supported.
func parseMicrodata(page []byte) []map[string]any {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil
	}
	var top []*microItem
	count := 0
	var walk func(n *html.Node, scope *microItem, depth int)
	walk = func(n *html.Node, scope *microItem, depth int) {
		if depth > maxMicrodataDepth || count > maxMicrodataItems {
			return
		}
		next := scope
		if n.Type == html.ElementNode {
			props := strings.Fields(attr(n, "itemprop"))
			if hasAttr(n, "itemscope") {
				count++
				item := &microItem{types: strings.Fields(attr(n, "itemtype")), props: map[string][]any{}}
				if len(props) > 0 && scope != nil {
					for _, p := range props {
						scope.props[p] = append(scope.props[p], item)
					}
				} else {
					top = append(top, item)
				}
				next = item
			} else if len(props) > 0 && scope != nil {
				v := microValue(n)
				for _, p := range props {
					scope.props[p] = append(scope.props[p], v)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, next, depth+1)
		}
	}
	walk(doc, nil, 0)
	out := make([]map[string]any, 0, len(top))
	for _, it := range top {
		out = append(out, it.toMap(0))
	}
	return out
}

// toMap renders an item like a JSON-LD node: "@type" from the itemtype URL's
// last path segment, single-valued properties as scalars.
func (it *microItem) toMap(depth int) map[string]any {
	m := map[string]any{}
	if len(it.types) > 0 {
		types := make([]any, 0, len(it.types))
		for _, t := range it.types {
			types = append(types, t[strings.LastIndex(t, "/")+1:])
		}
		if len(types) == 1 {
			m["@type"] = types[0]
		} else {
			m["@type"] = types
		}
	}
	for k, vs := range it.props {
		conv := make([]any, 0, len(vs))
		for _, v := range vs {
			if sub, ok := v.(*microItem); ok {
				if depth < maxJSONLDDepth {
					conv = append(conv, sub.toMap(depth+1))
				}
				continue
			}
			conv = append(conv, v)
		}
		if len(conv) == 1 {
			m[k] = conv[0]
		} else if len(conv) > 1 {
			m[k] = conv
		}
	}
	return m
}

// microValue is the property value of a non-item element.
func microValue(n *html.Node) string {
	switch n.Data {
	case "meta":
		return attr(n, "content")
	case "link", "a", "area":
		return attr(n, "href")
	case "img", "audio", "video", "source", "iframe", "embed", "track":
		return attr(n, "src")
	case "time":
		if v := attr(n, "datetime"); v != "" {
			return v
		}
	case "data", "meter":
		return attr(n, "value")
	}
	var b strings.Builder
	var text func(*html.Node, int)
	text = func(c *html.Node, depth int) {
		if depth > 64 || b.Len() > 4096 {
			return
		}
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
		for k := c.FirstChild; k != nil; k = k.NextSibling {
			text(k, depth+1)
		}
	}
	text(n, 0)
	return strings.Join(strings.Fields(b.String()), " ")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

// ParseProductPage reads one product page: schema.org JSON-LD when present,
// otherwise schema.org microdata. Offers without their own URL get the page
// URL, which is what a product page's offer refers to.
func ParseProductPage(page []byte, pageURL *url.URL) (ParseResult, error) {
	res, err := ParseStructuredData(page, pageURL)
	if err != nil {
		return ParseResult{}, err
	}
	if len(res.Offers) > 0 {
		return res, nil
	}
	seen := map[string]bool{}
	micro := ParseResult{}
	for _, item := range parseMicrodata(page) {
		if hasType(item, "Product") {
			if _, ok := item["url"]; !ok && pageURL != nil {
				item["url"] = pageURL.String()
			}
		}
		walkJSONLD(item, 0, pageURL, &micro, seen)
	}
	// Report what the JSON-LD pass skipped too, so parse failures are visible.
	micro.Skipped += res.Skipped
	return micro, nil
}
