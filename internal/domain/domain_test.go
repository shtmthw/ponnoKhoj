package domain

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestParseDecimalTaka(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    Money
		wantErr error
	}{
		{"1250", 125000, nil},
		{"1250.5", 125050, nil},
		{"1250.50", 125050, nil},
		{"1250.500", 125050, nil},
		{" 0.01 ", 1, nil},
		{"0", 0, nil},
		{"100000000", MaxMoney, nil},
		{"", 0, ErrMoneyEmpty},
		{"   ", 0, ErrMoneyEmpty},
		{".5", 0, ErrMoneySyntax},
		{"5.", 0, ErrMoneySyntax},
		{"1,250", 0, ErrMoneySyntax},
		{"-5", 0, ErrMoneySyntax},
		{"+5", 0, ErrMoneySyntax},
		{"12a", 0, ErrMoneySyntax},
		{"1.2x", 0, ErrMoneySyntax},
		{"1.23x", 0, ErrMoneySyntax},
		{"1.2.3", 0, ErrMoneySyntax},
		{"৫০", 0, ErrMoneySyntax},
		{"1.255", 0, ErrMoneyPrecision},
		{"100000000.01", 0, ErrMoneyRange},
		{"999999999999999999999999", 0, ErrMoneyRange},
	}
	for _, tc := range tests {
		got, err := ParseDecimalTaka(tc.in)
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("ParseDecimalTaka(%q) err = %v, want %v", tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseDecimalTaka(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestMoneyFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		m      Money
		en, bn string
		dec    string
	}{
		{0, "৳0", "৳০", "0.00"},
		{5000, "৳50", "৳৫০", "50.00"},
		{99999, "৳999.99", "৳৯৯৯.৯৯", "999.99"},
		{125000, "৳1,250", "৳১,২৫০", "1250.00"},
		{125005, "৳1,250.05", "৳১,২৫০.০৫", "1250.05"},
		{12500000, "৳1,25,000", "৳১,২৫,০০০", "125000.00"},
		{1250000000, "৳1,25,00,000", "৳১,২৫,০০,০০০", "12500000.00"},
		{-125050, "-৳1,250.50", "-৳১,২৫০.৫০", "-1250.50"},
	}
	for _, tc := range tests {
		if got := tc.m.Format(false); got != tc.en {
			t.Errorf("Format(%d, en) = %q, want %q", tc.m, got, tc.en)
		}
		if got := tc.m.Format(true); got != tc.bn {
			t.Errorf("Format(%d, bn) = %q, want %q", tc.m, got, tc.bn)
		}
		if got := tc.m.Decimal(); got != tc.dec {
			t.Errorf("Decimal(%d) = %q, want %q", tc.m, got, tc.dec)
		}
		if tc.m.String() != tc.dec {
			t.Errorf("String(%d) != Decimal", tc.m)
		}
	}
	// Extremes must not panic or overflow into nonsense.
	if got := Money(math.MinInt64).Decimal(); got != "-92233720368547758.08" {
		t.Errorf("MinInt64 Decimal = %q", got)
	}
	if got := Money(math.MinInt64).Format(false); got == "" {
		t.Error("MinInt64 Format is empty")
	}
}

func TestMoneyRoundTrip(t *testing.T) {
	t.Parallel()
	for _, m := range []Money{1, 99, 100, 101, 123456, MaxMoney} {
		got, err := ParseDecimalTaka(m.Decimal())
		if err != nil || got != m {
			t.Errorf("round trip %d: got %d, err %v", m, got, err)
		}
	}
}

func TestMoneyValid(t *testing.T) {
	t.Parallel()
	for m, want := range map[Money]bool{0: false, -1: false, 1: true, MaxMoney: true, MaxMoney + 1: false} {
		if m.Valid() != want {
			t.Errorf("Valid(%d) = %v, want %v", m, !want, want)
		}
	}
}

func TestGroupSouthAsian(t *testing.T) {
	t.Parallel()
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 99999: "99,999", 100000: "1,00,000", 12345678: "1,23,45,678", -5: "-5"} {
		if got := GroupSouthAsian(n); got != want {
			t.Errorf("GroupSouthAsian(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDigits(t *testing.T) {
	t.Parallel()
	if got := ToBanglaDigits("Tk 1,250.50"); got != "Tk ১,২৫০.৫০" {
		t.Errorf("ToBanglaDigits = %q", got)
	}
	if got := ToASCIIDigits("৳ ১,২৫০ টাকা 9"); got != "৳ 1,250 টাকা 9" {
		t.Errorf("ToASCIIDigits = %q", got)
	}
	if got := ToASCIIDigits("no digits"); got != "no digits" {
		t.Errorf("ToASCIIDigits passthrough = %q", got)
	}
}

func TestParseQuantity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		num, unit string
		want      Quantity
		ok        bool
	}{
		{"1", "kg", Quantity{1000, UnitGram}, true},
		{"1000", "g", Quantity{1000, UnitGram}, true},
		{"1", "কেজি", Quantity{1000, UnitGram}, true},
		{"1.5", "kg", Quantity{1500, UnitGram}, true},
		{"0.25", "kg", Quantity{250, UnitGram}, true},
		{"500", "gm", Quantity{500, UnitGram}, true},
		{"500", "গ্রাম", Quantity{500, UnitGram}, true},
		{"2", "ltr", Quantity{2000, UnitMillilitre}, true},
		{"2", "l", Quantity{2000, UnitMillilitre}, true},
		{"2", "লিটার", Quantity{2000, UnitMillilitre}, true},
		{"250", "ml", Quantity{250, UnitMillilitre}, true},
		{"1", "dozen", Quantity{12, UnitPiece}, true},
		{"1", "ডজন", Quantity{12, UnitPiece}, true},
		{"0.5", "dozen", Quantity{6, UnitPiece}, true},
		{"2", "হালি", Quantity{8, UnitPiece}, true},
		{"3", "pcs", Quantity{3, UnitPiece}, true},
		{"1", "furlong", Quantity{}, false},
		{"0", "kg", Quantity{}, false},
		{"1.5", "g", Quantity{}, false},  // half a gram is not a whole base unit
		{"0.1", "pc", Quantity{}, false}, // a tenth of a piece
		{"abc", "kg", Quantity{}, false},
		{"1.", "kg", Quantity{}, false},
		{".5", "kg", Quantity{}, false},
		{"1.2345", "kg", Quantity{}, false},
		{"99999999", "kg", Quantity{}, false},
		{"1000000", "kg", Quantity{MaxQuantity, UnitGram}, true},
		{"1000000", "dozen", Quantity{12_000_000, UnitPiece}, true},
		{"", "kg", Quantity{}, false},
	}
	for _, tc := range tests {
		got, ok := ParseQuantity(tc.num, tc.unit)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseQuantity(%q, %q) = %+v, %v; want %+v, %v", tc.num, tc.unit, got, ok, tc.want, tc.ok)
		}
	}
	if !IsUnitWord("কেজি") || IsUnitWord("rice") {
		t.Error("IsUnitWord misclassified")
	}
}

func TestQuantityFormatAndToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		q              Quantity
		en, bn, tok    string
		validBaseUnits bool
	}{
		{Quantity{5000, UnitGram}, "5 kg", "৫ কেজি", "5000g", true},
		{Quantity{500, UnitGram}, "500 g", "৫০০ গ্রাম", "500g", true},
		{Quantity{1500, UnitGram}, "1.5 kg", "১.৫ কেজি", "1500g", true},
		{Quantity{1050, UnitGram}, "1.05 kg", "১.০৫ কেজি", "1050g", true},
		{Quantity{2000, UnitMillilitre}, "2 L", "২ লিটার", "2000ml", true},
		{Quantity{250, UnitMillilitre}, "250 ml", "২৫০ মিলি", "250ml", true},
		{Quantity{12, UnitPiece}, "12 pcs", "১২ টি", "12pc", true},
		{Quantity{1, UnitPiece}, "1 pc", "১ টি", "1pc", true},
		{Quantity{3, Unit("box")}, "3 box", "3 box", "3box", false},
	}
	for _, tc := range tests {
		if got := tc.q.Format(false); got != tc.en {
			t.Errorf("Format(%+v, en) = %q, want %q", tc.q, got, tc.en)
		}
		if got := tc.q.Format(true); got != tc.bn {
			t.Errorf("Format(%+v, bn) = %q, want %q", tc.q, got, tc.bn)
		}
		if got := tc.q.Token(); got != tc.tok {
			t.Errorf("Token(%+v) = %q, want %q", tc.q, got, tc.tok)
		}
		if ValidUnit(tc.q.Unit) != tc.validBaseUnits {
			t.Errorf("ValidUnit(%q) wrong", tc.q.Unit)
		}
	}
}

func TestFreshness(t *testing.T) {
	t.Parallel()
	p := DefaultFreshnessPolicy()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	tests := []struct {
		age  time.Duration
		want Freshness
	}{
		{0, Fresh}, {7 * day, Fresh}, {7*day + time.Second, Stale},
		{30 * day, Stale}, {30*day + time.Second, Hidden}, {-time.Hour, Fresh},
	}
	for _, tc := range tests {
		if got := p.Of(now.Add(-tc.age), now); got != tc.want {
			t.Errorf("freshness at age %v = %s, want %s", tc.age, got, tc.want)
		}
	}
}

func TestEnums(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]Mode{"": ModeAll, "all": ModeAll, "online": ModeOnline, "local": ModeLocal, "estimate": ModeEstimate} {
		got, ok := ParseMode(in)
		if !ok || got != want {
			t.Errorf("ParseMode(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := ParseMode("offline"); ok {
		t.Error("ParseMode accepted an unknown mode")
	}
	if !ValidConfidence(ConfidenceLow) || !ValidConfidence(ConfidenceMedium) || !ValidConfidence(ConfidenceHigh) || ValidConfidence("certain") {
		t.Error("ValidConfidence wrong")
	}
	if !ValidReportReason(ReportWrongPrice) || !ValidReportReason(ReportClosed) || !ValidReportReason(ReportFake) || ValidReportReason("spam") {
		t.Error("ValidReportReason wrong")
	}
}
