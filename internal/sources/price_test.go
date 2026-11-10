package sources

import (
	"errors"
	"strings"
	"testing"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
)

func TestParsePrice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    domain.Money
		wantErr error
	}{
		// The formats named in the brief.
		{"৳ ১,২৫০", 125000, nil},
		{"Tk 1,250.00", 125000, nil},
		{"BDT 1250", 125000, nil},
		{"1,250/-", 125000, nil},
		// Variations.
		{"৳1250", 125000, nil},
		{"Tk. 1,250", 125000, nil},
		{"TK 1250.5", 125050, nil},
		{"1250 টাকা", 125000, nil},
		{"১২৫০ টাকা", 125000, nil},
		{"৳ ১২,৫০,০০০", 125000000, nil}, // South Asian grouping
		{"BDT 1,250,000", 125000000, nil},
		{"  85  ", 8500, nil},
		{"12.50", 1250, nil},
		{"Price: ৳ 99", 9900, nil},
		{"৳ 1,250.00/=", 125000, nil},
		// Missing.
		{"", 0, ErrPriceMissing},
		{"Call for price", 0, ErrPriceMissing},
		{"৳", 0, ErrPriceMissing},
		// Malformed.
		{"1,25", 0, ErrPriceMalformed},
		{"1,2500", 0, ErrPriceMalformed},
		{"1,,250", 0, ErrPriceMalformed},
		{"1250.999", 0, ErrPriceMalformed},
		{"-500", 0, ErrPriceMalformed},
		{".50", 0, ErrPriceMalformed},
		{strings.Repeat("9", 600), 0, ErrPriceMalformed},
		// Out of range.
		{"0", 0, ErrPriceOutOfRange},
		{"0.00", 0, ErrPriceOutOfRange},
		{"৳ 999,999,999,999", 0, ErrPriceOutOfRange},
		{"1234567890123456", 0, ErrPriceOutOfRange},
		// Ambiguous: more than one number that is not a range.
		{"৳ 1,500 ৳ 1,250", 0, ErrPriceAmbiguous},
		{"৳ 1,250 (5 kg)", 0, ErrPriceAmbiguous},
		{"1 2 3", 0, ErrPriceAmbiguous},
		{"1.2.3", 0, ErrPriceAmbiguous},
		// Foreign currency.
		{"$ 12.50", 0, ErrPriceForeignCurrency},
		{"USD 100", 0, ErrPriceForeignCurrency},
		{"Rs 500", 0, ErrPriceForeignCurrency},
		{"₹500", 0, ErrPriceForeignCurrency},
	}
	for _, tc := range tests {
		got, err := ParsePrice(tc.in)
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("ParsePrice(%q) err = %v, want %v", tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("ParsePrice(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParsePriceRange(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"৳ 1,200 - ৳ 1,500", "Tk 1200 – Tk 1500", "1200 to 1500", "১২০০ থেকে ১৫০০ টাকা", "1,200/- - 1,500/-", "1200~1500"} {
		_, err := ParsePrice(in)
		var re *PriceRangeError
		if !errors.As(err, &re) {
			t.Errorf("ParsePrice(%q) err = %v, want PriceRangeError", in, err)
			continue
		}
		if re.Low != 120000 || re.High != 150000 {
			t.Errorf("ParsePrice(%q) range = %d..%d", in, re.Low, re.High)
		}
		if !strings.Contains(re.Error(), "1200.00 to 1500.00") {
			t.Errorf("range error text = %q", re.Error())
		}
	}
	// A degenerate range is just a price; an inverted one is malformed.
	if got, err := ParsePrice("৳ 500 - ৳ 500"); err != nil || got != 50000 {
		t.Errorf("degenerate range = %d, %v", got, err)
	}
	if _, err := ParsePrice("৳ 900 - ৳ 500"); !errors.Is(err, ErrPriceMalformed) {
		t.Errorf("inverted range err = %v", err)
	}
	if _, err := ParsePrice("1,25 - 500"); !errors.Is(err, ErrPriceMalformed) {
		t.Errorf("range with bad low err = %v", err)
	}
	if _, err := ParsePrice("500 - 1,25"); !errors.Is(err, ErrPriceMalformed) {
		t.Errorf("range with bad high err = %v", err)
	}
}

func TestParsePriceHTML(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    domain.Money
		wantErr error
	}{
		// Struck-through "was" price vs current price.
		{`<del>৳ 1,500</del> <ins>৳ 1,250</ins>`, 125000, nil},
		{`<s>Tk 1500</s><span>Tk 1250</span>`, 125000, nil},
		{`<strike>1500</strike> 1250/-`, 125000, nil},
		{`<span class="price"><del><span>৳</span>১,৫০০</del> <span>৳</span>১,২৫০</span>`, 125000, nil},
		{`<span>৳ 99</span><script>var p = 12345;</script>`, 9900, nil},
		{`plain ৳ 80`, 8000, nil},
		// Two live prices cannot be told apart without semantic markup.
		{`<span>৳ 1,500</span> <span>৳ 1,250</span>`, 0, ErrPriceAmbiguous},
		{`<del>৳ 1,500</del>`, 0, ErrPriceMissing},
		{``, 0, ErrPriceMissing},
		{strings.Repeat("<b>", 5000), 0, ErrPriceMalformed},
	}
	for _, tc := range tests {
		got, err := ParsePriceHTML(tc.in)
		if !errors.Is(err, tc.wantErr) || got != tc.want {
			t.Errorf("ParsePriceHTML(%q) = %d, %v; want %d, %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestHasBDTMarker(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{"৳ 50": true, "Tk 50": true, "50 BDT": true, "৫০ টাকা": true, "50 taka": true, "50": false, "tkt 50": false, "$50": false} {
		if got := HasBDTMarker(in); got != want {
			t.Errorf("HasBDTMarker(%q) = %v", in, got)
		}
	}
}

func FuzzParsePrice(f *testing.F) {
	for _, s := range []string{"৳ ১,২৫০", "Tk 1,250.00", "BDT 1250", "1,250/-", "৳ 1,200 - ৳ 1,500", "<del>৳1,500</del> ৳1,250", "", "-", "1,", ",1", "1.", "9999999999999999999999", "১২৫০ টাকা", "$5", "1-2-3", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for name, parse := range map[string]func(string) (domain.Money, error){"text": ParsePrice, "html": ParsePriceHTML} {
			m, err := parse(s)
			if err == nil && !m.Valid() {
				t.Fatalf("%s: ParsePrice(%q) returned invalid money %d without an error", name, s, m)
			}
			if err != nil && m != 0 {
				t.Fatalf("%s: ParsePrice(%q) returned %d together with error %v", name, s, m, err)
			}
			var re *PriceRangeError
			if errors.As(err, &re) && (!re.Low.Valid() || !re.High.Valid() || re.Low >= re.High) {
				t.Fatalf("%s: ParsePrice(%q) returned a bad range %+v", name, s, re)
			}
		}
	})
}
