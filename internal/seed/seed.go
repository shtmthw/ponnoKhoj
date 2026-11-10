// Package seed loads reference data (areas, categories) and, on request,
// clearly marked demo data (catalog, vendors, shops, listings).
package seed

import (
	"context"
	_ "embed" // areas.json
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// areasJSON is derived from https://github.com/nuhil/bangladesh-geocode
// (MIT licence, see data/LICENSE.bangladesh-geocode and docs/data-sources.md).
// Only districts carry coordinates in that dataset.
//
//go:embed data/areas.json
var areasJSON []byte

type areaFile struct {
	Source    string `json:"source"`
	Divisions []struct {
		ID     string `json:"id"`
		NameEn string `json:"name_en"`
		NameBn string `json:"name_bn"`
	} `json:"divisions"`
	Districts []struct {
		ID         string `json:"id"`
		DivisionID string `json:"division_id"`
		NameEn     string `json:"name_en"`
		NameBn     string `json:"name_bn"`
		Lat        string `json:"lat"`
		Lon        string `json:"lon"`
	} `json:"districts"`
	Upazilas []struct {
		ID         string `json:"id"`
		DistrictID string `json:"district_id"`
		NameEn     string `json:"name_en"`
		NameBn     string `json:"name_bn"`
	} `json:"upazilas"`
}

// Areas loads Bangladesh's divisions, districts and upazilas. It is
// idempotent and safe in every environment.
func Areas(ctx context.Context, st *store.Store) (int, error) {
	var f areaFile
	if err := json.Unmarshal(areasJSON, &f); err != nil {
		return 0, fmt.Errorf("seed: parse areas: %w", err)
	}
	n := 0
	err := st.InTx(ctx, func(q *db.Queries) error {
		divisions := map[string]int64{}
		for _, d := range f.Divisions {
			id, err := q.UpsertArea(ctx, db.UpsertAreaParams{
				Kind: "division", NameEn: d.NameEn, NameBn: d.NameBn, Metadata: json.RawMessage(`{}`),
				Source: f.Source, ExternalID: d.ID,
			})
			if err != nil {
				return fmt.Errorf("seed: division %s: %w", d.NameEn, err)
			}
			divisions[d.ID] = id
			n++
		}
		districts := map[string]int64{}
		for _, d := range f.Districts {
			parent, ok := divisions[d.DivisionID]
			if !ok {
				return fmt.Errorf("seed: district %s has unknown division %s", d.NameEn, d.DivisionID)
			}
			p := db.UpsertAreaParams{
				Kind: "district", ParentID: &parent, NameEn: d.NameEn, NameBn: d.NameBn,
				Metadata: json.RawMessage(`{}`), Source: f.Source, ExternalID: d.ID,
			}
			// Coordinates come from the dataset; a district without usable
			// ones simply gets no centroid.
			if lat, errLat := strconv.ParseFloat(d.Lat, 64); errLat == nil {
				if lng, errLng := strconv.ParseFloat(d.Lon, 64); errLng == nil && lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180 {
					p.Lat, p.Lng = &lat, &lng
				}
			}
			id, err := q.UpsertArea(ctx, p)
			if err != nil {
				return fmt.Errorf("seed: district %s: %w", d.NameEn, err)
			}
			districts[d.ID] = id
			n++
		}
		for _, u := range f.Upazilas {
			parent, ok := districts[u.DistrictID]
			if !ok {
				return fmt.Errorf("seed: upazila %s has unknown district %s", u.NameEn, u.DistrictID)
			}
			if _, err := q.UpsertArea(ctx, db.UpsertAreaParams{
				Kind: "upazila", ParentID: &parent, NameEn: u.NameEn, NameBn: u.NameBn,
				Metadata: json.RawMessage(`{}`), Source: f.Source, ExternalID: u.ID,
			}); err != nil {
				return fmt.Errorf("seed: upazila %s: %w", u.NameEn, err)
			}
			n++
		}
		return nil
	})
	return n, err
}

type category struct {
	Slug, Parent, NameEn, NameBn string
}

// baseCategories is the starting category tree. It is reference data, not
// demo data.
var baseCategories = []category{
	{"groceries", "", "Groceries", "মুদি পণ্য"},
	{"rice-grains", "groceries", "Rice & Grains", "চাল ও শস্য"},
	{"lentils-pulses", "groceries", "Lentils & Pulses", "ডাল"},
	{"cooking-oil", "groceries", "Cooking Oil", "ভোজ্য তেল"},
	{"spices-salt-sugar", "groceries", "Spices, Salt & Sugar", "মসলা, লবণ ও চিনি"},
	{"eggs-dairy", "groceries", "Eggs & Dairy", "ডিম ও দুগ্ধজাত"},
	{"vegetables", "groceries", "Vegetables", "সবজি"},
	{"beverages", "groceries", "Beverages", "পানীয়"},
	{"personal-care", "", "Personal Care", "ব্যক্তিগত যত্ন"},
	{"home-care", "", "Home & Cleaning", "ঘর ও পরিচ্ছন্নতা"},
	{"electronics", "", "Electronics & Appliances", "ইলেকট্রনিক্স ও অ্যাপ্লায়েন্স"},
	{"networking", "electronics", "Networking", "নেটওয়ার্কিং"},
	{"computer-accessories", "electronics", "Computer Accessories", "কম্পিউটার এক্সেসরিজ"},
	{"storage-memory", "electronics", "Storage & Memory", "স্টোরেজ ও মেমোরি"},
	{"audio", "electronics", "Audio", "অডিও"},
	{"mobile-accessories", "electronics", "Mobile Accessories", "মোবাইল এক্সেসরিজ"},
}

// Categories loads the base category tree. Idempotent.
func Categories(ctx context.Context, st *store.Store) (map[string]int64, error) {
	ids := map[string]int64{}
	err := st.InTx(ctx, func(q *db.Queries) error {
		for _, c := range baseCategories {
			p := db.UpsertCategoryParams{Slug: c.Slug, NameEn: c.NameEn, NameBn: c.NameBn}
			if c.Parent != "" {
				parent, ok := ids[c.Parent]
				if !ok {
					return fmt.Errorf("seed: category %s listed before its parent %s", c.Slug, c.Parent)
				}
				p.ParentID = &parent
			}
			id, err := q.UpsertCategory(ctx, p)
			if err != nil {
				return fmt.Errorf("seed: category %s: %w", c.Slug, err)
			}
			ids[c.Slug] = id
		}
		return nil
	})
	return ids, err
}
