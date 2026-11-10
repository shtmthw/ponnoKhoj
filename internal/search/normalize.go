// Package search turns free-text shopper input (Bangla, English or mixed) into
// a normalized query, and ranks product candidates. It performs no I/O.
package search

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
)

// MaxQueryRunes bounds how much of a query is considered. Longer input is
// truncated, not rejected: a pasted product title should still search.
const MaxQueryRunes = 200

// Query is a parsed, normalized search query.
type Query struct {
	// Raw is the input as typed, trimmed and length-capped.
	Raw string
	// Text is the normalized text with the original scripts preserved and any
	// quantity phrase rewritten to its canonical token ("5 kg" -> "5000g").
	Text string
	// Key is the ASCII search key: Text with Bangla transliterated. Product
	// keys are built the same way, so trigram similarity is script-agnostic.
	Key string
	// Quantity is the first quantity phrase found in the query, if any.
	Quantity *domain.Quantity
}

// Empty reports whether the query has nothing to search for.
func (q Query) Empty() bool { return q.Key == "" }

// Normalize applies the script-preserving normalization pipeline:
// Unicode NFKC, case folding, Bangla digits to ASCII digits, and
// punctuation/whitespace cleanup. A '.' survives only between two ASCII digits
// (a decimal point). The result is idempotent: Normalize(Normalize(s)) ==
// Normalize(s).
func Normalize(s string) string {
	s = truncateRunes(strings.ToValidUTF8(s, " "), MaxQueryRunes*4)
	// Case folding and NFKC can each undo the other's work for a handful of
	// exotic code points, so iterate to a fixed point (bounded).
	for range 4 {
		next := cleanup(norm.NFKC.String(strings.ToLower(norm.NFKC.String(s))))
		if next == s {
			break
		}
		s = next
	}
	return truncateRunes(s, MaxQueryRunes)
}

// cleanup maps digits, drops punctuation and collapses whitespace.
func cleanup(s string) string {
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	space := true // suppress leading spaces
	for i, r := range rs {
		switch {
		case r >= '০' && r <= '৯':
			b.WriteByte(byte('0' + (r - '০')))
			space = false
		case r == '.' && i > 0 && i < len(rs)-1 && isDigit(rs[i-1]) && isDigit(rs[i+1]):
			b.WriteByte('.')
			space = false
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
			b.WriteRune(r)
			space = false
		default:
			// Punctuation, symbols, joiners, whitespace: one separating space.
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// isDigit reports ASCII or Bangla digits (both become ASCII digits).
func isDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= '০' && r <= '৯')
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for range n {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return strings.TrimRight(s[:i], " ")
}

// ParseQuery normalizes raw and extracts the first quantity phrase.
func ParseQuery(raw string) Query {
	raw = truncateRunes(strings.TrimSpace(strings.ToValidUTF8(raw, " ")), MaxQueryRunes)
	text, qty := canonicalizeQuantities(Normalize(raw))
	return Query{Raw: raw, Text: text, Key: Key(text), Quantity: qty}
}

// canonicalizeQuantities rewrites quantity phrases in normalized text to their
// canonical tokens and returns the first quantity found. It recognizes
// "5 kg", "5kg", "1.5 ltr" and "৫০০গ্রাম" (digits are already ASCII here).
func canonicalizeQuantities(text string) (string, *domain.Quantity) {
	tokens := strings.Fields(text)
	out := make([]string, 0, len(tokens))
	var first *domain.Quantity
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		num, rest := splitLeadingNumber(tok)
		var (
			q  domain.Quantity
			ok bool
		)
		switch {
		case num == "":
			// not a number
		case rest != "":
			q, ok = domain.ParseQuantity(num, rest) // "5kg"
		case i+1 < len(tokens):
			if q, ok = domain.ParseQuantity(num, tokens[i+1]); ok { // "5 kg"
				i++
			}
		}
		if !ok {
			out = append(out, tok)
			continue
		}
		out = append(out, q.Token())
		if first == nil {
			first = &q
		}
	}
	return strings.Join(out, " "), first
}

// splitLeadingNumber splits "1.5kg" into ("1.5", "kg"). num is empty when tok
// does not start with a digit.
func splitLeadingNumber(tok string) (num, rest string) {
	i := 0
	seenDot := false
	for i < len(tok) {
		c := tok[i]
		if c >= '0' && c <= '9' {
			i++
			continue
		}
		if c == '.' && !seenDot && i > 0 && i+1 < len(tok) && tok[i+1] >= '0' && tok[i+1] <= '9' {
			seenDot = true
			i++
			continue
		}
		break
	}
	return tok[:i], tok[i:]
}

// ProductKey builds the ASCII search key stored with a product. It combines
// both language names, brand and model, and the canonical quantity token, so a
// query in either script (or with any spelling of the quantity) can match.
func ProductKey(nameEn, nameBn, brand, model string, qty *domain.Quantity) string {
	parts := []string{nameEn, nameBn, brand, model}
	seen := map[string]bool{}
	var words []string
	for _, p := range parts {
		text, _ := canonicalizeQuantities(Normalize(p))
		for w := range strings.FieldsSeq(Key(text)) {
			if !seen[w] {
				seen[w] = true
				words = append(words, w)
			}
		}
	}
	if qty != nil {
		if tok := qty.Token(); !seen[tok] {
			words = append(words, tok)
		}
	}
	return strings.Join(words, " ")
}

// AliasKey builds the ASCII search key for a product alias.
func AliasKey(alias string) string {
	text, _ := canonicalizeQuantities(Normalize(alias))
	return Key(text)
}
