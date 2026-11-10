//go:build llmlive

// Package llmlive runs the estimation agent against a real OpenAI-compatible
// endpoint (LLM_BASE_URL, LLM_MODEL, LLM_API_KEY). It is manual and never
// part of CI: it costs requests and depends on a third party.
//
//	LLM_BASE_URL=https://openrouter.ai/api/v1 LLM_MODEL=... LLM_API_KEY=... \
//	  go test -tags llmlive -count=1 -v ./test/llmlive/
//
// The evidence sent is synthetic (no user data): product name, prices,
// distances and ages.
package llmlive

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/estimate"
	"github.com/shtmthw/ponnoKhoj/internal/llm"
)

func f(v float64) *float64 { return &v }

func TestAgentAgainstLiveModel(t *testing.T) {
	base, model, key := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_MODEL"), os.Getenv("LLM_API_KEY")
	if base == "" || model == "" {
		t.Skip("set LLM_BASE_URL and LLM_MODEL")
	}
	p := estimate.DefaultParams()
	ev := []estimate.Evidence{
		{ID: "L101", Type: estimate.EvLocal, PricePoisha: 46500, AdjustedPoisha: 46500, DistanceM: f(4200), AgeDays: 1},
		{ID: "L102", Type: estimate.EvLocal, PricePoisha: 45800, AdjustedPoisha: 45800, DistanceM: f(6100), AgeDays: 3},
		{ID: "L103", Type: estimate.EvLocal, PricePoisha: 47200, AdjustedPoisha: 47200, DistanceM: f(8800), AgeDays: 2},
		{ID: "A201", Type: estimate.EvOtherArea, PricePoisha: 48500, AdjustedPoisha: 48500, DistanceM: f(41000), AgeDays: 5},
		{ID: "O301", Type: estimate.EvOnline, PricePoisha: 49200, AdjustedPoisha: 46740, AgeDays: 0.5},
		{ID: "C401", Type: estimate.EvComparable, PricePoisha: 8900, AdjustedPoisha: 44500, DistanceM: f(1200), AgeDays: 2},
	}
	for i := range ev {
		ev[i].Weight = estimate.Weight(ev[i], estimate.Ratio{Value: 0.95, Scope: "global"}, p)
	}
	g := estimate.Gathered{Evidence: ev, Ratio: estimate.Ratio{Value: 0.95, Scope: "global", Pairs: 6}, RingM: 10000}
	base0, err := estimate.Baseline(ev, p)
	if err != nil {
		t.Fatal(err)
	}
	in := estimate.AgentInput{
		Target:  estimate.Target{ProductID: 3, CategoryID: 2, NameEn: "Nazirshail Rice 5 kg", NameBn: "নাজিরশাইল চাল ৫ কেজি", CategoryEn: "Rice & Grains", Quantity: domain.Quantity{Value: 5000, Unit: domain.UnitGram}},
		Area:    estimate.AreaInfo{DistrictEn: "Chattogram", DistrictBn: "চট্টগ্রাম", DistanceKm: 1.2},
		RadiusM: 3000, Gathered: g, Baseline: base0, Params: p,
	}
	agent := estimate.NewAgent(llm.NewHTTPClient(base, key), estimate.AgentConfig{Model: model, MaxSteps: 6, Timeout: 90 * time.Second, MaxTokens: 1024})
	start := time.Now()
	run, err := agent.Refine(context.Background(), in)
	t.Logf("model %s: %d calls, %d prompt + %d completion tokens, %s", model, run.Calls, run.Usage.PromptTokens, run.Usage.CompletionTokens, time.Since(start).Round(time.Millisecond))
	t.Logf("baseline: %s – %s (point %s, %s)", base0.Low.Decimal(), base0.High.Decimal(), base0.Point.Decimal(), base0.Confidence)
	if err != nil {
		t.Fatalf("refinement failed (production would fall back to the baseline): %v", err)
	}
	o := run.Output
	t.Logf("model answer: low %d, point %d, high %d poisha, confidence %s, cites %s", o.Low, o.Point, o.High, o.Confidence, strings.Join(o.EvidenceIDs, ","))
	t.Logf("en: %s", o.ExplanationEn)
	t.Logf("bn: %s", o.ExplanationBn)
}
