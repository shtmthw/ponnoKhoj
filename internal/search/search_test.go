package search

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
)

func TestNormalize(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"  Miniket   RICE  ", "miniket rice"},
		{"মিনিকেট চাল", "মিনিকেট চাল"},
		{"চাল ৫ কেজি", "চাল 5 কেজি"},
		{"৳ ১,২৫০", "1 250"},
		{"1.5 kg", "1.5 kg"},
		{"১.৫ কেজি", "1.5 কেজি"},
		{"rice.", "rice"},
		{".5kg", "5kg"},
		{"Walton WFC-3F5-GDEL-XX", "walton wfc 3f5 gdel xx"},
		{"ｆｕｌｌｗｉｄｔｈ ＫＧ", "fullwidth kg"},      // NFKC folds width
		{"rice\u200cbran\u200d", "rice bran"}, // joiners separate
		{"tea/coffee & sugar", "tea coffee sugar"},
		{"পেঁয়াজ", "পেঁয়াজ"},
		{"line\nbreak\ttab", "line break tab"},
		{"bad\xffutf8", "bad utf8"},
	}
	for _, tc := range tests {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeTruncates(t *testing.T) {
	t.Parallel()
	got := Normalize(strings.Repeat("চাল ", 500))
	if n := utf8.RuneCountInString(got); n > MaxQueryRunes {
		t.Errorf("normalized length = %d runes, want <= %d", n, MaxQueryRunes)
	}
	if strings.HasSuffix(got, " ") {
		t.Error("truncated output ends in a space")
	}
}

func TestKey(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"চাল", "chal"},
		{"মিনিকেট চাল", "miniket chal"},
		{"তেল", "tel"},
		{"ডাল", "dal"},
		{"আলু", "alu"},
		{"চিনি", "chini"},
		{"পেঁয়াজ", "peyaj"},
		{"ডিম", "dim"},
		{"সয়াবিন তেল 1000ml", "syabin tel 1000ml"},
		{"miniket rice 5000g", "miniket rice 5000g"},
		{"rice чай tea", "rice tea"},
		{"1.5", "1.5"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := Key(Normalize(tc.in)); got != tc.want {
			t.Errorf("Key(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseQueryUnits(t *testing.T) {
	t.Parallel()
	kg1 := domain.Quantity{Value: 1000, Unit: domain.UnitGram}
	l1 := domain.Quantity{Value: 1000, Unit: domain.UnitMillilitre}
	tests := []struct {
		in       string
		wantKey  string
		wantQty  *domain.Quantity
		wantText string
	}{
		// The brief's equivalence: 1kg = 1000g = ১ কেজি.
		{"rice 1kg", "rice 1000g", &kg1, "rice 1000g"},
		{"rice 1000g", "rice 1000g", &kg1, "rice 1000g"},
		{"rice ১ কেজি", "rice 1000g", &kg1, "rice 1000g"},
		{"Rice 1 KG", "rice 1000g", &kg1, "rice 1000g"},
		{"rice 1000 gm", "rice 1000g", &kg1, "rice 1000g"},
		{"চাল ১কেজি", "chal 1000g", &kg1, "চাল 1000g"},
		// ltr / L / লিটার.
		{"oil 1 ltr", "oil 1000ml", &l1, "oil 1000ml"},
		{"oil 1L", "oil 1000ml", &l1, "oil 1000ml"},
		{"তেল ১ লিটার", "tel 1000ml", &l1, "তেল 1000ml"},
		{"oil 1000 ml", "oil 1000ml", &l1, "oil 1000ml"},
		{"1.5 kg flour", "1500g flour", &domain.Quantity{Value: 1500, Unit: domain.UnitGram}, "1500g flour"},
		{"eggs 1 dozen", "eggs 12pc", &domain.Quantity{Value: 12, Unit: domain.UnitPiece}, "eggs 12pc"},
		{"ডিম ১ হালি", "dim 4pc", &domain.Quantity{Value: 4, Unit: domain.UnitPiece}, "ডিম 4pc"},
		// Numbers that are not quantities stay as they are.
		{"iphone 15", "iphone 15", nil, "iphone 15"},
		{"galaxy a15 5g", "galaxy a15 5g", &domain.Quantity{Value: 5, Unit: domain.UnitGram}, "galaxy a15 5g"},
		{"7 up", "7 up", nil, "7 up"},
		{"1.5 g saffron", "1.5 g saffron", nil, "1.5 g saffron"},
		{"", "", nil, ""},
	}
	for _, tc := range tests {
		q := ParseQuery(tc.in)
		if q.Key != tc.wantKey || q.Text != tc.wantText {
			t.Errorf("ParseQuery(%q): key %q text %q, want key %q text %q", tc.in, q.Key, q.Text, tc.wantKey, tc.wantText)
		}
		switch {
		case (q.Quantity == nil) != (tc.wantQty == nil):
			t.Errorf("ParseQuery(%q): quantity %v, want %v", tc.in, q.Quantity, tc.wantQty)
		case q.Quantity != nil && *q.Quantity != *tc.wantQty:
			t.Errorf("ParseQuery(%q): quantity %+v, want %+v", tc.in, *q.Quantity, *tc.wantQty)
		}
		if q.Empty() != (tc.wantKey == "") {
			t.Errorf("ParseQuery(%q).Empty() wrong", tc.in)
		}
	}
}

func TestParseQueryFirstQuantityWins(t *testing.T) {
	t.Parallel()
	q := ParseQuery("2 kg rice 500 ml oil")
	if q.Quantity == nil || *q.Quantity != (domain.Quantity{Value: 2000, Unit: domain.UnitGram}) {
		t.Fatalf("quantity = %v", q.Quantity)
	}
	if q.Key != "2000g rice 500ml oil" {
		t.Errorf("key = %q", q.Key)
	}
}

func TestProductKey(t *testing.T) {
	t.Parallel()
	qty := &domain.Quantity{Value: 5000, Unit: domain.UnitGram}
	got := ProductKey("Miniket Rice 5 kg", "মিনিকেট চাল ৫ কেজি", "Chashi", "", qty)
	want := "miniket rice 5000g chal chashi"
	if got != want {
		t.Errorf("ProductKey = %q, want %q", got, want)
	}
	if got := ProductKey("Egg", "ডিম", "", "", nil); got != "egg dim" {
		t.Errorf("ProductKey without quantity = %q", got)
	}
	if got := ProductKey("Soap", "", "Lux", "", &domain.Quantity{Value: 100, Unit: domain.UnitGram}); got != "soap lux 100g" {
		t.Errorf("ProductKey appends token = %q", got)
	}
	if got := AliasKey("চাউল ৫ কেজি"); got != "chaul 5000g" {
		t.Errorf("AliasKey = %q", got)
	}
}

func TestRank(t *testing.T) {
	t.Parallel()
	kg5 := &domain.Quantity{Value: 5000, Unit: domain.UnitGram}
	kg1 := &domain.Quantity{Value: 1000, Unit: domain.UnitGram}
	q := ParseQuery("miniket rice 5kg")
	cands := []Candidate{
		{ProductID: 1, Similarity: 0.80, Quantity: kg1}, // good text, wrong size
		{ProductID: 2, Similarity: 0.70, Quantity: kg5}, // right size
		{ProductID: 3, Similarity: 0.70, Quantity: nil},
		{ProductID: 5, Similarity: 0.40, Quantity: kg5},
		{ProductID: 4, Similarity: 0.40, Quantity: kg5},
	}
	got := Rank(q, cands)
	var ids []int64
	for _, c := range got {
		ids = append(ids, c.ProductID)
	}
	want := []int64{2, 3, 1, 4, 5}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("rank order = %v, want %v", ids, want)
		}
	}
	// Without a query quantity only similarity and ID matter.
	plain := Rank(ParseQuery("rice"), []Candidate{{ProductID: 9, Similarity: 0.5, Quantity: kg1}, {ProductID: 8, Similarity: 0.5}})
	if plain[0].ProductID != 8 {
		t.Errorf("tie should break by ID, got %d first", plain[0].ProductID)
	}
}

func FuzzNormalize(f *testing.F) {
	for _, s := range []string{"", "Miniket Rice 5 kg", "মিনিকেট চাল ৫ কেজি", "৳ ১,২৫০.৫০", "1.5kg", "a.b.c", "ǅ İ ß ﬁ", "\xff\xfe", "ড়ঢ়য়", "é̀", ".5.", "１２３ ＫＧ", "ẞͅΣ"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n := Normalize(s)
		if !utf8.ValidString(n) {
			t.Fatalf("Normalize(%q) is not valid UTF-8: %q", s, n)
		}
		if utf8.RuneCountInString(n) > MaxQueryRunes {
			t.Fatalf("Normalize(%q) exceeds %d runes", s, MaxQueryRunes)
		}
		if n != strings.TrimSpace(n) || strings.Contains(n, "  ") {
			t.Fatalf("Normalize(%q) = %q has stray whitespace", s, n)
		}
		if again := Normalize(n); again != n {
			t.Fatalf("Normalize is not idempotent for %q: %q -> %q", s, n, again)
		}
		q := ParseQuery(s)
		for _, r := range q.Key {
			if r != ' ' && r != '.' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				t.Fatalf("Key(%q) = %q contains %q", s, q.Key, r)
			}
		}
		if q.Quantity != nil && (q.Quantity.Value <= 0 || !domain.ValidUnit(q.Quantity.Unit)) {
			t.Fatalf("ParseQuery(%q) produced invalid quantity %+v", s, *q.Quantity)
		}
	})
}
