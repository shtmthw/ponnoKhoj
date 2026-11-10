package search

import (
	"strings"
	"unicode"
)

// Key converts normalized text into an ASCII search key containing only
// [a-z0-9. ] by transliterating Bangla and dropping every other non-ASCII
// letter's marks.
//
// Why: pg_trgm builds trigrams from what the database locale considers
// alphanumeric, and Bangla vowel signs (matras) are combining marks, so
// trigrams over raw Bangla text split words at every matra. Matching on an
// ASCII key is locale-independent and lets "chal" find "চাল".
//
// This is a deterministic, rough romanization, not a linguistic one: it
// writes no inherent vowel, so "লবণ" becomes "lbn" rather than "lobon".
// Spellings it cannot bridge are covered by the product alias table.
func Key(text string) string {
	rs := []rune(text)
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == ' ' || r == '.' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r >= 0x0980 && r <= 0x09FF:
			// Nukta forms: NFKC decomposes ড়/ঢ়/য় into base + U+09BC.
			if i+1 < len(rs) && rs[i+1] == 0x09BC {
				if s, ok := banglaNukta[r]; ok {
					b.WriteString(s)
					i++
					continue
				}
			}
			b.WriteString(banglaLatin[r]) // unmapped signs contribute nothing
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// Other scripts are kept out of the key but must still separate
			// words, so neighbours do not fuse into a false match.
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

var banglaNukta = map[rune]string{
	'ড': "r",  // ড়
	'ঢ': "rh", // ঢ়
	'য': "y",  // য়
}

var banglaLatin = map[rune]string{
	// Independent vowels.
	'অ': "o", 'আ': "a", 'ই': "i", 'ঈ': "i", 'উ': "u", 'ঊ': "u", 'ঋ': "ri",
	'এ': "e", 'ঐ': "oi", 'ও': "o", 'ঔ': "ou",
	// Vowel signs.
	'া': "a", 'ি': "i", 'ী': "i", 'ু': "u", 'ূ': "u", 'ৃ': "ri",
	'ে': "e", 'ৈ': "oi", 'ো': "o", 'ৌ': "ou",
	// Consonants.
	'ক': "k", 'খ': "kh", 'গ': "g", 'ঘ': "gh", 'ঙ': "ng",
	'চ': "ch", 'ছ': "chh", 'জ': "j", 'ঝ': "jh", 'ঞ': "n",
	'ট': "t", 'ঠ': "th", 'ড': "d", 'ঢ': "dh", 'ণ': "n",
	'ত': "t", 'থ': "th", 'দ': "d", 'ধ': "dh", 'ন': "n",
	'প': "p", 'ফ': "f", 'ব': "b", 'ভ': "bh", 'ম': "m",
	'য': "j", 'র': "r", 'ল': "l", 'শ': "sh", 'ষ': "sh", 'স': "s", 'হ': "h",
	0x09DC: "r", 0x09DD: "rh", 0x09DF: "y", // precomposed ড় ঢ় য় (NFKC decomposes them, kept for safety)
	'ৎ': "t",
	// Signs.
	'ং': "ng", 'ঃ': "h",
	// Hasanta, chandrabindu, nukta and the rest map to "".
}
