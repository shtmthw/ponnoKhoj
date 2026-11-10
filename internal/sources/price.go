// Package sources integrates online shops: a compliance-first fetch client
// (robots.txt, rate limits, retries, circuit breaker), a schema.org JSON-LD
// parser, a price parser, and the Source implementations built on them.
package sources

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/unicode/norm"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
)

// Price parsing errors. The parser returns poisha or one of these typed
// errors; it never guesses.
var (
	// ErrPriceMissing means the text contains no number at all.
	ErrPriceMissing = errors.New("sources: no price in text")
	// ErrPriceMalformed means a number is present but is not a valid amount.
	ErrPriceMalformed = errors.New("sources: malformed price")
	// ErrPriceAmbiguous means several unrelated numbers are present and the
	// current price cannot be told apart from the others.
	ErrPriceAmbiguous = errors.New("sources: ambiguous price")
	// ErrPriceOutOfRange means the amount is zero or implausibly large.
	ErrPriceOutOfRange = errors.New("sources: price out of range")
	// ErrPriceForeignCurrency means the text names a currency other than BDT.
	ErrPriceForeignCurrency = errors.New("sources: price is not in BDT")
)

// PriceRangeError is returned for a "from–to" price such as "৳ 1,200 – ৳ 1,500".
// A range is not a price; callers decide whether the bounds are useful.
type PriceRangeError struct {
	Low, High domain.Money
}

func (e *PriceRangeError) Error() string {
	return fmt.Sprintf("sources: price is a range (%s to %s)", e.Low.Decimal(), e.High.Decimal())
}

var (
	numberRe = regexp.MustCompile(`[0-9][0-9,]*(?:\.[0-9]+)?`)
	// rangeSepRe matches what may sit between the two numbers of a range,
	// optionally with currency markers around the separator.
	rangeSepRe      = regexp.MustCompile(`^\s*(?:/-|/=)?\s*(?:tk\.?|bdt|taka|টাকা|৳)?\s*(?:-|–|—|~|to|থেকে)\s*(?:tk\.?|bdt|taka|টাকা|৳)?\s*$`)
	foreignMarkerRe = regexp.MustCompile(`[$€£₹¥]|\b(?:usd|eur|gbp|inr|rs|rupees?|dollars?)\b`)
	bdtMarkerRe     = regexp.MustCompile(`৳|\b(?:tk|bdt|taka)\b|টাকা`)
)

// HasBDTMarker reports whether text explicitly names taka (৳, Tk, BDT, টাকা).
func HasBDTMarker(text string) bool {
	return bdtMarkerRe.MatchString(strings.ToLower(norm.NFKC.String(text)))
}

// ParsePrice parses a human-written BDT price into poisha. It understands
// "৳ ১,২৫০", "Tk 1,250.00", "BDT 1250", "1,250/-" and plain numbers, with
// Bangla or ASCII digits and Western (1,250,000) or South Asian (12,50,000)
// digit grouping.
func ParsePrice(text string) (domain.Money, error) {
	if len(text) > 512 {
		return 0, ErrPriceMalformed
	}
	s := strings.ToLower(domain.ToASCIIDigits(norm.NFKC.String(text)))
	if foreignMarkerRe.MatchString(s) {
		return 0, ErrPriceForeignCurrency
	}
	locs := numberRe.FindAllStringIndex(s, -1)
	switch len(locs) {
	case 0:
		return 0, ErrPriceMissing
	case 1:
		return parseAmount(s, locs[0], false)
	case 2:
		if !rangeSepRe.MatchString(s[locs[0][1]:locs[1][0]]) {
			return 0, ErrPriceAmbiguous
		}
		low, err := parseAmount(s, locs[0], false)
		if err != nil {
			return 0, err
		}
		high, err := parseAmount(s, locs[1], true)
		if err != nil {
			return 0, err
		}
		if low > high {
			return 0, ErrPriceMalformed
		}
		if low == high {
			return low, nil
		}
		return 0, &PriceRangeError{Low: low, High: high}
	default:
		return 0, ErrPriceAmbiguous
	}
}

// parseAmount converts the number at loc into poisha.
func parseAmount(s string, loc []int, afterSeparator bool) (domain.Money, error) {
	// A decimal point glued to the front means this is not a clean amount
	// (".50"); so does a minus sign, unless it is a range separator.
	if loc[0] > 0 && (s[loc[0]-1] == '.' || (s[loc[0]-1] == '-' && !afterSeparator)) {
		return 0, ErrPriceMalformed
	}
	num := s[loc[0]:loc[1]]
	whole, frac, _ := strings.Cut(num, ".")
	if strings.Contains(whole, ",") {
		groups := strings.Split(whole, ",")
		// Western: 1,250,000. South Asian: 12,50,000. Either way the last
		// group has three digits and inner groups have two or three.
		if n := len(groups[0]); n < 1 || n > 3 {
			return 0, ErrPriceMalformed
		}
		for i, g := range groups[1:] {
			last := i == len(groups)-2
			if (last && len(g) != 3) || (!last && len(g) != 2 && len(g) != 3) {
				return 0, ErrPriceMalformed
			}
		}
		whole = strings.Join(groups, "")
	}
	if len(whole) > 12 {
		return 0, ErrPriceOutOfRange
	}
	dec := whole
	if frac != "" {
		dec += "." + frac
	}
	m, err := domain.ParseDecimalTaka(dec)
	switch {
	case errors.Is(err, domain.ErrMoneyRange):
		return 0, ErrPriceOutOfRange
	case err != nil:
		return 0, ErrPriceMalformed
	case !m.Valid():
		return 0, ErrPriceOutOfRange
	}
	return m, nil
}

// ParsePriceHTML parses the current price out of an HTML fragment such as
// "<del>৳1,500</del> <ins>৳1,250</ins>". Text inside <del>, <s> and <strike>
// (the semantic "was" price elements) is ignored; what remains must be a
// single unambiguous price.
func ParsePriceHTML(fragment string) (domain.Money, error) {
	if len(fragment) > 8192 {
		return 0, ErrPriceMalformed
	}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return 0, ErrPriceMalformed
	}
	var b strings.Builder
	var walk func(n *html.Node, depth int)
	walk = func(n *html.Node, depth int) {
		if depth > 64 {
			return
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "del", "s", "strike", "script", "style":
				return
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, depth+1)
		}
	}
	for _, n := range nodes {
		walk(n, 0)
	}
	return ParsePrice(strings.TrimSpace(b.String()))
}
