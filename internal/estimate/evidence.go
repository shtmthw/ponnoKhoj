// Package estimate produces price estimates for products that no nearby
// vendor lists. The guiding rule: an estimate is derived only from evidence
// the system retrieved. No evidence, no estimate.
//
// The pipeline is: gather evidence (deterministic) -> sufficiency check ->
// statistical baseline (always) -> optional LLM refinement over read-only
// tools -> server-side validation, falling back to the baseline.
package estimate

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
)

// EvidenceType says where an evidence item came from.
type EvidenceType string

// Evidence types, in decreasing order of trust.
const (
	EvLocal      EvidenceType = "local_listing"      // same product, within the expanding search rings
	EvOtherArea  EvidenceType = "other_area_listing" // same product, further away in the same division
	EvHistory    EvidenceType = "price_history"      // superseded price of the same product nearby
	EvOnline     EvidenceType = "online_offer"       // same product, online
	EvComparable EvidenceType = "comparable_listing" // similar product in the same category, rescaled
)

// Evidence is one retrieved price observation. IDs are stable across runs:
// a type letter plus the database ID of the row it came from.
type Evidence struct {
	ID   string       `json:"id"`
	Type EvidenceType `json:"type"`
	// PricePoisha is the price as observed.
	PricePoisha int64 `json:"price_poisha"`
	// AdjustedPoisha is the price made comparable to the target: comparables
	// are rescaled to the target size and online offers are multiplied by
	// the learned online-to-offline ratio.
	AdjustedPoisha int64 `json:"adjusted_poisha"`
	// DistanceM is rounded to 100 m; nil for online offers.
	DistanceM *float64 `json:"distance_m,omitempty"`
	AgeDays   float64  `json:"age_days"`
	Weight    float64  `json:"weight"`
	// origin identifies the independent source (a shop, an online offer) so
	// that several observations from one shop count once for sufficiency.
	origin string
}

// Target is the product being estimated.
type Target struct {
	ProductID  int64
	CategoryID int64
	NameEn     string
	NameBn     string
	Brand      string
	CategoryEn string
	Quantity   domain.Quantity
}

// Rows returned by an EvidenceStore.
type (
	// ListingRow is a current or historical listing price near a point.
	ListingRow struct {
		ID         int64
		ShopID     int64
		Price      int64
		ObservedAt time.Time
		DistanceM  float64
	}
	// OfferRow is an online offer.
	OfferRow struct {
		ID         int64
		Price      int64
		ObservedAt time.Time
	}
	// ComparableRow is a listing of a similar product, with that product's size.
	ComparableRow struct {
		ListingRow
		Quantity int64
	}
	// RatioPair is one product's median local and online price.
	RatioPair struct {
		ProductID    int64
		CategoryID   int64
		MedianLocal  float64
		MedianOnline float64
		Near         bool
	}
)

// Cutoffs bound how old evidence may be.
type Cutoffs struct {
	ListingsSince time.Time
	OffersSince   time.Time
	HistorySince  time.Time
}

// EvidenceStore is the read-only data access the gatherer needs. The
// Postgres implementation is in service.go; the evaluation harness has an
// in-memory one, so the backtest exercises the same gathering logic.
type EvidenceStore interface {
	ListingsInRing(ctx context.Context, productID int64, center geo.Point, minM, maxM float64, c Cutoffs, limit int) ([]ListingRow, error)
	OtherAreaListings(ctx context.Context, productID int64, center geo.Point, beyondM float64, c Cutoffs, limit int) ([]ListingRow, error)
	Offers(ctx context.Context, productID int64, c Cutoffs, limit int) ([]OfferRow, error)
	Comparables(ctx context.Context, t Target, center geo.Point, radiusM float64, c Cutoffs, limit int) ([]ComparableRow, error)
	History(ctx context.Context, productID int64, center geo.Point, radiusM float64, c Cutoffs, limit int) ([]ListingRow, error)
	RatioPairs(ctx context.Context, excludeProductID int64, center geo.Point, radiusM float64, c Cutoffs) ([]RatioPair, error)
}

// Params tunes the estimator. Defaults come from DefaultParams.
type Params struct {
	// MinEvidence is the number of independent evidence items below which no
	// estimate is produced.
	MinEvidence int
	// FreshFor is the age up to which evidence counts as fresh. Evidence
	// that is all older than this is insufficient.
	FreshFor time.Duration
	// ListingMaxAge, OfferMaxAge and HistoryMaxAge bound evidence age.
	ListingMaxAge time.Duration
	OfferMaxAge   time.Duration
	HistoryMaxAge time.Duration
	// DistanceScaleM is the e-folding distance of the distance decay.
	DistanceScaleM float64
	// RecencyHalfLifeDays is the half-life of the recency decay.
	RecencyHalfLifeDays float64
	// EnvelopeLow and EnvelopeHigh bound any estimate to
	// [EnvelopeLow x cheapest evidence, EnvelopeHigh x dearest evidence].
	EnvelopeLow  float64
	EnvelopeHigh float64
	// MaxRingM is the widest ring searched for the same product.
	MaxRingM float64
}

// DefaultParams returns the documented defaults (docs/estimation.md).
func DefaultParams() Params {
	return Params{
		MinEvidence:         3,
		FreshFor:            7 * 24 * time.Hour,
		ListingMaxAge:       30 * 24 * time.Hour,
		OfferMaxAge:         7 * 24 * time.Hour,
		HistoryMaxAge:       90 * 24 * time.Hour,
		DistanceScaleM:      10_000,
		RecencyHalfLifeDays: 14,
		EnvelopeLow:         0.5,
		EnvelopeHigh:        1.5,
		MaxRingM:            25_000,
	}
}

// baseWeights are the per-type trust weights before distance and recency
// decay. Online offers weigh less when no online-to-offline ratio could be
// learned (see Gather).
var baseWeights = map[EvidenceType]float64{
	EvLocal:      1.0,
	EvOtherArea:  0.5,
	EvHistory:    0.5,
	EvOnline:     0.6,
	EvComparable: 0.3,
}

const unadjustedOnlineWeight = 0.35

// Ratio is the learned online-to-offline price adjustment.
type Ratio struct {
	Value float64 `json:"value"`
	// Scope is "category_local", "global" or "none".
	Scope string `json:"scope"`
	Pairs int    `json:"pairs"`
}

// minRatioPairs is how many products with both local and online prices are
// needed before a ratio is trusted.
const minRatioPairs = 3

// LearnRatio derives the online-to-offline ratio: the median of
// local/online medians over products in the target's category near the
// point; failing that over all products; failing that no adjustment.
// The ratio is clamped to [0.5, 2] so one odd pair cannot distort prices.
func LearnRatio(pairs []RatioPair, categoryID int64) Ratio {
	var local, global []float64
	for _, p := range pairs {
		if p.MedianLocal <= 0 || p.MedianOnline <= 0 {
			continue
		}
		r := p.MedianLocal / p.MedianOnline
		global = append(global, r)
		if p.CategoryID == categoryID && p.Near {
			local = append(local, r)
		}
	}
	switch {
	case len(local) >= minRatioPairs:
		return Ratio{Value: clamp(median(local), 0.5, 2), Scope: "category_local", Pairs: len(local)}
	case len(global) >= minRatioPairs:
		return Ratio{Value: clamp(median(global), 0.5, 2), Scope: "global", Pairs: len(global)}
	default:
		return Ratio{Value: 1, Scope: "none", Pairs: len(global)}
	}
}

func median(xs []float64) float64 {
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func clamp(v, lo, hi float64) float64 { return math.Min(math.Max(v, lo), hi) }

// Gathered is the outcome of evidence gathering.
type Gathered struct {
	Evidence []Evidence
	Ratio    Ratio
	// RingM is the widest same-product ring that was searched.
	RingM float64
}

// Gather retrieves evidence for a target around center. It is deterministic
// for a given store state and time.
func Gather(ctx context.Context, st EvidenceStore, t Target, center geo.Point, radiusM int, p Params, now time.Time) (Gathered, error) {
	c := Cutoffs{
		ListingsSince: now.Add(-p.ListingMaxAge),
		OffersSince:   now.Add(-p.OfferMaxAge),
		HistorySince:  now.Add(-p.HistoryMaxAge),
	}
	var out Gathered

	// (a) Same product in expanding rings: the requested radius, then 10 km,
	// then 25 km, stopping once there is comfortably enough.
	rings := []float64{float64(radiusM)}
	for _, r := range []float64{10_000, p.MaxRingM} {
		if r > rings[len(rings)-1] && r <= p.MaxRingM {
			rings = append(rings, r)
		}
	}
	prev := 0.0
	shops := map[int64]bool{}
	for _, ring := range rings {
		rows, err := st.ListingsInRing(ctx, t.ProductID, center, prev, ring, c, 100)
		if err != nil {
			return Gathered{}, fmt.Errorf("estimate: listings in ring: %w", err)
		}
		for _, r := range rows {
			shops[r.ShopID] = true
			out.Evidence = append(out.Evidence, listingEvidence("L", EvLocal, r, r.Price, now))
		}
		prev, out.RingM = ring, ring
		if len(shops) >= 2*p.MinEvidence {
			break
		}
	}

	// (b) Same product further away in the same division.
	other, err := st.OtherAreaListings(ctx, t.ProductID, center, out.RingM, c, 20)
	if err != nil {
		return Gathered{}, fmt.Errorf("estimate: other-area listings: %w", err)
	}
	for _, r := range other {
		out.Evidence = append(out.Evidence, listingEvidence("A", EvOtherArea, r, r.Price, now))
	}

	// (c) Online offers, adjusted by the learned online-to-offline ratio.
	pairs, err := st.RatioPairs(ctx, t.ProductID, center, p.MaxRingM, c)
	if err != nil {
		return Gathered{}, fmt.Errorf("estimate: ratio pairs: %w", err)
	}
	out.Ratio = LearnRatio(pairs, t.CategoryID)
	offers, err := st.Offers(ctx, t.ProductID, c, 50)
	if err != nil {
		return Gathered{}, fmt.Errorf("estimate: offers: %w", err)
	}
	for _, o := range offers {
		e := Evidence{
			ID: "O" + strconv.FormatInt(o.ID, 10), Type: EvOnline,
			PricePoisha: o.Price, AdjustedPoisha: scale(o.Price, out.Ratio.Value),
			AgeDays: ageDays(o.ObservedAt, now), origin: "offer:" + strconv.FormatInt(o.ID, 10),
		}
		out.Evidence = append(out.Evidence, e)
	}

	// (d) Comparable products nearby, rescaled to the target size.
	wide := math.Max(float64(radiusM), 10_000)
	comps, err := st.Comparables(ctx, t, center, wide, c, 50)
	if err != nil {
		return Gathered{}, fmt.Errorf("estimate: comparables: %w", err)
	}
	for _, r := range comps {
		if r.Quantity <= 0 {
			continue
		}
		adjusted := scale(r.Price, float64(t.Quantity.Value)/float64(r.Quantity))
		out.Evidence = append(out.Evidence, listingEvidence("C", EvComparable, r.ListingRow, adjusted, now))
	}

	// (e) Historical prices of the product nearby.
	hist, err := st.History(ctx, t.ProductID, center, wide, c, 50)
	if err != nil {
		return Gathered{}, fmt.Errorf("estimate: history: %w", err)
	}
	for _, r := range hist {
		out.Evidence = append(out.Evidence, listingEvidence("H", EvHistory, r, r.Price, now))
	}

	// Drop anything that did not survive adjustment, then weigh.
	kept := out.Evidence[:0]
	for _, e := range out.Evidence {
		if e.AdjustedPoisha <= 0 || e.AdjustedPoisha > int64(domain.MaxMoney) {
			continue
		}
		e.Weight = Weight(e, out.Ratio, p)
		kept = append(kept, e)
	}
	out.Evidence = capComparables(kept)
	sort.SliceStable(out.Evidence, func(i, j int) bool { return out.Evidence[i].ID < out.Evidence[j].ID })
	return out, nil
}

// MaxComparableShare bounds how much comparable products may weigh relative
// to evidence about the product itself. A different product is a hint about
// the price level, never the main argument: without this cap twenty listings
// of a cheaper rice would outvote three listings of the rice being estimated.
const MaxComparableShare = 0.25

// capComparables scales comparable weights down so that together they weigh
// at most MaxComparableShare of the same-product evidence.
func capComparables(ev []Evidence) []Evidence {
	var same, comp float64
	for _, e := range ev {
		if e.Type == EvComparable {
			comp += e.Weight
		} else {
			same += e.Weight
		}
	}
	if comp == 0 || comp <= MaxComparableShare*same {
		return ev
	}
	factor := MaxComparableShare * same / comp
	for i := range ev {
		if ev[i].Type == EvComparable {
			ev[i].Weight *= factor
		}
	}
	return ev
}

// SameProduct returns the evidence about the target product itself (every
// type except comparables).
func SameProduct(ev []Evidence) []Evidence {
	out := make([]Evidence, 0, len(ev))
	for _, e := range ev {
		if e.Type != EvComparable {
			out = append(out, e)
		}
	}
	return out
}

func listingEvidence(prefix string, typ EvidenceType, r ListingRow, adjusted int64, now time.Time) Evidence {
	d := math.Round(r.DistanceM/100) * 100
	return Evidence{
		ID: prefix + strconv.FormatInt(r.ID, 10), Type: typ,
		PricePoisha: r.Price, AdjustedPoisha: adjusted, DistanceM: &d,
		AgeDays: ageDays(r.ObservedAt, now), origin: "shop:" + strconv.FormatInt(r.ShopID, 10),
	}
}

func ageDays(t, now time.Time) float64 {
	d := now.Sub(t).Hours() / 24
	if d < 0 {
		return 0
	}
	return math.Round(d*10) / 10
}

// scale multiplies a price by a dimensionless factor, rounding to poisha.
// Prices are integers; only the factor is a float.
func scale(price int64, factor float64) int64 {
	v := math.Round(float64(price) * factor)
	if math.IsNaN(v) || v < 0 || v > float64(domain.MaxMoney) {
		return 0
	}
	return int64(v)
}

// Weight is the evidence weight: type trust x distance decay x recency decay.
func Weight(e Evidence, ratio Ratio, p Params) float64 {
	w := baseWeights[e.Type]
	if e.Type == EvOnline && ratio.Scope == "none" {
		w = unadjustedOnlineWeight
	}
	if e.DistanceM != nil && p.DistanceScaleM > 0 {
		w *= math.Exp(-*e.DistanceM / p.DistanceScaleM)
	}
	if p.RecencyHalfLifeDays > 0 {
		w *= math.Pow(0.5, e.AgeDays/p.RecencyHalfLifeDays)
	}
	return w
}

// Independent counts distinct evidence origins: several observations from
// one shop (a listing and its history) are one independent source.
func Independent(ev []Evidence) int {
	seen := map[string]bool{}
	for _, e := range ev {
		seen[e.origin] = true
	}
	return len(seen)
}

// Sufficiency reasons.
const (
	ReasonTooFew   = "fewer independent price points for this product than the minimum"
	ReasonAllStale = "all evidence is older than the freshness threshold"
)

// Sufficient reports whether there is enough evidence to estimate, and if
// not, why. Only evidence about the product itself counts: comparable
// products refine an estimate but can never justify one on their own.
func Sufficient(ev []Evidence, p Params) (bool, string) {
	same := SameProduct(ev)
	if Independent(same) < p.MinEvidence {
		return false, ReasonTooFew
	}
	freshDays := p.FreshFor.Hours() / 24
	for _, e := range same {
		if e.AgeDays <= freshDays {
			return true, ""
		}
	}
	return false, ReasonAllStale
}
