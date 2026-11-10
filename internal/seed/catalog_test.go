package seed

import (
	"encoding/json"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/matching"
	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/sources"
)

// TestCatalogMatchesObservedTitles checks every reference product against
// the titles its own product pages showed (test/fixtures/sources/
// catalog_titles.json): each must match, and no product may match another
// product's pages, so a refresh never files an offer under the wrong item.
func TestCatalogMatchesObservedTitles(t *testing.T) {
	t.Parallel()
	products, err := ReferenceCatalog()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../test/fixtures/sources/catalog_titles.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Titles map[string]map[string]string `json:"titles"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	slugOf := func(p CatalogProduct) string { return path.Base(p.Pages[sources.TechlandID]) }
	toMatch := func(p CatalogProduct) matching.Product {
		q := search.ParseQuery(p.Size).Quantity
		return matching.Product{NameEn: p.NameEn, NameBn: p.NameBn, Brand: p.Brand, Model: p.Model, Quantity: *q}
	}
	if len(products) < 20 {
		t.Errorf("catalog has %d products", len(products))
	}
	for _, p := range products {
		titles, ok := fixture.Titles[slugOf(p)]
		if !ok {
			t.Errorf("%s: no observed titles in the fixture", p.NameEn)
			continue
		}
		for source, title := range titles {
			if c := matching.Confidence(toMatch(p), title); c < matching.MinConfidence {
				t.Errorf("%s does not match its own %s title %q (confidence %.2f)", p.NameEn, source, title, c)
			}
		}
		for _, other := range products {
			if slugOf(other) == slugOf(p) {
				continue
			}
			for source, title := range fixture.Titles[slugOf(other)] {
				if c := matching.Confidence(toMatch(p), title); c >= matching.MinConfidence {
					t.Errorf("%s wrongly matches %s's %s title %q (confidence %.2f)", p.NameEn, other.NameEn, source, title, c)
				}
			}
		}
	}
}

func TestCatalogIsWellFormed(t *testing.T) {
	t.Parallel()
	products, err := ReferenceCatalog()
	if err != nil {
		t.Fatal(err)
	}
	cats := map[string]bool{}
	for _, c := range baseCategories {
		cats[c.Slug] = true
	}
	bases := map[string]string{}
	for _, d := range sources.Catalog {
		bases[d.ID] = d.BaseURL
	}
	seen := map[string]bool{}
	for _, p := range products {
		if p.NameEn == "" || p.NameBn == "" || p.Brand == "" || !cats[p.Category] {
			t.Errorf("incomplete product %+v", p)
		}
		if q := search.ParseQuery(p.Size).Quantity; q == nil || !domain.ValidUnit(q.Unit) {
			t.Errorf("%s: bad size %q", p.NameEn, p.Size)
		}
		if len(p.Pages) == 0 {
			t.Errorf("%s has no product pages", p.NameEn)
		}
		for id, u := range p.Pages {
			if bases[id] == "" || !strings.HasPrefix(u, bases[id]+"/") || strings.ContainsAny(u, "?#") {
				t.Errorf("%s: page %q is not a plain product page of %s", p.NameEn, u, id)
			}
			if seen[u] {
				t.Errorf("page %s is used twice", u)
			}
			seen[u] = true
		}
	}
}
