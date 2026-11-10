package estimate

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/llm"
)

func fptr(f float64) *float64 { return &f }

// ev builds a weighted evidence item for baseline tests.
func ev(id string, typ EvidenceType, price int64, distM, ageDays float64, origin string) Evidence {
	e := Evidence{ID: id, Type: typ, PricePoisha: price, AdjustedPoisha: price, AgeDays: ageDays, origin: origin}
	if typ != EvOnline {
		e.DistanceM = fptr(distM)
	}
	e.Weight = Weight(e, Ratio{Value: 1, Scope: "global"}, DefaultParams())
	return e
}

func TestWeight(t *testing.T) {
	t.Parallel()
	p := DefaultParams()
	learned := Ratio{Value: 0.95, Scope: "global"}
	near := ev("L1", EvLocal, 100, 0, 0, "shop:1")
	if math.Abs(near.Weight-1) > 1e-9 {
		t.Errorf("fresh, zero-distance local listing weight = %v, want 1", near.Weight)
	}
	// Distance decay: one scale length away multiplies the weight by 1/e.
	far := Evidence{Type: EvLocal, DistanceM: fptr(p.DistanceScaleM)}
	if got := Weight(far, learned, p); math.Abs(got-1/math.E) > 1e-9 {
		t.Errorf("weight at one distance scale = %v, want 1/e", got)
	}
	// Recency decay: one half-life halves the weight.
	old := Evidence{Type: EvLocal, DistanceM: fptr(0), AgeDays: p.RecencyHalfLifeDays}
	if got := Weight(old, learned, p); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("weight at one half-life = %v, want 0.5", got)
	}
	// Type trust ordering.
	order := []EvidenceType{EvLocal, EvOnline, EvOtherArea, EvComparable}
	for i := 1; i < len(order); i++ {
		a := Weight(Evidence{Type: order[i-1]}, learned, p)
		b := Weight(Evidence{Type: order[i]}, learned, p)
		if a < b {
			t.Errorf("%s (%v) should not weigh less than %s (%v)", order[i-1], a, order[i], b)
		}
	}
	// Online offers weigh less when no ratio could be learned.
	if adj, unadj := Weight(Evidence{Type: EvOnline}, learned, p), Weight(Evidence{Type: EvOnline}, Ratio{Value: 1, Scope: "none"}, p); unadj >= adj {
		t.Errorf("unadjusted online weight %v should be below adjusted %v", unadj, adj)
	}
}

func TestLearnRatio(t *testing.T) {
	t.Parallel()
	pair := func(cat int64, local, online float64, near bool) RatioPair {
		return RatioPair{CategoryID: cat, MedianLocal: local, MedianOnline: online, Near: near}
	}
	// Three local pairs in the category: category_local wins.
	r := LearnRatio([]RatioPair{pair(1, 90, 100, true), pair(1, 92, 100, true), pair(1, 94, 100, true), pair(2, 50, 100, true)}, 1)
	if r.Scope != "category_local" || math.Abs(r.Value-0.92) > 1e-9 || r.Pairs != 3 {
		t.Errorf("category_local ratio = %+v", r)
	}
	// Too few in the category: fall back to the global median.
	r = LearnRatio([]RatioPair{pair(1, 90, 100, true), pair(2, 80, 100, false), pair(3, 70, 100, true), pair(4, 60, 100, true)}, 1)
	if r.Scope != "global" || math.Abs(r.Value-0.75) > 1e-9 || r.Pairs != 4 {
		t.Errorf("global ratio = %+v", r)
	}
	// Category pairs that are not near do not count as local.
	r = LearnRatio([]RatioPair{pair(1, 90, 100, false), pair(1, 90, 100, false), pair(1, 90, 100, false)}, 1)
	if r.Scope != "global" {
		t.Errorf("far category pairs should fall back to global, got %+v", r)
	}
	// Too few overall: no adjustment.
	r = LearnRatio([]RatioPair{pair(1, 90, 100, true), pair(1, 0, 100, true), pair(1, 90, 0, true)}, 1)
	if r.Scope != "none" || r.Value != 1 {
		t.Errorf("none ratio = %+v", r)
	}
	// Clamped to [0.5, 2].
	r = LearnRatio([]RatioPair{pair(1, 10, 100, true), pair(1, 10, 100, true), pair(1, 10, 100, true)}, 1)
	if r.Value != 0.5 {
		t.Errorf("ratio not clamped low: %+v", r)
	}
	r = LearnRatio([]RatioPair{pair(1, 900, 100, true), pair(1, 900, 100, true), pair(1, 900, 100, true)}, 1)
	if r.Value != 2 {
		t.Errorf("ratio not clamped high: %+v", r)
	}
}

func TestSufficient(t *testing.T) {
	t.Parallel()
	p := DefaultParams()
	three := []Evidence{ev("L1", EvLocal, 100, 0, 1, "shop:1"), ev("L2", EvLocal, 100, 0, 1, "shop:2"), ev("O1", EvOnline, 100, 0, 1, "offer:1")}
	if ok, reason := Sufficient(three, p); !ok {
		t.Errorf("three independent same-product items should suffice: %s", reason)
	}
	// A listing and its own history are one independent source.
	sameShop := []Evidence{ev("L1", EvLocal, 100, 0, 1, "shop:1"), ev("H1", EvHistory, 95, 0, 40, "shop:1"), ev("L2", EvLocal, 100, 0, 1, "shop:2")}
	if ok, reason := Sufficient(sameShop, p); ok || reason != ReasonTooFew {
		t.Errorf("two shops should not suffice, got ok=%v reason=%q", ok, reason)
	}
	// Comparables never make an estimate sufficient on their own.
	comps := []Evidence{ev("O1", EvOnline, 100, 0, 1, "offer:1")}
	for i := range 20 {
		comps = append(comps, ev("C"+string(rune('a'+i)), EvComparable, 80, 0, 1, "shop:"+string(rune('a'+i))))
	}
	if ok, reason := Sufficient(comps, p); ok || reason != ReasonTooFew {
		t.Errorf("comparables alone should not suffice, got ok=%v reason=%q", ok, reason)
	}
	// Only stale evidence is insufficient.
	stale := []Evidence{ev("L1", EvLocal, 100, 0, 20, "shop:1"), ev("L2", EvLocal, 100, 0, 9, "shop:2"), ev("L3", EvLocal, 100, 0, 25, "shop:3")}
	if ok, reason := Sufficient(stale, p); ok || reason != ReasonAllStale {
		t.Errorf("all-stale evidence should not suffice, got ok=%v reason=%q", ok, reason)
	}
	if ok, _ := Sufficient(nil, p); ok {
		t.Error("no evidence should not suffice")
	}
}

func TestBaselineWeightedMedianAndRange(t *testing.T) {
	t.Parallel()
	p := DefaultParams()
	items := []Evidence{
		ev("L1", EvLocal, 40000, 500, 1, "shop:1"), ev("L2", EvLocal, 40500, 800, 2, "shop:2"),
		ev("L3", EvLocal, 41000, 1200, 1, "shop:3"), ev("L4", EvLocal, 39500, 300, 3, "shop:4"),
		ev("L5", EvLocal, 42000, 2500, 2, "shop:5"),
	}
	r, err := Baseline(items, p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Point != 40500 {
		t.Errorf("point = %d, want the weighted median 40500", r.Point)
	}
	if r.Low >= r.Point || r.Point >= r.High {
		t.Errorf("range is not a proper range: %d..%d..%d", r.Low, r.Point, r.High)
	}
	// Whole-taka bounds, rounded outwards.
	if r.Low%100 != 0 || r.High%100 != 0 {
		t.Errorf("bounds are not whole taka: %d, %d", r.Low, r.High)
	}
	// Within the envelope around the evidence.
	lo, hi := Envelope(items, p)
	if int64(r.Low) < lo || int64(r.High) > hi {
		t.Errorf("range %d..%d escapes the envelope %d..%d", r.Low, r.High, lo, hi)
	}
	// Deterministic: same input, same output, regardless of order.
	rev := append([]Evidence(nil), items...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	if r2, _ := Baseline(rev, p); r2 != r {
		t.Errorf("baseline depends on evidence order: %+v vs %+v", r, r2)
	}
}

func TestBaselineOutlierResistance(t *testing.T) {
	t.Parallel()
	items := []Evidence{
		ev("L1", EvLocal, 40000, 500, 1, "shop:1"), ev("L2", EvLocal, 40500, 500, 1, "shop:2"),
		ev("L3", EvLocal, 41000, 500, 1, "shop:3"), ev("L4", EvLocal, 40200, 500, 1, "shop:4"),
		ev("L5", EvLocal, 400000, 500, 1, "shop:5"), // a typo ten times too high
	}
	r, err := Baseline(items, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	if r.Point < 40000 || r.Point > 41000 {
		t.Errorf("one outlier moved the point to %d", r.Point)
	}
}

func TestBaselineWidensWhenThin(t *testing.T) {
	t.Parallel()
	p := DefaultParams()
	var thin, rich []Evidence
	for i := range 3 {
		thin = append(thin, ev("L"+string(rune('1'+i)), EvLocal, 40000, 100, 1, "shop:"+string(rune('1'+i))))
	}
	for i := range 20 {
		rich = append(rich, ev("L"+string(rune('a'+i)), EvLocal, 40000, 100, 1, "shop:"+string(rune('a'+i))))
	}
	a, _ := Baseline(thin, p)
	b, _ := Baseline(rich, p)
	if (a.High - a.Low) <= (b.High - b.Low) {
		t.Errorf("thin evidence range %d should be wider than rich evidence range %d", a.High-a.Low, b.High-b.Low)
	}
	// Even perfect agreement never collapses to a point.
	if b.Low >= b.Point || b.High <= b.Point {
		t.Errorf("range collapsed: %+v", b)
	}
}

func TestBaselineConfidence(t *testing.T) {
	t.Parallel()
	p := DefaultParams()
	build := func(n int, spread int64, age float64, typ EvidenceType) []Evidence {
		var out []Evidence
		for i := range n {
			out = append(out, ev("L"+string(rune('a'+i)), typ, 40000+int64(i)*spread, 500, age, "shop:"+string(rune('a'+i))))
		}
		return out
	}
	tests := []struct {
		name  string
		items []Evidence
		want  domain.Confidence
	}{
		{"many fresh agreeing local listings", build(10, 100, 1, EvLocal), domain.ConfidenceHigh},
		{"few listings", build(4, 100, 1, EvLocal), domain.ConfidenceMedium},
		{"many but disagreeing", build(10, 6000, 1, EvLocal), domain.ConfidenceLow},
		{"many but none fresh", build(10, 100, 20, EvLocal), domain.ConfidenceLow},
		{"three only", build(3, 100, 1, EvLocal), domain.ConfidenceLow},
		{"many, but not the local same product", build(10, 100, 1, EvOtherArea), domain.ConfidenceMedium},
	}
	for _, tc := range tests {
		r, err := Baseline(tc.items, p)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if r.Confidence != tc.want {
			t.Errorf("%s: confidence = %s, want %s", tc.name, r.Confidence, tc.want)
		}
	}
}

func TestBaselineEdgeCases(t *testing.T) {
	t.Parallel()
	p := DefaultParams()
	if _, err := Baseline(nil, p); !errors.Is(err, ErrNoEvidence) {
		t.Errorf("empty evidence err = %v", err)
	}
	zero := []Evidence{{ID: "L1", Type: EvLocal, AdjustedPoisha: 100, Weight: 0}, {ID: "L2", Type: EvLocal, AdjustedPoisha: 0, Weight: 1}}
	if _, err := Baseline(zero, p); !errors.Is(err, ErrNoEvidence) {
		t.Errorf("unusable evidence err = %v", err)
	}
	// Tiny prices keep poisha precision and stay positive.
	r, err := Baseline([]Evidence{ev("L1", EvLocal, 150, 0, 0, "shop:1"), ev("L2", EvLocal, 160, 0, 0, "shop:2"), ev("L3", EvLocal, 170, 0, 0, "shop:3")}, p)
	if err != nil || r.Low < 1 || r.Low > r.Point || r.Point > r.High {
		t.Errorf("tiny price estimate = %+v, %v", r, err)
	}
	if MinConfidence(domain.ConfidenceHigh, domain.ConfidenceLow) != domain.ConfidenceLow ||
		MinConfidence(domain.ConfidenceMedium, domain.ConfidenceHigh) != domain.ConfidenceMedium ||
		MinConfidence(domain.ConfidenceHigh, domain.ConfidenceHigh) != domain.ConfidenceHigh {
		t.Error("MinConfidence wrong")
	}
}

func TestEnvelopeClamping(t *testing.T) {
	t.Parallel()
	p := DefaultParams()
	items := []Evidence{{AdjustedPoisha: 10000}, {AdjustedPoisha: 20000}, {AdjustedPoisha: 0}}
	lo, hi := Envelope(items, p)
	if lo != 5000 || hi != 30000 {
		t.Errorf("envelope = %d..%d, want 5000..30000", lo, hi)
	}
	// The clamp pulls a too-wide range in but never past the point.
	low, high := clampToEnvelope(100, 99999, 15000, items, p)
	if low != 5000 || high != 30000 {
		t.Errorf("clamped = %d..%d", low, high)
	}
	low, high = clampToEnvelope(16000, 14000, 15000, items, p)
	if low != 15000 || high != 15000 {
		t.Errorf("inverted bounds clamp to the point, got %d..%d", low, high)
	}
	// A tiny envelope never goes below one poisha.
	if lo, _ := Envelope([]Evidence{{AdjustedPoisha: 1}}, p); lo != 1 {
		t.Errorf("envelope low = %d, want 1", lo)
	}
}

func TestCapComparables(t *testing.T) {
	t.Parallel()
	items := []Evidence{{Type: EvLocal, Weight: 1}, {Type: EvLocal, Weight: 1}}
	for range 20 {
		items = append(items, Evidence{Type: EvComparable, Weight: 0.3})
	}
	out := capComparables(items)
	var comp float64
	for _, e := range out {
		if e.Type == EvComparable {
			comp += e.Weight
		}
	}
	if math.Abs(comp-MaxComparableShare*2) > 1e-9 {
		t.Errorf("comparable weight = %v, want %v", comp, MaxComparableShare*2)
	}
	// Already under the cap: untouched.
	small := capComparables([]Evidence{{Type: EvLocal, Weight: 4}, {Type: EvComparable, Weight: 0.3}})
	if small[1].Weight != 0.3 {
		t.Errorf("under-cap comparable was rescaled to %v", small[1].Weight)
	}
	// No same-product evidence: comparables weigh nothing.
	alone := capComparables([]Evidence{{Type: EvComparable, Weight: 0.3}})
	if alone[0].Weight != 0 {
		t.Errorf("comparable without same-product evidence weighs %v", alone[0].Weight)
	}
}

// fakeStore is an in-memory EvidenceStore for Gather tests.
type fakeStore struct {
	rings       map[float64][]ListingRow // keyed by ring max
	other       []ListingRow
	offers      []OfferRow
	comparables []ComparableRow
	history     []ListingRow
	pairs       []RatioPair
	failOn      string
	ringCalls   []float64
}

var errStore = errors.New("store down")

func (f *fakeStore) ListingsInRing(_ context.Context, _ int64, _ geo.Point, _, maxM float64, _ Cutoffs, _ int) ([]ListingRow, error) {
	f.ringCalls = append(f.ringCalls, maxM)
	if f.failOn == "ring" {
		return nil, errStore
	}
	return f.rings[maxM], nil
}

func (f *fakeStore) OtherAreaListings(context.Context, int64, geo.Point, float64, Cutoffs, int) ([]ListingRow, error) {
	if f.failOn == "other" {
		return nil, errStore
	}
	return f.other, nil
}

func (f *fakeStore) Offers(context.Context, int64, Cutoffs, int) ([]OfferRow, error) {
	if f.failOn == "offers" {
		return nil, errStore
	}
	return f.offers, nil
}

func (f *fakeStore) Comparables(context.Context, Target, geo.Point, float64, Cutoffs, int) ([]ComparableRow, error) {
	if f.failOn == "comparables" {
		return nil, errStore
	}
	return f.comparables, nil
}

func (f *fakeStore) History(context.Context, int64, geo.Point, float64, Cutoffs, int) ([]ListingRow, error) {
	if f.failOn == "history" {
		return nil, errStore
	}
	return f.history, nil
}

func (f *fakeStore) RatioPairs(context.Context, int64, geo.Point, float64, Cutoffs) ([]RatioPair, error) {
	if f.failOn == "pairs" {
		return nil, errStore
	}
	return f.pairs, nil
}

var (
	evalNow    = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	testTarget = Target{ProductID: 1, CategoryID: 7, NameEn: "Miniket Rice 5 kg", NameBn: "মিনিকেট চাল ৫ কেজি", CategoryEn: "Rice & Grains", Quantity: domain.Quantity{Value: 5000, Unit: domain.UnitGram}}
	testCenter = geo.Point{Lat: 22.335, Lng: 91.834}
)

func row(id, shop, price int64, distM float64, ageDays int) ListingRow {
	return ListingRow{ID: id, ShopID: shop, Price: price, DistanceM: distM, ObservedAt: evalNow.Add(-time.Duration(ageDays) * 24 * time.Hour)}
}

func TestGather(t *testing.T) {
	t.Parallel()
	st := &fakeStore{
		rings: map[float64][]ListingRow{
			3000:  {row(1, 1, 40000, 420, 1), row(2, 2, 40500, 880, 2)},
			10000: {row(3, 3, 41000, 6400, 1)},
			25000: {row(4, 4, 39000, 18000, 3)},
		},
		other:       []ListingRow{row(5, 5, 43000, 60000, 2)},
		offers:      []OfferRow{{ID: 1, Price: 44000, ObservedAt: evalNow.Add(-24 * time.Hour)}},
		comparables: []ComparableRow{{ListingRow: row(6, 6, 8200, 500, 1), Quantity: 1000}, {ListingRow: row(7, 7, 1, 500, 1), Quantity: 0}},
		history:     []ListingRow{row(8, 1, 39000, 420, 40)},
		pairs: []RatioPair{
			{CategoryID: 7, MedianLocal: 90, MedianOnline: 100, Near: true}, {CategoryID: 7, MedianLocal: 90, MedianOnline: 100, Near: true},
			{CategoryID: 7, MedianLocal: 90, MedianOnline: 100, Near: true},
		},
	}
	g, err := Gather(context.Background(), st, testTarget, testCenter, 3000, DefaultParams(), evalNow)
	if err != nil {
		t.Fatal(err)
	}
	// All three rings were searched because there were never 2 x MinEvidence shops.
	if len(st.ringCalls) != 3 || st.ringCalls[0] != 3000 || st.ringCalls[2] != 25000 || g.RingM != 25000 {
		t.Errorf("ring calls = %v, RingM = %v", st.ringCalls, g.RingM)
	}
	byID := map[string]Evidence{}
	for _, e := range g.Evidence {
		byID[e.ID] = e
	}
	for _, id := range []string{"L1", "L2", "L3", "L4", "A5", "O1", "C6", "H8"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("evidence %s missing; have %v", id, g.Evidence)
		}
	}
	if _, ok := byID["C7"]; ok {
		t.Error("comparable with zero quantity should be dropped")
	}
	// Online offer adjusted by the learned ratio (0.9).
	if g.Ratio.Scope != "category_local" || byID["O1"].AdjustedPoisha != 39600 || byID["O1"].PricePoisha != 44000 {
		t.Errorf("online adjustment wrong: ratio %+v, offer %+v", g.Ratio, byID["O1"])
	}
	// Comparable rescaled from 1 kg to 5 kg.
	if byID["C6"].AdjustedPoisha != 41000 {
		t.Errorf("comparable rescale = %d, want 41000", byID["C6"].AdjustedPoisha)
	}
	// Distances are rounded to 100 m; online offers carry none.
	if d := byID["L1"].DistanceM; d == nil || *d != 400 {
		t.Errorf("L1 distance = %v, want 400", d)
	}
	if byID["O1"].DistanceM != nil {
		t.Error("online offer has a distance")
	}
	// Sorted by ID for determinism.
	for i := 1; i < len(g.Evidence); i++ {
		if g.Evidence[i-1].ID > g.Evidence[i].ID {
			t.Errorf("evidence not sorted: %s before %s", g.Evidence[i-1].ID, g.Evidence[i].ID)
		}
	}
	// History from shop 1 is not independent of listing L1.
	if n := Independent(SameProduct(g.Evidence)); n != 6 {
		t.Errorf("independent same-product sources = %d, want 6", n)
	}
}

func TestGatherStopsExpandingWhenEnough(t *testing.T) {
	t.Parallel()
	var near []ListingRow
	for i := int64(1); i <= 6; i++ {
		near = append(near, row(i, i, 40000, 500, 1))
	}
	st := &fakeStore{rings: map[float64][]ListingRow{3000: near, 10000: {row(99, 99, 1, 9000, 1)}}}
	g, err := Gather(context.Background(), st, testTarget, testCenter, 3000, DefaultParams(), evalNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.ringCalls) != 1 || g.RingM != 3000 {
		t.Errorf("should stop at the first ring: calls %v", st.ringCalls)
	}
	// A radius larger than 10 km skips the 10 km ring.
	st = &fakeStore{rings: map[float64][]ListingRow{}}
	if _, err := Gather(context.Background(), st, testTarget, testCenter, 15000, DefaultParams(), evalNow); err != nil {
		t.Fatal(err)
	}
	if len(st.ringCalls) != 2 || st.ringCalls[0] != 15000 || st.ringCalls[1] != 25000 {
		t.Errorf("ring calls for 15 km = %v", st.ringCalls)
	}
}

func TestGatherPropagatesStoreErrors(t *testing.T) {
	t.Parallel()
	for _, step := range []string{"ring", "other", "offers", "comparables", "history", "pairs"} {
		st := &fakeStore{failOn: step}
		if _, err := Gather(context.Background(), st, testTarget, testCenter, 3000, DefaultParams(), evalNow); !errors.Is(err, errStore) {
			t.Errorf("failure in %s: err = %v", step, err)
		}
	}
}

// agentEvidence is a small evidence set whose envelope is [19500, 66000].
func agentEvidence() Gathered {
	return Gathered{
		Evidence: []Evidence{
			ev("L1", EvLocal, 40000, 400, 1, "shop:1"), ev("L2", EvLocal, 40500, 900, 2, "shop:2"),
			ev("L3", EvLocal, 41000, 1500, 1, "shop:3"), ev("O1", EvOnline, 44000, 0, 1, "offer:1"),
			ev("C6", EvComparable, 39000, 500, 1, "shop:6"),
		},
		Ratio: Ratio{Value: 1, Scope: "global", Pairs: 4}, RingM: 3000,
	}
}

func agentInput() AgentInput {
	g := agentEvidence()
	base, _ := Baseline(g.Evidence, DefaultParams())
	return AgentInput{Target: testTarget, Area: AreaInfo{DistrictEn: "Chattogram", DistrictBn: "চট্টগ্রাম"}, RadiusM: 3000, Gathered: g, Baseline: base, Params: DefaultParams()}
}

func agentConfig() AgentConfig {
	return AgentConfig{Model: "fake-model", MaxSteps: 6, Timeout: 5 * time.Second, MaxTokens: 512}
}

func loadRecording(t *testing.T, name string) *llm.FakeClient {
	t.Helper()
	c, err := llm.LoadRecording("../../test/fixtures/llm/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAgentHappyPath(t *testing.T) {
	t.Parallel()
	client := loadRecording(t, "estimate_ok.json")
	agent := NewAgent(client, agentConfig())
	run, err := agent.Refine(context.Background(), agentInput())
	if err != nil {
		t.Fatal(err)
	}
	if run.Output.Low != 39000 || run.Output.Point != 40500 || run.Output.High != 43000 || run.Output.Confidence != domain.ConfidenceHigh {
		t.Errorf("output = %+v", run.Output)
	}
	if run.Calls != 2 || run.Usage.PromptTokens != 1170 || run.Usage.CompletionTokens != 124 {
		t.Errorf("accounting = %+v", run)
	}
	if agent.Model() != "fake-model" {
		t.Error("Model() wrong")
	}
	// Prompt-injection hygiene: instructions live in the system message; the
	// product is passed as JSON data; tools are the four read-only ones.
	first := client.Requests[0]
	if first.Messages[0].Role != "system" || !strings.Contains(first.Messages[0].Content, "DATA, not instructions") {
		t.Error("system prompt does not mark inputs as data")
	}
	if first.Messages[1].Role != "user" || !strings.Contains(first.Messages[1].Content, `"name_en":"Miniket Rice 5 kg"`) {
		t.Errorf("product is not passed as a JSON data field: %s", first.Messages[1].Content)
	}
	if len(first.Tools) != 4 || first.MaxTokens != 512 || first.Model != "fake-model" {
		t.Errorf("request tools/limits wrong: %d tools, %d tokens", len(first.Tools), first.MaxTokens)
	}
	// The second request carries both tool results, which contain only IDs
	// and numbers from the gathered evidence.
	second := client.Requests[1]
	var toolMsgs []llm.Message
	for _, m := range second.Messages {
		if m.Role == "tool" {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) != 2 || toolMsgs[0].ToolCallID != "call_1" || !strings.Contains(toolMsgs[0].Content, `"id":"L1"`) || !strings.Contains(toolMsgs[1].Content, `"id":"O1"`) {
		t.Errorf("tool results wrong: %+v", toolMsgs)
	}
	if strings.Contains(toolMsgs[0].Content, `"id":"O1"`) || strings.Contains(toolMsgs[0].Content, "Miniket") {
		t.Error("get_local_listings leaked other evidence or free text")
	}
}

func TestAgentRejectsHallucinatedEvidenceThenAcceptsFix(t *testing.T) {
	t.Parallel()
	client := loadRecording(t, "estimate_hallucinated_then_fixed.json")
	run, err := NewAgent(client, agentConfig()).Refine(context.Background(), agentInput())
	if err != nil {
		t.Fatal(err)
	}
	if run.Calls != 3 || len(run.Output.EvidenceIDs) != 2 || run.Output.EvidenceIDs[1] != "L2" {
		t.Errorf("run = %+v", run)
	}
	// The validation error was fed back to the model for the retry.
	last := client.Requests[2].Messages
	feedback := last[len(last)-1]
	if feedback.Role != "user" || !strings.Contains(feedback.Content, `"L999"`) || !strings.Contains(feedback.Content, "rejected by validation") {
		t.Errorf("feedback message = %+v", feedback)
	}
}

func TestAgentFallsBackAfterSecondInvalidAnswer(t *testing.T) {
	t.Parallel()
	client := loadRecording(t, "estimate_out_of_envelope_twice.json")
	_, err := NewAgent(client, agentConfig()).Refine(context.Background(), agentInput())
	if !errors.Is(err, ErrAgentInvalid) || !strings.Contains(err.Error(), "envelope") {
		t.Errorf("err = %v, want ErrAgentInvalid mentioning the envelope", err)
	}
	if client.Calls() != 3 {
		t.Errorf("calls = %d, want exactly one retry (3 calls)", client.Calls())
	}
}

func TestAgentCitingUnretrievedToolFamily(t *testing.T) {
	t.Parallel()
	// O1 exists in the evidence but the model never called get_online_offers,
	// so citing it is rejected: it must have been retrieved in THIS run.
	answer := `{"low": 39000, "point": 40500, "high": 43000, "confidence": "low", "evidence_ids": ["O1"], "explanation_bn": "ক", "explanation_en": "a"}`
	client := llm.NewFakeClient(
		llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "get_local_listings", Arguments: "{}"}}}},
		llm.Response{Message: llm.Message{Role: "assistant", Content: answer}},
		llm.Response{Message: llm.Message{Role: "assistant", Content: answer}},
	)
	if _, err := NewAgent(client, agentConfig()).Refine(context.Background(), agentInput()); !errors.Is(err, ErrAgentInvalid) {
		t.Errorf("err = %v", err)
	}
}

func TestAgentStepLimit(t *testing.T) {
	t.Parallel()
	loop := llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c", Name: "get_area_info", Arguments: "{}"}}}}
	client := llm.NewFakeClient(loop, loop, loop, loop)
	cfg := agentConfig()
	cfg.MaxSteps = 3
	run, err := NewAgent(client, cfg).Refine(context.Background(), agentInput())
	if !errors.Is(err, ErrAgentSteps) || run.Calls != 3 {
		t.Errorf("err = %v, calls = %d; want ErrAgentSteps after 3 calls", err, run.Calls)
	}
}

func TestAgentUnknownToolAndClientErrors(t *testing.T) {
	t.Parallel()
	client := llm.NewFakeClient(
		llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "delete_everything", Arguments: "{}"}, {ID: "c2", Name: "get_comparables", Arguments: "{}"}, {ID: "c3", Name: "get_area_info", Arguments: "{}"}}}},
		llm.Response{Message: llm.Message{Role: "assistant", Content: `{"low": 38000, "point": 39000, "high": 41000, "confidence": "low", "evidence_ids": ["C6"], "explanation_bn": "ক", "explanation_en": "a"}`}},
	)
	run, err := NewAgent(client, agentConfig()).Refine(context.Background(), agentInput())
	if err != nil || run.Output.Point != 39000 {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	msgs := client.Requests[1].Messages
	if !strings.Contains(msgs[len(msgs)-3].Content, "unknown tool") || !strings.Contains(msgs[len(msgs)-1].Content, "Chattogram") {
		t.Errorf("tool results = %q / %q", msgs[len(msgs)-3].Content, msgs[len(msgs)-1].Content)
	}

	failing := llm.NewFakeClient()
	failing.Err = errors.New("connection refused")
	if _, err := NewAgent(failing, agentConfig()).Refine(context.Background(), agentInput()); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("client error not propagated: %v", err)
	}

	// The total timeout bounds the loop.
	cfg := agentConfig()
	cfg.Timeout = time.Nanosecond
	time.Sleep(time.Millisecond)
	if _, err := NewAgent(llm.NewFakeClient(), cfg).Refine(context.Background(), agentInput()); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout err = %v", err)
	}
}

func TestValidateOutput(t *testing.T) {
	t.Parallel()
	g := agentEvidence()
	p := DefaultParams()
	retrieved := map[string]bool{"L1": true, "L2": true, "L3": true}
	good := `{"low": 39000, "point": 40500, "high": 43000, "confidence": "medium", "evidence_ids": ["L1", "L2"], "explanation_bn": "ব্যাখ্যা", "explanation_en": "explanation"}`
	out, err := ValidateOutput(good, retrieved, g.Evidence, p)
	if err != nil || out.Point != 40500 || out.Confidence != domain.ConfidenceMedium || len(out.EvidenceIDs) != 2 {
		t.Fatalf("valid output rejected: %+v, %v", out, err)
	}
	// Tolerates code fences and prose around the object.
	if _, err := ValidateOutput("Sure!\n```json\n"+good+"\n```\nHope that helps.", retrieved, g.Evidence, p); err != nil {
		t.Errorf("fenced output rejected: %v", err)
	}
	repl := func(old, new string) string { return strings.Replace(good, old, new, 1) }
	bad := map[string]string{
		"not json":                 "the price is about 400 taka",
		"empty":                    "",
		"broken json":              `{"low": 39000,`,
		"array":                    `[1, 2, 3]`,
		"missing field":            repl(`"confidence": "medium", `, ``),
		"extra field":              repl(`"low"`, `"note": "x", "low"`),
		"float amount":             repl(`"low": 39000`, `"low": 39000.5`),
		"string amount":            repl(`"low": 39000`, `"low": "39000"`),
		"zero amount":              repl(`"low": 39000`, `"low": 0`),
		"negative amount":          repl(`"low": 39000`, `"low": -5`),
		"bad confidence":           repl(`"medium"`, `"certain"`),
		"no evidence":              repl(`["L1", "L2"]`, `[]`),
		"duplicate evidence":       repl(`["L1", "L2"]`, `["L1", "L1"]`),
		"malformed evidence id":    repl(`["L1", "L2"]`, `["listing 1"]`),
		"empty explanation":        repl(`"explanation_en": "explanation"`, `"explanation_en": ""`),
		"low above point":          repl(`"low": 39000`, `"low": 41000`),
		"point above high":         repl(`"high": 43000`, `"high": 40000`),
		"hallucinated evidence id": repl(`["L1", "L2"]`, `["L1", "L999"]`),
		"evidence not retrieved":   repl(`["L1", "L2"]`, `["O1"]`),
		"below envelope":           repl(`"low": 39000`, `"low": 19000`),
		"above envelope":           repl(`"high": 43000`, `"high": 66001`),
		"too long":                 good + strings.Repeat(" ", 70_000),
		"invalid utf8":             repl("explanation", "expl\xffanation"),
		"bangla not in bangla":     repl(`"explanation_bn": "ব্যাখ্যা"`, `"explanation_bn": "explanation"`),
		"language drift":           repl(`"explanation_bn": "ব্যাখ্যা"`, `"explanation_bn": "দাম ৪৫৮ টাকা之间"`),
	}
	for name, raw := range bad {
		_, err := ValidateOutput(raw, retrieved, g.Evidence, p)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want a ValidationError", name, err)
			continue
		}
		if ve.Reason == "" || !strings.HasPrefix(ve.Error(), "estimate: invalid model output: ") {
			t.Errorf("%s: unhelpful error %q", name, ve.Error())
		}
	}
	// Exactly on the envelope bounds is allowed.
	edge := repl(`"low": 39000`, `"low": 19500`)
	edge = strings.Replace(edge, `"high": 43000`, `"high": 66000`, 1)
	if _, err := ValidateOutput(edge, retrieved, g.Evidence, p); err != nil {
		t.Errorf("answer on the envelope bounds rejected: %v", err)
	}
}

func TestStatisticalExplanation(t *testing.T) {
	t.Parallel()
	g := agentEvidence()
	stored := StoredEvidence{Items: g.Evidence, Counts: countByType(g.Evidence)}
	en, bn := statisticalExplanation(stored, Result{Confidence: domain.ConfidenceLow})
	if !strings.Contains(en, "5 price points (3 local listings of this product, 1 online offers, 1 comparable") || !strings.Contains(en, "Evidence is thin") {
		t.Errorf("english explanation = %q", en)
	}
	if !strings.Contains(bn, "৫টি মূল্য-তথ্যের") || !strings.Contains(bn, "তথ্য কম") {
		t.Errorf("bangla explanation = %q", bn)
	}
	en, _ = statisticalExplanation(stored, Result{Confidence: domain.ConfidenceHigh})
	if strings.Contains(en, "thin") {
		t.Error("high confidence explanation mentions thin evidence")
	}
	if got := filterType(g.Evidence, EvOnline); len(got) != 1 || got[0].ID != "O1" {
		t.Errorf("filterType = %v", got)
	}
	if truncate("চালচাল", 3) != "চাল" || truncate("ab", 5) != "ab" {
		t.Error("truncate wrong")
	}
}

func TestBacktestMeetsCommittedThresholds(t *testing.T) {
	t.Parallel()
	ds, err := LoadEvalDataset("../../test/fixtures/eval/dataset.json")
	if err != nil {
		t.Fatal(err)
	}
	th, err := LoadEvalThresholds("../../test/fixtures/eval/thresholds.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := Backtest(context.Background(), ds, DefaultParams(), 3000, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("baseline backtest: %d listings, %d estimated, MAPE %.2f%%, median abs error %s, range coverage %.1f%%",
		report.Listings, report.Estimated, report.MAPE*100, domain.Money(report.MedianAbsErrorPoisha).Decimal(), RoundPct(report.RangeCoverage))
	if failures := th.Check(report); len(failures) > 0 {
		t.Errorf("baseline misses the committed thresholds: %v", failures)
	}
	// Thresholds actually bite.
	strict := EvalThresholds{MaxMAPE: 0.0001, MinRangeCoverage: 0.9999, MinEstimatedShare: 1.1}
	if len(strict.Check(report)) != 3 {
		t.Error("impossible thresholds were not reported as missed")
	}
	// A refiner that fails falls back to the baseline; one that succeeds is used.
	failing, err := Backtest(context.Background(), ds, DefaultParams(), 3000, func(context.Context, AgentInput) (Result, error) {
		return Result{}, errors.New("llm down")
	})
	if err != nil || failing != report {
		t.Errorf("failing refiner changed the report: %+v vs %+v (%v)", failing, report, err)
	}
	wild, err := Backtest(context.Background(), ds, DefaultParams(), 3000, func(_ context.Context, in AgentInput) (Result, error) {
		return Result{Low: 1, Point: in.Baseline.Point * 2, High: in.Baseline.Point * 3}, nil
	})
	if err != nil || wild.MAPE <= report.MAPE {
		t.Errorf("refined results were not used: MAPE %v vs %v", wild.MAPE, report.MAPE)
	}
}

func TestEvalLoadErrors(t *testing.T) {
	t.Parallel()
	if _, err := LoadEvalDataset("does-not-exist.json"); err == nil {
		t.Error("missing dataset accepted")
	}
	if _, err := LoadEvalThresholds("does-not-exist.json"); err == nil {
		t.Error("missing thresholds accepted")
	}
	if _, err := LoadEvalDataset("../../go.mod"); err == nil {
		t.Error("non-JSON dataset accepted")
	}
	if _, err := LoadEvalThresholds("../../go.mod"); err == nil {
		t.Error("non-JSON thresholds accepted")
	}
	bad := EvalDataset{Listings: []EvalListing{{ID: 1, ShopID: 9, ProductID: 9, PricePoisha: 100}}}
	if _, err := Backtest(context.Background(), bad, DefaultParams(), 3000, nil); err == nil {
		t.Error("inconsistent dataset accepted")
	}
	if (EvalReport{}).EstimatedShare() != 0 {
		t.Error("empty report share")
	}
}
