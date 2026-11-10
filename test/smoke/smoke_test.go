//go:build smoke

// Package smoke reads one live product page from each approved online
// source, through the same polite fetch client production uses (robots.txt,
// rate limit, identifying User-Agent). Manual only: `make sources-smoke`.
// It never runs in CI, which must not touch the live network.
package smoke

import (
	"context"
	"testing"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/matching"
	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/seed"
	"github.com/shtmthw/ponnoKhoj/internal/sources"
)

func TestApprovedSourcesLive(t *testing.T) {
	catalog, err := seed.ReferenceCatalog()
	if err != nil {
		t.Fatal(err)
	}
	approved := map[string]bool{}
	var ids []string
	for _, d := range sources.Catalog {
		if d.Compliance == "approved" {
			approved[d.ID] = true
			ids = append(ids, d.ID)
		}
	}
	reg, err := sources.NewRegistry(sources.RegistryConfig{Enabled: func(id string) bool { return approved[id] }, ContactURL: "https://github.com/shtmthw/ponnoKhoj"}, ids)
	if err != nil {
		t.Fatal(err)
	}
	p := catalog[0]
	qty := search.ParseQuery(p.Size).Quantity
	mp := matching.Product{NameEn: p.NameEn, NameBn: p.NameBn, Brand: p.Brand, Model: p.Model, Quantity: domain.Quantity{Value: qty.Value, Unit: qty.Unit}}
	for _, src := range reg.Enabled() {
		reader, ok := src.(sources.PageReader)
		if !ok {
			continue
		}
		page := p.Pages[src.ID()]
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		res, err := reader.ReadPage(ctx, page)
		cancel()
		if err != nil {
			t.Errorf("%s: %s: %v", src.ID(), page, err)
			continue
		}
		if len(res.Offers) == 0 {
			t.Errorf("%s: no offer on %s (skipped %d): the page structure may have changed", src.ID(), page, res.Skipped)
			continue
		}
		for _, o := range res.Offers {
			t.Logf("%s: %q %s in-stock=%v match=%.2f", src.ID(), o.Title, o.Price.Format(false), o.InStock, matching.Confidence(mp, o.Title))
		}
	}
}
