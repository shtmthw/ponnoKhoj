// Package domain holds ponnoKhoj's core types. It performs no I/O.
package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Money is an amount in poisha (1 BDT = 100 poisha). Prices are never floats.
type Money int64

// PoishaPerTaka is the number of poisha in one taka.
const PoishaPerTaka = 100

// MaxMoney caps accepted prices at ৳10 crore, far above anything a price
// comparison site lists, so that arithmetic on sums of prices cannot overflow.
const MaxMoney Money = 100_000_000 * PoishaPerTaka

// Errors returned by ParseDecimalTaka.
var (
	ErrMoneyEmpty     = errors.New("money: empty amount")
	ErrMoneySyntax    = errors.New("money: not a decimal amount")
	ErrMoneyPrecision = errors.New("money: more than two decimal places")
	ErrMoneyRange     = errors.New("money: amount out of range")
)

// ParseDecimalTaka parses a plain ASCII decimal taka amount such as "1250" or
// "1250.50" into poisha. It accepts no currency symbols, grouping separators
// or signs; richer free-text prices are handled by the sources price parser.
func ParseDecimalTaka(s string) (Money, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ErrMoneyEmpty
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" || (hasDot && frac == "") {
		return 0, ErrMoneySyntax
	}
	if len(frac) > 2 {
		for _, c := range frac {
			if c < '0' || c > '9' {
				return 0, ErrMoneySyntax
			}
		}
		if strings.Trim(frac[2:], "0") != "" {
			return 0, ErrMoneyPrecision
		}
		frac = frac[:2]
	}
	var taka int64
	for _, c := range whole {
		if c < '0' || c > '9' {
			return 0, ErrMoneySyntax
		}
		taka = taka*10 + int64(c-'0')
		if taka > int64(MaxMoney)/PoishaPerTaka {
			return 0, ErrMoneyRange
		}
	}
	var poisha int64
	for _, c := range frac {
		if c < '0' || c > '9' {
			return 0, ErrMoneySyntax
		}
		poisha = poisha*10 + int64(c-'0')
	}
	if len(frac) == 1 {
		poisha *= 10
	}
	m := Money(taka*PoishaPerTaka + poisha)
	if m > MaxMoney {
		return 0, ErrMoneyRange
	}
	return m, nil
}

// Valid reports whether m is a price the system accepts (positive, in range).
func (m Money) Valid() bool { return m > 0 && m <= MaxMoney }

// Decimal renders m as a plain decimal taka string with two places ("1250.50").
func (m Money) Decimal() string {
	sign := ""
	v := int64(m)
	if v < 0 {
		sign = "-"
		if v == math.MinInt64 {
			return "-92233720368547758.08"
		}
		v = -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/PoishaPerTaka, v%PoishaPerTaka)
}

// String implements fmt.Stringer using the plain decimal form.
func (m Money) String() string { return m.Decimal() }

// Format renders m for display as "৳1,25,000" using South Asian digit grouping
// (last three digits, then groups of two). Poisha are shown only when non-zero.
// With bangla=true the digits are Bangla digits.
func (m Money) Format(bangla bool) string {
	v := int64(m)
	sign := ""
	if v < 0 {
		sign = "-"
		if v == math.MinInt64 {
			v = math.MaxInt64
		} else {
			v = -v
		}
	}
	out := sign + "৳" + GroupSouthAsian(v/PoishaPerTaka)
	if p := v % PoishaPerTaka; p != 0 {
		out += fmt.Sprintf(".%02d", p)
	}
	if bangla {
		out = ToBanglaDigits(out)
	}
	return out
}

// GroupSouthAsian formats a non-negative integer with lakh/crore grouping:
// 12500000 -> "1,25,00,000".
func GroupSouthAsian(n int64) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 || len(s) <= 3 {
		return s
	}
	head, tail := s[:len(s)-3], s[len(s)-3:]
	var parts []string
	for len(head) > 2 {
		parts = append([]string{head[len(head)-2:]}, parts...)
		head = head[:len(head)-2]
	}
	if head != "" {
		parts = append([]string{head}, parts...)
	}
	return strings.Join(parts, ",") + "," + tail
}

var banglaDigits = [10]rune{'০', '১', '২', '৩', '৪', '৫', '৬', '৭', '৮', '৯'}

// ToBanglaDigits replaces ASCII digits in s with Bangla digits.
func ToBanglaDigits(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 3)
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(banglaDigits[r-'0'])
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ToASCIIDigits replaces Bangla digits in s with ASCII digits.
func ToASCIIDigits(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r >= '০' && r <= '৯' }) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= '০' && r <= '৯' {
			b.WriteByte(byte('0' + (r - '০')))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
