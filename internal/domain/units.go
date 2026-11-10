package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// Unit is a normalized base unit. Every quantity is stored in one of these.
type Unit string

// Base units.
const (
	UnitGram       Unit = "g"
	UnitMillilitre Unit = "ml"
	UnitPiece      Unit = "pc"
)

// ValidUnit reports whether u is one of the base units.
func ValidUnit(u Unit) bool {
	return u == UnitGram || u == UnitMillilitre || u == UnitPiece
}

// Quantity is an amount in a base unit: 5 kg is {5000, "g"}.
type Quantity struct {
	Value int64
	Unit  Unit
}

// MaxQuantity bounds quantity values (1000 tonnes / 1000 kilolitres / a
// million pieces) so multiplications cannot overflow.
const MaxQuantity int64 = 1_000_000_000

// unitAliases maps the spellings vendors and shoppers actually type to a base
// unit and a multiplier expressed in thousandths (so "kg" is 1000 g = 1_000_000
// milli-grams), which lets decimal inputs like "1.5 kg" stay in integers.
var unitAliases = map[string]struct {
	unit  Unit
	milli int64 // base units per one of this unit, times 1000
}{
	"g": {UnitGram, 1000}, "gm": {UnitGram, 1000}, "gms": {UnitGram, 1000}, "gram": {UnitGram, 1000}, "grams": {UnitGram, 1000},
	"গ্রাম": {UnitGram, 1000}, "গ্রা": {UnitGram, 1000},
	"kg": {UnitGram, 1_000_000}, "kgs": {UnitGram, 1_000_000}, "kilo": {UnitGram, 1_000_000}, "kilogram": {UnitGram, 1_000_000}, "kilograms": {UnitGram, 1_000_000},
	"কেজি": {UnitGram, 1_000_000}, "কিলো": {UnitGram, 1_000_000}, "কিলোগ্রাম": {UnitGram, 1_000_000},
	"ml": {UnitMillilitre, 1000}, "মিলি": {UnitMillilitre, 1000}, "মিলিলিটার": {UnitMillilitre, 1000}, "millilitre": {UnitMillilitre, 1000}, "milliliter": {UnitMillilitre, 1000},
	"l": {UnitMillilitre, 1_000_000}, "lt": {UnitMillilitre, 1_000_000}, "ltr": {UnitMillilitre, 1_000_000}, "litre": {UnitMillilitre, 1_000_000}, "liter": {UnitMillilitre, 1_000_000}, "litres": {UnitMillilitre, 1_000_000}, "liters": {UnitMillilitre, 1_000_000},
	"লিটার": {UnitMillilitre, 1_000_000}, "লি": {UnitMillilitre, 1_000_000},
	"pc": {UnitPiece, 1000}, "pcs": {UnitPiece, 1000}, "piece": {UnitPiece, 1000}, "pieces": {UnitPiece, 1000},
	"পিস": {UnitPiece, 1000}, "টি": {UnitPiece, 1000}, "টা": {UnitPiece, 1000},
	"dozen": {UnitPiece, 12_000}, "ডজন": {UnitPiece, 12_000},
	"hali": {UnitPiece, 4000}, "হালি": {UnitPiece, 4000},
}

// IsUnitWord reports whether word (already lower-cased and NFKC-normalized) is
// a recognized unit spelling.
func IsUnitWord(word string) bool {
	_, ok := unitAliases[word]
	return ok
}

// ParseQuantity converts a decimal number string (ASCII digits, optional single
// '.') and a unit word into a Quantity in base units. It returns false when the
// unit is unknown, the number is malformed, or the result is not a positive
// whole number of base units within range.
func ParseQuantity(number, unitWord string) (Quantity, bool) {
	ua, ok := unitAliases[unitWord]
	if !ok {
		return Quantity{}, false
	}
	milli, ok := parseMilli(number)
	if !ok || milli <= 0 {
		return Quantity{}, false
	}
	// milli is thousandths of the typed unit; ua.milli is thousandths of base
	// units per typed unit. Both are bounded so the product fits in int64.
	total := milli * ua.milli
	if total%1_000_000 != 0 {
		return Quantity{}, false
	}
	v := total / 1_000_000
	if v <= 0 || v > MaxQuantity {
		return Quantity{}, false
	}
	return Quantity{Value: v, Unit: ua.unit}, true
}

// parseMilli parses "1", "1.5", "0.25" into thousandths (1000, 1500, 250).
// Numbers above one million or with more than three decimals are rejected.
func parseMilli(s string) (int64, bool) {
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" || len(whole) > 7 || (hasDot && (frac == "" || len(frac) > 3)) {
		return 0, false
	}
	for _, c := range whole + frac {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > 1_000_000 {
		return 0, false
	}
	var f int64
	if frac != "" {
		f, err = strconv.ParseInt((frac + "000")[:3], 10, 64)
		if err != nil {
			return 0, false
		}
	}
	return w*1000 + f, true
}

// Token is the canonical search token for a quantity, e.g. "5000g". Queries
// and product keys both contain it, which is how "5 kg", "5kg" and "৫ কেজি"
// all match the same product.
func (q Quantity) Token() string {
	return strconv.FormatInt(q.Value, 10) + string(q.Unit)
}

// Format renders a quantity for people: 5000 g -> "5 kg", 250 ml -> "250 ml",
// 12 pc -> "12 pcs". With bangla=true it uses Bangla digits and unit names.
func (q Quantity) Format(bangla bool) string {
	var num, unit string
	switch q.Unit {
	case UnitGram:
		num, unit = scaled(q.Value, "g", "kg")
		if bangla {
			unit = map[string]string{"g": "গ্রাম", "kg": "কেজি"}[unit]
		}
	case UnitMillilitre:
		num, unit = scaled(q.Value, "ml", "L")
		if bangla {
			unit = map[string]string{"ml": "মিলি", "L": "লিটার"}[unit]
		}
	case UnitPiece:
		num = strconv.FormatInt(q.Value, 10)
		unit = "pcs"
		if q.Value == 1 {
			unit = "pc"
		}
		if bangla {
			unit = "টি"
		}
	default:
		return fmt.Sprintf("%d %s", q.Value, q.Unit)
	}
	if bangla {
		num = ToBanglaDigits(num)
	}
	return num + " " + unit
}

func scaled(v int64, small, big string) (string, string) {
	if v < 1000 {
		return strconv.FormatInt(v, 10), small
	}
	whole, rem := v/1000, v%1000
	if rem == 0 {
		return strconv.FormatInt(whole, 10), big
	}
	frac := strings.TrimRight(fmt.Sprintf("%03d", rem), "0")
	return strconv.FormatInt(whole, 10) + "." + frac, big
}
