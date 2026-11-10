package matching

import (
	"testing"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
)

func TestConfidence(t *testing.T) {
	t.Parallel()
	rice := Product{NameEn: "Miniket Rice 5 kg", NameBn: "মিনিকেট চাল ৫ কেজি", Brand: "Chashi", Quantity: domain.Quantity{Value: 5000, Unit: domain.UnitGram}}
	fridge := Product{NameEn: "Refrigerator", NameBn: "রেফ্রিজারেটর", Brand: "Walton", Model: "WFC-3F5-GDEL-XX", Quantity: domain.Quantity{Value: 1, Unit: domain.UnitPiece}}
	egg := Product{NameEn: "Farm Egg 12 pcs", NameBn: "ফার্মের ডিম ১২ টি", Quantity: domain.Quantity{Value: 12, Unit: domain.UnitPiece}}

	tests := []struct {
		name     string
		p        Product
		title    string
		min, max float64
	}{
		{"exact with brand and size", rice, "Chashi Miniket Rice 5kg", 0.95, 1},
		{"bangla title", rice, "চাষী মিনিকেট চাল ৫ কেজি", 0.7, 1},
		{"size spelled differently", rice, "Chashi Miniket Rice - 5000 gm", 0.95, 1},
		{"no size in title", rice, "Chashi Miniket Rice", 0.85, 0.95},
		{"brand missing", rice, "Miniket Rice 5 kg", 0.7, 0.8},
		{"different size", rice, "Chashi Miniket Rice 1kg", 0, 0.2},
		{"different product", rice, "Chashi Mustard Oil 5 kg", 0, 0.59},
		{"unrelated", rice, "Samsung Galaxy A15", 0, 0},
		{"empty title", rice, "", 0, 0},
		{"model present", fridge, "Walton Refrigerator WFC-3F5-GDEL-XX", 0.9, 1},
		{"model absent", fridge, "Walton Refrigerator 380L", 0, 0.3},
		{"other model", fridge, "Walton Refrigerator WFE-2H2-GDXX-XX", 0, 0.3},
		{"no brand product", egg, "Farm Egg (1 dozen)", 0.9, 1},
		{"no brand product, other size", egg, "Farm Egg 4 pcs", 0, 0.2},
	}
	for _, tc := range tests {
		got := Confidence(tc.p, tc.title)
		if got < tc.min || got > tc.max {
			t.Errorf("%s: Confidence(%q) = %.3f, want in [%.2f, %.2f]", tc.name, tc.title, got, tc.min, tc.max)
		}
		if got < 0 || got > 1 {
			t.Errorf("%s: confidence %.3f out of [0,1]", tc.name, got)
		}
	}
	// The storage threshold separates the clear matches from the rest.
	if Confidence(rice, "Chashi Miniket Rice 5kg") < MinConfidence || Confidence(rice, "Chashi Miniket Rice 1kg") >= MinConfidence {
		t.Error("MinConfidence does not separate a match from a size mismatch")
	}
}

func TestVariantsAreDifferentProducts(t *testing.T) {
	t.Parallel()
	pc := domain.Quantity{Value: 1, Unit: domain.UnitPiece}
	router := Product{NameEn: "Archer C6 AC1200 Gigabit Router", Brand: "TP-Link", Model: "Archer C6", Quantity: pc}
	bank := Product{NameEn: "Zolo 10000mAh 35W Power Bank", Brand: "Anker", Model: "Zolo", Quantity: pc}
	drive := Product{NameEn: "Cruzer Glide 128GB USB 3.0 Flash Drive", Brand: "SanDisk", Model: "Cruzer Glide", Quantity: pc}
	tests := []struct {
		name     string
		p        Product
		title    string
		min, max float64
	}{
		{"same router", router, "TP-Link Archer C6 AC1200 Dual Band Gigabit WiFi Router", 0.85, 1},
		{"router C64 is another model", router, "TP-Link Archer C64 AC1200 WiFi Router", 0, 0.2},
		{"router C6U is another model (rejected by the model rule)", router, "TP-Link Archer C6U Dual Band Gigabit Router", 0, MinConfidence - 0.01},
		{"same power bank", bank, "Anker Zolo 10000mAh 35W Power Bank With Built-in Dual Type-C Cable", 0.85, 1},
		{"bigger power bank", bank, "Anker Zolo 20000mAh 22.5W Power Bank with Built-in Type-C Cable", 0, 0.2},
		{"model code is not a size", drive, "SanDisk Cruzer Glide 128GB USB 3.0 Flash Drive (SDCZ600-128G-G35)", 0.85, 1},
		{"smaller drive", drive, "SanDisk Cruzer Glide CZ600 32GB USB 3.0 Black Pendrive", 0, 0.2},
	}
	for _, tc := range tests {
		if got := Confidence(tc.p, tc.title); got < tc.min || got > tc.max {
			t.Errorf("%s: Confidence(%q) = %.2f, want in [%.2f, %.2f]", tc.name, tc.title, got, tc.min, tc.max)
		}
	}
	for in, want := range map[string]string{"10000mah": "#mah", "ac1200": "ac#", "3.0": "#.#", "x20": "x#", "c6u": "c#u", "plain": "plain"} {
		if got := shape(in); got != want {
			t.Errorf("shape(%q) = %q, want %q", in, got, want)
		}
	}
}
