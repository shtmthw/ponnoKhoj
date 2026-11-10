// Package matching decides how confidently an online offer's title refers to
// a canonical product. It is deterministic and performs no I/O.
package matching

import (
	"strings"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/search"
)

// Product is the part of a canonical product that matching looks at.
type Product struct {
	NameEn   string
	NameBn   string
	Brand    string
	Model    string
	Quantity domain.Quantity
}

// MinConfidence is the lowest match confidence at which an offer is stored.
const MinConfidence = 0.6

// Confidence scores, in [0, 1], how likely an offer titled title is the
// given product.
//
// The rules, in order of authority:
//   - A different stated size is a different product: capped at 0.2.
//   - A product with a model number needs every model token in the title,
//     otherwise capped at 0.3 (a fridge of another model is another fridge).
//   - Otherwise the score is the share of the product's name words found in
//     the title (best of the English and Bangla names), adjusted up when the
//     brand and size agree and down when the brand is missing.
func Confidence(p Product, title string) float64 {
	tq := search.ParseQuery(title)
	titleWords := wordSet(tq.Key)
	if len(titleWords) == 0 {
		return 0
	}

	nameScore := max(coverage(nameWords(p.NameEn), titleWords), coverage(nameWords(p.NameBn), titleWords))
	if nameScore == 0 {
		return 0
	}
	score := 0.75 * nameScore

	if brand := wordSet(search.Key(search.Normalize(p.Brand))); len(brand) > 0 {
		if coverage(keys(brand), titleWords) == 1 {
			score += 0.15
		} else {
			score -= 0.15
		}
	} else {
		score += 0.05
	}

	sizeMismatch := false
	switch {
	case tq.Quantity == nil || tq.Quantity.Unit != p.Quantity.Unit:
		// The title states no size, or one in another dimension (a model
		// code such as "SDCZ600-128G" reads as 128 g next to a 1-piece
		// product): neither confirms nor contradicts.
	case *tq.Quantity == p.Quantity:
		score += 0.15
	default:
		sizeMismatch = true
	}

	score = min(max(score, 0), 1)
	if model := wordSet(search.Key(search.Normalize(p.Model))); len(model) > 0 && coverage(keys(model), titleWords) < 1 {
		score = min(score, 0.3)
	}
	if variantConflict(p, titleWords) {
		score = min(score, 0.2)
	}
	if sizeMismatch {
		score = min(score, 0.2)
	}
	return score
}

// nameWords returns the search-key words of a product name with any quantity
// token removed (size is compared separately).
func nameWords(name string) []string {
	q := search.ParseQuery(name)
	var out []string
	for w := range strings.FieldsSeq(q.Key) {
		if q.Quantity != nil && w == q.Quantity.Token() {
			continue
		}
		out = append(out, w)
	}
	return out
}

func wordSet(key string) map[string]bool {
	set := map[string]bool{}
	for w := range strings.FieldsSeq(key) {
		set[w] = true
	}
	return set
}

func keys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// coverage is the share of words present in the title.
func coverage(words []string, title map[string]bool) float64 {
	if len(words) == 0 {
		return 0
	}
	hit := 0
	for _, w := range words {
		if title[w] {
			hit++
		}
	}
	return float64(hit) / float64(len(words))
}

// variantConflict reports whether the title names a different variant of
// the product: one of the product's spec tokens (a token with digits, such
// as "10000mah", "32gb", "c6", "x20") is missing from the title while the
// title has a token of the same shape with another number ("20000mah",
// "128gb", "c64", "x50"). Variants share almost every word, so word
// coverage alone cannot tell them apart.
func variantConflict(p Product, title map[string]bool) bool {
	shapes := map[string]bool{}
	for w := range title {
		if hasDigit(w) {
			shapes[shape(w)] = true
		}
	}
	names := []string{p.NameEn, p.Model}
	for _, n := range names {
		for _, w := range nameWords(n) {
			if hasDigit(w) && !title[w] && shapes[shape(w)] {
				return true
			}
		}
	}
	return false
}

func hasDigit(w string) bool { return strings.ContainsAny(w, "0123456789") }

// shape replaces each run of digits with "#": "10000mah" -> "#mah",
// "ac1200" -> "ac#", "3.0" -> "#.#".
func shape(w string) string {
	var b strings.Builder
	inDigits := false
	for _, r := range w {
		if r >= '0' && r <= '9' {
			if !inDigits {
				b.WriteByte('#')
			}
			inDigits = true
			continue
		}
		inDigits = false
		b.WriteRune(r)
	}
	return b.String()
}
