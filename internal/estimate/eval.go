package estimate

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
)

// EvalDataset is a self-contained snapshot of listings and offers used to
// backtest the estimator: each listing is hidden in turn and estimated from
// the rest.
type EvalDataset struct {
	Description string        `json:"description"`
	Products    []EvalProduct `json:"products"`
	Shops       []EvalShop    `json:"shops"`
	Listings    []EvalListing `json:"listings"`
	Offers      []EvalOffer   `json:"offers"`
}

// EvalProduct is a product in the evaluation dataset.
type EvalProduct struct {
	ID         int64       `json:"id"`
	CategoryID int64       `json:"category_id"`
	Name       string      `json:"name"`
	Unit       domain.Unit `json:"unit"`
	Quantity   int64       `json:"quantity"`
}

// EvalShop is a shop in the evaluation dataset.
type EvalShop struct {
	ID       int64   `json:"id"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Division string  `json:"division"`
}

// EvalListing is a known local price.
type EvalListing struct {
	ID          int64   `json:"id"`
	ShopID      int64   `json:"shop_id"`
	ProductID   int64   `json:"product_id"`
	PricePoisha int64   `json:"price_poisha"`
	AgeDays     float64 `json:"age_days"`
}

// EvalOffer is a known online price.
type EvalOffer struct {
	ID          int64   `json:"id"`
	ProductID   int64   `json:"product_id"`
	PricePoisha int64   `json:"price_poisha"`
	AgeDays     float64 `json:"age_days"`
}

// LoadEvalDataset reads a dataset from a JSON file.
func LoadEvalDataset(path string) (EvalDataset, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied dataset path
	if err != nil {
		return EvalDataset{}, fmt.Errorf("estimate: read dataset: %w", err)
	}
	var ds EvalDataset
	if err := json.Unmarshal(data, &ds); err != nil {
		return EvalDataset{}, fmt.Errorf("estimate: parse dataset: %w", err)
	}
	return ds, nil
}

// EvalReport is the outcome of a backtest.
type EvalReport struct {
	// Listings is how many held-out listings were attempted.
	Listings int `json:"listings"`
	// Estimated is how many got an estimate; the rest were insufficient_data.
	Estimated int `json:"estimated"`
	// MAPE is the mean absolute percentage error of the point estimate.
	MAPE float64 `json:"mape"`
	// MedianAbsErrorPoisha is the median absolute error of the point estimate.
	MedianAbsErrorPoisha int64 `json:"median_abs_error_poisha"`
	// RangeCoverage is the share of true prices inside [low, high].
	RangeCoverage float64 `json:"range_coverage"`
}

// EstimatedShare is the fraction of held-out listings that got an estimate.
func (r EvalReport) EstimatedShare() float64 {
	if r.Listings == 0 {
		return 0
	}
	return float64(r.Estimated) / float64(r.Listings)
}

// EvalThresholds are the committed pass/fail bounds for the baseline.
type EvalThresholds struct {
	MaxMAPE           float64 `json:"max_mape"`
	MinRangeCoverage  float64 `json:"min_range_coverage"`
	MinEstimatedShare float64 `json:"min_estimated_share"`
}

// LoadEvalThresholds reads thresholds from a JSON file.
func LoadEvalThresholds(path string) (EvalThresholds, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied thresholds path
	if err != nil {
		return EvalThresholds{}, fmt.Errorf("estimate: read thresholds: %w", err)
	}
	var t EvalThresholds
	if err := json.Unmarshal(data, &t); err != nil {
		return EvalThresholds{}, fmt.Errorf("estimate: parse thresholds: %w", err)
	}
	return t, nil
}

// Check returns the list of thresholds the report misses (empty = pass).
func (t EvalThresholds) Check(r EvalReport) []string {
	var failures []string
	if r.MAPE > t.MaxMAPE {
		failures = append(failures, fmt.Sprintf("MAPE %.4f exceeds the maximum %.4f", r.MAPE, t.MaxMAPE))
	}
	if r.RangeCoverage < t.MinRangeCoverage {
		failures = append(failures, fmt.Sprintf("range coverage %.4f is below the minimum %.4f", r.RangeCoverage, t.MinRangeCoverage))
	}
	if r.EstimatedShare() < t.MinEstimatedShare {
		failures = append(failures, fmt.Sprintf("estimated share %.4f is below the minimum %.4f", r.EstimatedShare(), t.MinEstimatedShare))
	}
	return failures
}

// Refiner optionally post-processes the baseline (the LLM-assisted path).
// Returning an error falls back to the baseline, as in production.
type Refiner func(ctx context.Context, in AgentInput) (Result, error)

// Backtest hides each listing in turn, gathers evidence from the remaining
// data with the production gathering logic, and estimates the hidden price.
func Backtest(ctx context.Context, ds EvalDataset, p Params, radiusM int, refine Refiner) (EvalReport, error) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // ages in the dataset are relative; any fixed instant works
	products := map[int64]EvalProduct{}
	for _, pr := range ds.Products {
		products[pr.ID] = pr
	}
	shops := map[int64]EvalShop{}
	for _, s := range ds.Shops {
		shops[s.ID] = s
	}
	var (
		report  EvalReport
		absErrs []int64
		sumAPE  float64
		covered int
	)
	for _, held := range ds.Listings {
		prod, okP := products[held.ProductID]
		shop, okS := shops[held.ShopID]
		if !okP || !okS || held.PricePoisha <= 0 {
			return EvalReport{}, fmt.Errorf("estimate: dataset listing %d is inconsistent", held.ID)
		}
		report.Listings++
		target := Target{
			ProductID: prod.ID, CategoryID: prod.CategoryID, NameEn: prod.Name, NameBn: prod.Name,
			Quantity: domain.Quantity{Value: prod.Quantity, Unit: prod.Unit},
		}
		center := geo.Point{Lat: shop.Lat, Lng: shop.Lng}
		mem := &memEvidenceStore{ds: ds, products: products, shops: shops, heldOut: held.ID, heldShop: shop, now: now}
		gathered, err := Gather(ctx, mem, target, center, radiusM, p, now)
		if err != nil {
			return EvalReport{}, err
		}
		if ok, _ := Sufficient(gathered.Evidence, p); !ok {
			continue
		}
		res, err := Baseline(gathered.Evidence, p)
		if err != nil {
			continue
		}
		if refine != nil {
			if refined, err := refine(ctx, AgentInput{Target: target, RadiusM: radiusM, Gathered: gathered, Baseline: res, Params: p}); err == nil {
				res = refined
			}
		}
		report.Estimated++
		diff := int64(res.Point) - held.PricePoisha
		if diff < 0 {
			diff = -diff
		}
		absErrs = append(absErrs, diff)
		sumAPE += float64(diff) / float64(held.PricePoisha)
		if held.PricePoisha >= int64(res.Low) && held.PricePoisha <= int64(res.High) {
			covered++
		}
	}
	if report.Estimated > 0 {
		sort.Slice(absErrs, func(i, j int) bool { return absErrs[i] < absErrs[j] })
		report.MAPE = sumAPE / float64(report.Estimated)
		report.MedianAbsErrorPoisha = absErrs[len(absErrs)/2]
		report.RangeCoverage = float64(covered) / float64(report.Estimated)
	}
	return report, nil
}

// memEvidenceStore serves an EvalDataset through the EvidenceStore
// interface, hiding one listing.
type memEvidenceStore struct {
	ds       EvalDataset
	products map[int64]EvalProduct
	shops    map[int64]EvalShop
	heldOut  int64
	heldShop EvalShop
	now      time.Time
}

func (m *memEvidenceStore) at(ageDays float64) time.Time {
	return m.now.Add(-time.Duration(ageDays * 24 * float64(time.Hour)))
}

// listings returns the visible listings matching keep, nearest first.
func (m *memEvidenceStore) listings(center geo.Point, c Cutoffs, keep func(l EvalListing, distM float64) bool) []ListingRow {
	var out []ListingRow
	for _, l := range m.ds.Listings {
		if l.ID == m.heldOut {
			continue
		}
		shop := m.shops[l.ShopID]
		observed := m.at(l.AgeDays)
		if observed.Before(c.ListingsSince) {
			continue
		}
		dist := geo.DistanceMeters(center, geo.Point{Lat: shop.Lat, Lng: shop.Lng})
		if !keep(l, dist) {
			continue
		}
		out = append(out, ListingRow{ID: l.ID, ShopID: l.ShopID, Price: l.PricePoisha, ObservedAt: observed, DistanceM: dist})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DistanceM != out[j].DistanceM {
			return out[i].DistanceM < out[j].DistanceM
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func limitRows[T any](rows []T, limit int) []T {
	if len(rows) > limit {
		return rows[:limit]
	}
	return rows
}

func (m *memEvidenceStore) ListingsInRing(_ context.Context, productID int64, center geo.Point, minM, maxM float64, c Cutoffs, limit int) ([]ListingRow, error) {
	return limitRows(m.listings(center, c, func(l EvalListing, d float64) bool {
		return l.ProductID == productID && d <= maxM && (minM == 0 || d > minM)
	}), limit), nil
}

func (m *memEvidenceStore) OtherAreaListings(_ context.Context, productID int64, center geo.Point, beyondM float64, c Cutoffs, limit int) ([]ListingRow, error) {
	return limitRows(m.listings(center, c, func(l EvalListing, d float64) bool {
		return l.ProductID == productID && d > beyondM && m.shops[l.ShopID].Division == m.heldShop.Division
	}), limit), nil
}

func (m *memEvidenceStore) Offers(_ context.Context, productID int64, c Cutoffs, limit int) ([]OfferRow, error) {
	var out []OfferRow
	for _, o := range m.ds.Offers {
		if observed := m.at(o.AgeDays); o.ProductID == productID && !observed.Before(c.OffersSince) {
			out = append(out, OfferRow{ID: o.ID, Price: o.PricePoisha, ObservedAt: observed})
		}
	}
	return limitRows(out, limit), nil
}

func (m *memEvidenceStore) Comparables(_ context.Context, t Target, center geo.Point, radiusM float64, c Cutoffs, limit int) ([]ComparableRow, error) {
	rows := m.listings(center, c, func(l EvalListing, d float64) bool {
		p := m.products[l.ProductID]
		return l.ProductID != t.ProductID && p.CategoryID == t.CategoryID && p.Unit == t.Quantity.Unit &&
			p.Quantity >= max(t.Quantity.Value/2, 1) && p.Quantity <= t.Quantity.Value*2 && d <= radiusM
	})
	out := make([]ComparableRow, 0, len(rows))
	for _, r := range limitRows(rows, limit) {
		var productID int64
		for _, l := range m.ds.Listings {
			if l.ID == r.ID {
				productID = l.ProductID
				break
			}
		}
		out = append(out, ComparableRow{ListingRow: r, Quantity: m.products[productID].Quantity})
	}
	return out, nil
}

func (m *memEvidenceStore) History(context.Context, int64, geo.Point, float64, Cutoffs, int) ([]ListingRow, error) {
	return nil, nil // the evaluation dataset carries no price history
}

func (m *memEvidenceStore) RatioPairs(_ context.Context, excludeProductID int64, center geo.Point, radiusM float64, c Cutoffs) ([]RatioPair, error) {
	local := map[int64][]float64{}
	near := map[int64]bool{}
	for _, l := range m.ds.Listings {
		if l.ID == m.heldOut || l.ProductID == excludeProductID || m.at(l.AgeDays).Before(c.ListingsSince) {
			continue
		}
		local[l.ProductID] = append(local[l.ProductID], float64(l.PricePoisha))
		shop := m.shops[l.ShopID]
		if geo.DistanceMeters(center, geo.Point{Lat: shop.Lat, Lng: shop.Lng}) <= radiusM {
			near[l.ProductID] = true
		}
	}
	online := map[int64][]float64{}
	for _, o := range m.ds.Offers {
		if o.ProductID != excludeProductID && !m.at(o.AgeDays).Before(c.OffersSince) {
			online[o.ProductID] = append(online[o.ProductID], float64(o.PricePoisha))
		}
	}
	var out []RatioPair
	for id, prices := range local {
		if len(online[id]) == 0 {
			continue
		}
		out = append(out, RatioPair{
			ProductID: id, CategoryID: m.products[id].CategoryID,
			MedianLocal: median(prices), MedianOnline: median(online[id]), Near: near[id],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProductID < out[j].ProductID })
	return out, nil
}

// RoundPct formats a ratio as a percentage with one decimal, for reports.
func RoundPct(v float64) float64 { return math.Round(v*1000) / 10 }
