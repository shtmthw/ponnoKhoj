package estimate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/matching"
	"github.com/shtmthw/ponnoKhoj/internal/observability"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// pendingTimeout is how long a pending estimate blocks a new request. If the
// job never reports back (worker down), the request can be started again.
const pendingTimeout = 2 * time.Minute

// StoredEvidence is the evidence summary saved with an estimate and shown to
// the shopper.
type StoredEvidence struct {
	Items  []Evidence     `json:"items"`
	Counts map[string]int `json:"counts"`
	Ratio  Ratio          `json:"online_to_offline_ratio"`
	RingM  float64        `json:"widest_ring_m"`
	// Cited lists the evidence IDs the LLM cited (LLM-assisted only).
	Cited []string `json:"cited,omitempty"`
	// Reason says why there is no estimate (insufficient_data only).
	Reason string `json:"reason,omitempty"`
}

// View is an estimate as the UI and API see it.
type View struct {
	Status         domain.EstimateStatus
	Low            domain.Money
	Point          domain.Money
	High           domain.Money
	Confidence     domain.Confidence
	Method         domain.EstimateMethod
	ModelID        string
	FallbackReason string
	ExplanationEn  string
	ExplanationBn  string
	Evidence       StoredEvidence
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// Service runs and serves estimates.
type Service struct {
	store   *store.Store
	params  Params
	ttl     time.Duration
	agent   *Agent // nil when LLM refinement is disabled
	metrics *observability.Metrics
	log     *slog.Logger
	now     func() time.Time
}

// NewService builds the estimate service. agent may be nil.
func NewService(st *store.Store, params Params, ttl time.Duration, agent *Agent, metrics *observability.Metrics, log *slog.Logger) *Service {
	return &Service{store: st, params: params, ttl: ttl, agent: agent, metrics: metrics, log: log, now: time.Now}
}

// Lookup returns the cached estimate for (product, cell, radius bucket), if
// one exists and is still pending or unexpired.
func (s *Service) Lookup(ctx context.Context, productID int64, cell string, radiusM int) (View, bool, error) {
	row, err := s.store.GetEstimate(ctx, db.GetEstimateParams{ProductID: productID, Cell: cell, RadiusM: int32(radiusM)}) //nolint:gosec // radius is bucketed to <= 25000
	if store.IsNotFound(err) {
		return View{}, false, nil
	}
	if err != nil {
		return View{}, false, fmt.Errorf("estimate: lookup: %w", err)
	}
	if !row.ExpiresAt.After(s.now()) {
		return View{}, false, nil
	}
	v := View{
		Status: domain.EstimateStatus(row.Status), ModelID: row.ModelID, FallbackReason: row.FallbackReason,
		ExplanationEn: row.ExplanationEn, ExplanationBn: row.ExplanationBn,
		CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
	}
	if row.LowPoisha != nil && row.PointPoisha != nil && row.HighPoisha != nil {
		v.Low, v.Point, v.High = domain.Money(*row.LowPoisha), domain.Money(*row.PointPoisha), domain.Money(*row.HighPoisha)
	}
	if row.Confidence != nil {
		v.Confidence = domain.Confidence(*row.Confidence)
	}
	if row.Method != nil {
		v.Method = domain.EstimateMethod(*row.Method)
	}
	if err := json.Unmarshal(row.Evidence, &v.Evidence); err != nil {
		// Rows written as '[]' (pending) are not an object; that is fine.
		v.Evidence = StoredEvidence{}
	}
	return v, true, nil
}

// Begin marks an estimate as pending. It returns true when the caller should
// enqueue the job, false when a live estimate (pending or ready) exists.
func (s *Service) Begin(ctx context.Context, productID int64, cell string, radiusM int) (bool, error) {
	_, err := s.store.BeginEstimate(ctx, db.BeginEstimateParams{
		ProductID: productID, Cell: cell, RadiusM: int32(radiusM), ExpiresAt: s.now().Add(pendingTimeout), //nolint:gosec // radius is bucketed to <= 25000
	})
	if store.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("estimate: begin: %w", err)
	}
	return true, nil
}

// Run computes and stores the estimate for (product, cell, radius). It is
// what the River job executes.
func (s *Service) Run(ctx context.Context, productID int64, cell string, radiusM int) error {
	center, ok := geo.CellCenter(cell)
	if !ok {
		return fmt.Errorf("estimate: invalid cell %q", cell)
	}
	prod, err := s.store.GetProduct(ctx, productID)
	if err != nil {
		return fmt.Errorf("estimate: load product: %w", err)
	}
	target := Target{
		ProductID: prod.ID, CategoryID: prod.CategoryID, NameEn: prod.NameEn, NameBn: prod.NameBn,
		Brand: prod.Brand, CategoryEn: prod.CategoryEn,
		Quantity: domain.Quantity{Value: prod.Quantity, Unit: domain.Unit(prod.Unit)},
	}
	now := s.now()
	pg := &pgEvidenceStore{store: s.store}
	gathered, err := Gather(ctx, pg, target, center, radiusM, s.params, now)
	if err != nil {
		return s.saveFailed(ctx, productID, cell, radiusM, err)
	}

	stored := StoredEvidence{Items: gathered.Evidence, Counts: countByType(gathered.Evidence), Ratio: gathered.Ratio, RingM: gathered.RingM}
	save := db.SaveEstimateParams{
		ProductID: productID, Cell: cell, RadiusM: int32(radiusM), //nolint:gosec // radius is bucketed to <= 25000
		ExpiresAt: now.Add(s.ttl),
	}

	if ok, reason := Sufficient(gathered.Evidence, s.params); !ok {
		// Not enough evidence: no estimate. Keep only the online prices that
		// do exist so the shopper still sees them.
		stored.Reason = reason
		stored.Items = filterType(gathered.Evidence, EvOnline)
		save.Status = string(domain.EstimateInsufficient)
		s.count(domain.EstimateInsufficient, "")
		return s.save(ctx, save, stored)
	}

	base, err := Baseline(gathered.Evidence, s.params)
	if err != nil {
		return s.saveFailed(ctx, productID, cell, radiusM, err)
	}
	result, method := base, domain.MethodStatistical
	expEn, expBn := statisticalExplanation(stored, base)

	if s.agent != nil {
		area := s.areaInfo(ctx, pg, center)
		start := time.Now()
		run, err := s.agent.Refine(ctx, AgentInput{Target: target, Area: area, RadiusM: radiusM, Gathered: gathered, Baseline: base, Params: s.params})
		s.observeLLM(run, time.Since(start), err)
		if err != nil {
			// Fall back to the baseline and record why.
			save.FallbackReason = truncate(err.Error(), 300)
			s.log.WarnContext(ctx, "estimate: llm refinement failed, using statistical baseline", "product_id", productID, "error", err)
		} else {
			method = domain.MethodLLMAssisted
			result = Result{
				Low: domain.Money(run.Output.Low), Point: domain.Money(run.Output.Point), High: domain.Money(run.Output.High),
				// The model may lower the confidence but never raise it above
				// what the evidence statistics support.
				Confidence: MinConfidence(run.Output.Confidence, base.Confidence),
			}
			expEn, expBn = truncate(run.Output.ExplanationEn, 1000), truncate(run.Output.ExplanationBn, 1000)
			stored.Cited = run.Output.EvidenceIDs
			save.ModelID = s.agent.Model()
		}
	}

	low, point, high := int64(result.Low), int64(result.Point), int64(result.High)
	conf, meth := string(result.Confidence), string(method)
	save.Status = string(domain.EstimateReady)
	save.LowPoisha, save.PointPoisha, save.HighPoisha = &low, &point, &high
	save.Confidence, save.Method = &conf, &meth
	save.ExplanationEn, save.ExplanationBn = expEn, expBn
	s.count(domain.EstimateReady, meth)
	return s.save(ctx, save, stored)
}

func (s *Service) save(ctx context.Context, p db.SaveEstimateParams, ev StoredEvidence) error {
	if ev.Items == nil {
		ev.Items = []Evidence{}
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("estimate: encode evidence: %w", err)
	}
	p.Evidence = raw
	if err := s.store.SaveEstimate(ctx, p); err != nil {
		return fmt.Errorf("estimate: save: %w", err)
	}
	return nil
}

// saveFailed records a failed run so the UI stops polling, and returns the
// original error so the job is retried by River.
func (s *Service) saveFailed(ctx context.Context, productID int64, cell string, radiusM int, cause error) error {
	s.count(domain.EstimateFailed, "")
	saveErr := s.save(ctx, db.SaveEstimateParams{
		ProductID: productID, Cell: cell, RadiusM: int32(radiusM), //nolint:gosec // radius is bucketed to <= 25000
		Status: string(domain.EstimateFailed), ExpiresAt: s.now().Add(pendingTimeout),
	}, StoredEvidence{})
	return errors.Join(cause, saveErr)
}

func (s *Service) count(status domain.EstimateStatus, method string) {
	if s.metrics != nil {
		s.metrics.Estimates.WithLabelValues(string(status), method).Inc()
	}
}

func (s *Service) observeLLM(run AgentRun, elapsed time.Duration, err error) {
	if s.metrics == nil {
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	s.metrics.LLMCalls.WithLabelValues(outcome).Add(float64(run.Calls))
	s.metrics.LLMTokens.WithLabelValues("prompt").Add(float64(run.Usage.PromptTokens))
	s.metrics.LLMTokens.WithLabelValues("completion").Add(float64(run.Usage.CompletionTokens))
	s.metrics.LLMLatency.Observe(elapsed.Seconds())
}

func (s *Service) areaInfo(ctx context.Context, pg *pgEvidenceStore, center geo.Point) AreaInfo {
	d, err := pg.store.NearestDistrict(ctx, db.NearestDistrictParams{Lat: center.Lat, Lng: center.Lng})
	if err != nil {
		return AreaInfo{}
	}
	return AreaInfo{DistrictEn: d.NameEn, DistrictBn: d.NameBn, DistanceKm: float64(int(d.DistanceM/100)) / 10}
}

func countByType(ev []Evidence) map[string]int {
	counts := map[string]int{}
	for _, e := range ev {
		counts[string(e.Type)]++
	}
	return counts
}

func filterType(ev []Evidence, t EvidenceType) []Evidence {
	out := []Evidence{}
	for _, e := range ev {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

func truncate(s string, maxRunes int) string {
	rs := []rune(s)
	if len(rs) <= maxRunes {
		return s
	}
	return string(rs[:maxRunes])
}

// statisticalExplanation describes a baseline estimate in both languages.
func statisticalExplanation(ev StoredEvidence, r Result) (en, bn string) {
	n := len(ev.Items)
	same := ev.Counts[string(EvLocal)] + ev.Counts[string(EvOtherArea)]
	online := ev.Counts[string(EvOnline)]
	other := n - same - online
	en = fmt.Sprintf("Weighted median of %d price points (%d local listings of this product, %d online offers, %d comparable or past prices), weighted by distance and freshness.", n, same, online, other)
	bn = fmt.Sprintf("%sটি মূল্য-তথ্যের ভারযুক্ত মধ্যক (এই পণ্যের %sটি স্থানীয় তালিকা, %sটি অনলাইন অফার, %sটি তুলনীয় বা পুরোনো মূল্য); দূরত্ব ও তথ্যের সতেজতা অনুযায়ী ভার দেওয়া হয়েছে।",
		domain.ToBanglaDigits(fmt.Sprint(n)), domain.ToBanglaDigits(fmt.Sprint(same)), domain.ToBanglaDigits(fmt.Sprint(online)), domain.ToBanglaDigits(fmt.Sprint(other)))
	if r.Confidence == domain.ConfidenceLow {
		en += " Evidence is thin, so the range is wide."
		bn += " তথ্য কম থাকায় পরিসরটি বড় রাখা হয়েছে।"
	}
	return en, bn
}

// pgEvidenceStore implements EvidenceStore on Postgres.
type pgEvidenceStore struct {
	store *store.Store
}

func (p *pgEvidenceStore) ListingsInRing(ctx context.Context, productID int64, center geo.Point, minM, maxM float64, c Cutoffs, limit int) ([]ListingRow, error) {
	rows, err := p.store.EvidenceListings(ctx, db.EvidenceListingsParams{
		Lat: center.Lat, Lng: center.Lng, ProductID: productID, VisibleSince: c.ListingsSince,
		MaxM: maxM, MinM: minM, MaxResults: int32(limit), //nolint:gosec // small constant limits
	})
	if err != nil {
		return nil, err
	}
	out := make([]ListingRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ListingRow{ID: r.ID, ShopID: r.ShopID, Price: r.PricePoisha, ObservedAt: r.UpdatedAt, DistanceM: r.DistanceM})
	}
	return out, nil
}

func (p *pgEvidenceStore) OtherAreaListings(ctx context.Context, productID int64, center geo.Point, beyondM float64, c Cutoffs, limit int) ([]ListingRow, error) {
	district, err := p.store.NearestDistrict(ctx, db.NearestDistrictParams{Lat: center.Lat, Lng: center.Lng})
	if store.IsNotFound(err) {
		return nil, nil // no areas seeded: nothing to compare against
	}
	if err != nil {
		return nil, err
	}
	area, err := p.store.GetArea(ctx, district.ID)
	if err != nil {
		return nil, err
	}
	if area.ParentID == nil {
		return nil, nil
	}
	rows, err := p.store.EvidenceOtherAreaListings(ctx, db.EvidenceOtherAreaListingsParams{
		Lat: center.Lat, Lng: center.Lng, ProductID: productID, VisibleSince: c.ListingsSince,
		DivisionID: area.ParentID, MinM: beyondM, MaxResults: int32(limit), //nolint:gosec // small constant limits
	})
	if err != nil {
		return nil, err
	}
	out := make([]ListingRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ListingRow{ID: r.ID, ShopID: r.ShopID, Price: r.PricePoisha, ObservedAt: r.UpdatedAt, DistanceM: r.DistanceM})
	}
	return out, nil
}

func (p *pgEvidenceStore) Offers(ctx context.Context, productID int64, c Cutoffs, limit int) ([]OfferRow, error) {
	rows, err := p.store.EvidenceOffers(ctx, db.EvidenceOffersParams{
		ProductID: productID, ObservedSince: c.OffersSince, MinConfidence: matching.MinConfidence, MaxResults: int32(limit), //nolint:gosec // small constant limits
	})
	if err != nil {
		return nil, err
	}
	out := make([]OfferRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, OfferRow{ID: r.ID, Price: r.PricePoisha, ObservedAt: r.ObservedAt})
	}
	return out, nil
}

func (p *pgEvidenceStore) Comparables(ctx context.Context, t Target, center geo.Point, radiusM float64, c Cutoffs, limit int) ([]ComparableRow, error) {
	rows, err := p.store.EvidenceComparables(ctx, db.EvidenceComparablesParams{
		Lat: center.Lat, Lng: center.Lng, CategoryID: t.CategoryID, Unit: string(t.Quantity.Unit), ProductID: t.ProductID,
		MinQuantity: max(t.Quantity.Value/2, 1), MaxQuantity: t.Quantity.Value * 2,
		VisibleSince: c.ListingsSince, RadiusM: radiusM, MaxResults: int32(limit), //nolint:gosec // small constant limits
	})
	if err != nil {
		return nil, err
	}
	out := make([]ComparableRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ComparableRow{
			ListingRow: ListingRow{ID: r.ID, ShopID: r.ShopID, Price: r.PricePoisha, ObservedAt: r.UpdatedAt, DistanceM: r.DistanceM},
			Quantity:   r.Quantity,
		})
	}
	return out, nil
}

func (p *pgEvidenceStore) History(ctx context.Context, productID int64, center geo.Point, radiusM float64, c Cutoffs, limit int) ([]ListingRow, error) {
	rows, err := p.store.EvidenceHistory(ctx, db.EvidenceHistoryParams{
		Lat: center.Lat, Lng: center.Lng, ProductID: productID, RecordedSince: c.HistorySince,
		RadiusM: radiusM, MaxResults: int32(limit), //nolint:gosec // small constant limits
	})
	if err != nil {
		return nil, err
	}
	out := make([]ListingRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ListingRow{ID: r.ID, ShopID: r.ShopID, Price: r.PricePoisha, ObservedAt: r.RecordedAt, DistanceM: r.DistanceM})
	}
	return out, nil
}

func (p *pgEvidenceStore) RatioPairs(ctx context.Context, excludeProductID int64, center geo.Point, radiusM float64, c Cutoffs) ([]RatioPair, error) {
	rows, err := p.store.OnlineOfflinePairs(ctx, db.OnlineOfflinePairsParams{
		Lat: center.Lat, Lng: center.Lng, RadiusM: radiusM, VisibleSince: c.ListingsSince,
		ObservedSince: c.OffersSince, ExcludeProductID: excludeProductID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]RatioPair, 0, len(rows))
	for _, r := range rows {
		out = append(out, RatioPair{ProductID: r.ProductID, CategoryID: r.CategoryID, MedianLocal: r.MedianLocal, MedianOnline: r.MedianOnline, Near: r.Near})
	}
	return out, nil
}
