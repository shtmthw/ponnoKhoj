package seed

import (
	"context"
	_ "embed" // catalog.json
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/sources"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// catalogJSON is the real-product catalog with each product's page at the
// approved online sources. It is reference data, loaded in every
// environment; prices are never stored in it, only read live.
//
//go:embed data/catalog.json
var catalogJSON []byte

// CatalogProduct is one real product in the reference catalog.
type CatalogProduct struct {
	NameEn   string            `json:"name_en"`
	NameBn   string            `json:"name_bn"`
	Brand    string            `json:"brand"`
	Model    string            `json:"model"`
	Category string            `json:"category"`
	Size     string            `json:"size"`
	Aliases  []string          `json:"aliases"`
	Pages    map[string]string `json:"pages"` // source ID -> product page URL
}

// ReferenceCatalog returns the embedded real-product catalog.
func ReferenceCatalog() ([]CatalogProduct, error) {
	var f struct {
		Products []CatalogProduct `json:"products"`
	}
	if err := json.Unmarshal(catalogJSON, &f); err != nil {
		return nil, fmt.Errorf("seed: parse catalog: %w", err)
	}
	return f.Products, nil
}

// Catalog loads the real-product catalog and its online product pages.
// Idempotent. It returns how many products it loaded.
func Catalog(ctx context.Context, st *store.Store, cats map[string]int64) (int, error) {
	products, err := ReferenceCatalog()
	if err != nil {
		return 0, err
	}
	known := map[string]sources.Descriptor{}
	for _, d := range sources.Catalog {
		known[d.ID] = d
	}
	// Product pages reference their source; make sure the rows exist
	// without touching the enabled flags (the web and worker own those).
	for id := range pagesSources(products) {
		d, ok := known[id]
		if !ok {
			return 0, fmt.Errorf("seed: catalog names unknown source %q", id)
		}
		if err := st.EnsureSource(ctx, db.EnsureSourceParams{
			ID: d.ID, Name: d.Name, BaseUrl: d.BaseURL, AccessMethod: d.AccessMethod, ComplianceStatus: d.Compliance,
		}); err != nil {
			return 0, fmt.Errorf("seed: source %s: %w", id, err)
		}
	}
	n := 0
	err = st.InTx(ctx, func(q *db.Queries) error {
		for _, p := range products {
			qty := search.ParseQuery(p.Size).Quantity
			catID, ok := cats[p.Category]
			if qty == nil || !ok {
				return fmt.Errorf("seed: catalog product %q: bad size %q or unknown category %q", p.NameEn, p.Size, p.Category)
			}
			id, err := q.FindProductByIdentity(ctx, db.FindProductByIdentityParams{
				NameEn: p.NameEn, Brand: p.Brand, Model: p.Model, Unit: string(qty.Unit), Quantity: qty.Value,
			})
			if store.IsNotFound(err) {
				id, err = q.InsertProduct(ctx, db.InsertProductParams{
					CategoryID: catID, NameEn: p.NameEn, NameBn: p.NameBn, Brand: p.Brand, Model: p.Model,
					Unit: string(qty.Unit), Quantity: qty.Value,
					SearchKey: search.ProductKey(p.NameEn, p.NameBn, p.Brand, p.Model, qty), Status: "active",
				})
			}
			if err != nil {
				return fmt.Errorf("seed: catalog product %q: %w", p.NameEn, err)
			}
			for _, alias := range p.Aliases {
				if key := search.AliasKey(alias); key != "" {
					if err := q.InsertProductAlias(ctx, db.InsertProductAliasParams{ProductID: id, Alias: alias, AliasKey: key}); err != nil {
						return fmt.Errorf("seed: alias %q: %w", alias, err)
					}
				}
			}
			for sourceID, url := range p.Pages {
				if !strings.HasPrefix(url, known[sourceID].BaseURL+"/") {
					return fmt.Errorf("seed: %q page %s is not on %s", p.NameEn, url, sourceID)
				}
				if err := q.UpsertProductSourceURL(ctx, db.UpsertProductSourceURLParams{ProductID: id, SourceID: sourceID, Url: url}); err != nil {
					return fmt.Errorf("seed: product page %s: %w", url, err)
				}
			}
			n++
		}
		return nil
	})
	return n, err
}

func pagesSources(products []CatalogProduct) map[string]bool {
	out := map[string]bool{}
	for _, p := range products {
		for id := range p.Pages {
			out[id] = true
		}
	}
	return out
}
