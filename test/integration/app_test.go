//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/llm"
	"github.com/shtmthw/ponnoKhoj/internal/migrations"
	"github.com/shtmthw/ponnoKhoj/internal/seed"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// apiGet fetches a JSON API endpoint into v and returns the status code.
func apiGet(t *testing.T, base, path string, v any) int {
	t.Helper()
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			t.Fatalf("GET %s: not JSON (%v): %s", path, err, body)
		}
	}
	return resp.StatusCode
}

func apiPost(t *testing.T, base, path, body string, v any) int {
	t.Helper()
	resp, err := http.Post(base+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if v != nil {
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatalf("POST %s: not JSON (%v): %s", path, err, raw)
		}
	}
	return resp.StatusCode
}

type searchJSON struct {
	Selected *struct {
		ID     int64  `json:"id"`
		NameEn string `json:"name_en"`
	} `json:"selected_product"`
	Local *struct {
		NeedsLocation bool  `json:"needs_location"`
		Total         int64 `json:"total"`
		Listings      []struct {
			ShopName string `json:"shop_name"`
			Price    struct {
				Poisha int64  `json:"poisha"`
				BDT    string `json:"bdt"`
			} `json:"price"`
			Freshness string  `json:"freshness"`
			DistanceM float64 `json:"distance_m"`
		} `json:"listings"`
	} `json:"local"`
	Online *struct {
		SourcesEnabled int      `json:"sources_enabled"`
		Refreshing     bool     `json:"refreshing"`
		FailedSources  []string `json:"failed_sources"`
		Offers         []struct {
			SourceID string `json:"source_id"`
			Title    string `json:"title"`
			URL      string `json:"url"`
			IsDemo   bool   `json:"is_demo"`
			InStock  bool   `json:"in_stock"`
			Price    struct {
				Poisha int64 `json:"poisha"`
			} `json:"price"`
		} `json:"offers"`
	} `json:"online"`
	Estimate *estimateJSON    `json:"estimate"`
	Counts   map[string]int64 `json:"counts"`
}

type estimateJSON struct {
	State      string            `json:"state"`
	IsEstimate bool              `json:"is_estimate"`
	Disclaimer map[string]string `json:"disclaimer"`
	Low        *struct {
		Poisha int64 `json:"poisha"`
	} `json:"low"`
	Point *struct {
		Poisha int64 `json:"poisha"`
	} `json:"point"`
	High *struct {
		Poisha int64 `json:"poisha"`
	} `json:"high"`
	Confidence     string `json:"confidence"`
	Method         string `json:"method"`
	ModelID        string `json:"model_id"`
	FallbackReason string `json:"fallback_reason"`
	ExplanationEn  string `json:"explanation_en"`
	Evidence       *struct {
		Items []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"items"`
		Counts map[string]int `json:"counts"`
		Cited  []string       `json:"cited"`
		Reason string         `json:"reason"`
	} `json:"evidence"`
}

func seedDemo(t *testing.T, a *testApp) {
	t.Helper()
	authSvc := newWorld(t, a.rt.Store).auth
	if _, err := seed.Demo(context.Background(), a.rt.Store, authSvc, seed.DemoOptions{AdminPassword: testPassword, VendorPassword: testPassword}); err != nil {
		t.Fatal(err)
	}
}

func demoProductID(t *testing.T, a *testApp, name string) int64 {
	t.Helper()
	var id int64
	if err := a.rt.Store.Pool.QueryRow(context.Background(), `SELECT id FROM products WHERE name_en = $1`, name).Scan(&id); err != nil {
		t.Fatalf("demo product %q: %v", name, err)
	}
	return id
}

func chattogram(t *testing.T, a *testApp) geo.Point {
	t.Helper()
	ctx := context.Background()
	id, err := a.rt.Store.GetAreaByName(ctx, db.GetAreaByNameParams{Kind: "district", NameEn: "Chattogram"})
	if err != nil {
		t.Fatal(err)
	}
	area, err := a.rt.Store.GetArea(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return geo.Point{Lat: area.Lat, Lng: area.Lng}
}

// TestOnlinePipelineEndToEnd is the M3 flow: a search returns the cache at
// once and queues a refresh; the worker fetches the (fictional, self-hosted)
// demo shop through the real fetch client (robots.txt, rate limit), parses
// its JSON-LD, matches offers to the product and caches them.
func TestOnlinePipelineEndToEnd(t *testing.T) {
	t.Parallel()
	a := startApp(t, newDB(t, true), map[string]string{"SOURCES_ENABLED": "demoshop", "DEMO_SHOP_ENABLED": "true"}, true, nil)
	seedDemo(t, a)
	rice := demoProductID(t, a, "Miniket Rice 5 kg")
	path := fmt.Sprintf("/api/v1/search?product_id=%d&mode=online", rice)

	var first searchJSON
	if code := apiGet(t, a.server.URL, path, &first); code != 200 {
		t.Fatalf("search = %d", code)
	}
	// Stale-while-revalidate: the first answer comes straight from the
	// (empty) cache and says a refresh is under way.
	if first.Online == nil || first.Online.SourcesEnabled != 1 || !first.Online.Refreshing || len(first.Online.Offers) != 0 || first.Local != nil {
		t.Fatalf("first online answer = %+v", first.Online)
	}
	var got searchJSON
	waitFor(t, "the demo shop offer to be cached", 20*time.Second, func() bool {
		got = searchJSON{}
		apiGet(t, a.server.URL, path, &got)
		return len(got.Online.Offers) > 0
	})
	// Exactly the 5 kg product matched: the 1 kg and 25 kg rice and the
	// other rice on the demo shop's result page were rejected by matching.
	if len(got.Online.Offers) != 1 {
		t.Fatalf("offers = %+v, want exactly one", got.Online.Offers)
	}
	o := got.Online.Offers[0]
	if o.SourceID != "demoshop" || !o.IsDemo || o.Price.Poisha != 43800 || !o.InStock || o.Title != "Miniket Rice 5kg (মিনিকেট চাল)" {
		t.Errorf("offer = %+v", o)
	}
	if o.URL != a.server.URL+"/demo-shop/p/miniket-rice-5kg" {
		t.Errorf("offer URL = %q", o.URL)
	}
	// The cache is now fresh: no new refresh is queued.
	if got.Online.Refreshing || len(got.Online.FailedSources) != 0 || got.Counts["online"] != 1 {
		t.Errorf("after refresh: %+v, counts %v", got.Online, got.Counts)
	}
	// Source health was recorded.
	srcs, err := a.rt.Store.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range srcs {
		switch {
		case src.ID == "demoshop" && (!src.Enabled || src.LastSuccessAt == nil || src.ConsecutiveFailures != 0):
			t.Errorf("demo shop health = %+v", src)
		case src.ID != "demoshop" && src.Enabled:
			t.Errorf("source %s is enabled without being configured", src.ID)
		}
	}
	// Per-source metrics were exported.
	_, metrics, _ := a.rt.Handlers(a.ready)
	rec := newRecorder()
	metrics.ServeHTTP(rec, mustRequest(t, "/metrics"))
	for _, want := range []string{`ponnokhoj_source_requests_total{outcome="ok",source="demoshop"}`, `ponnokhoj_source_offers_total{source="demoshop"} 1`, `ponnokhoj_jobs_total{kind="refresh_offers",outcome="ok"}`} {
		if !strings.Contains(rec.body.String(), want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}

// TestOnlineModeWithoutSources: with every source off (the default) online
// mode is honest about it rather than implying nothing is sold online.
func TestOnlineModeWithoutSources(t *testing.T) {
	t.Parallel()
	a := startApp(t, newDB(t, true), nil, false, nil)
	seedDemo(t, a)
	var got searchJSON
	apiGet(t, a.server.URL, fmt.Sprintf("/api/v1/search?product_id=%d&mode=online", demoProductID(t, a, "Sugar 1 kg")), &got)
	if got.Online.SourcesEnabled != 0 || got.Online.Refreshing || len(got.Online.Offers) != 0 {
		t.Errorf("online without sources = %+v", got.Online)
	}
	b := newBrowser(t, a.server.URL)
	if _, body, _ := b.get("/search?q=sugar&mode=online"); !strings.Contains(body, "No online shop is connected") {
		t.Error("the page does not explain that no online source is connected")
	}
	if code, _, _ := b.get("/demo-shop/search?q=sugar"); code != 404 {
		t.Errorf("demo shop served while disabled: %d", code)
	}
}

// TestEstimatePipelineEndToEnd is the M4 flow through the worker, with the
// statistical baseline (no LLM configured).
func TestEstimatePipelineEndToEnd(t *testing.T) {
	t.Parallel()
	a := startApp(t, newDB(t, true), map[string]string{"SOURCES_ENABLED": "demoshop", "DEMO_SHOP_ENABLED": "true"}, true, nil)
	seedDemo(t, a)
	ctg := chattogram(t, a)
	loc := fmt.Sprintf("lat=%f&lng=%f", ctg.Lat, ctg.Lng)

	// Nazirshail rice is listed only ~8.5 km away: nothing within 3 km, so
	// the search suggests an estimate; nothing is computed until asked.
	nazir := demoProductID(t, a, "Nazirshail Rice 5 kg")
	var s searchJSON
	apiGet(t, a.server.URL, fmt.Sprintf("/api/v1/search?product_id=%d&mode=all&radius_m=3000&%s", nazir, loc), &s)
	if s.Local.Total != 0 || s.Estimate.State != "suggested" || !s.Estimate.IsEstimate {
		t.Fatalf("before requesting: local %d, estimate %+v", s.Local.Total, s.Estimate)
	}
	body := fmt.Sprintf(`{"product_id": %d, "lat": %f, "lng": %f, "radius_m": 3000}`, nazir, ctg.Lat, ctg.Lng)
	var e estimateJSON
	if code := apiPost(t, a.server.URL, "/api/v1/estimates", body, &e); code != http.StatusAccepted || e.State != "pending" {
		t.Fatalf("request estimate = %d %+v", code, e)
	}
	// Identical requests while it is in flight do not start a second job.
	apiPost(t, a.server.URL, "/api/v1/estimates", body, nil)
	apiPost(t, a.server.URL, "/api/v1/estimates", body, nil)

	path := fmt.Sprintf("/api/v1/estimates?product_id=%d&radius_m=3000&%s", nazir, loc)
	waitFor(t, "the estimate", 20*time.Second, func() bool {
		e = estimateJSON{}
		apiGet(t, a.server.URL, path, &e)
		return e.State != "pending"
	})
	if e.State != "ready" || e.Method != "statistical" || e.ModelID != "" {
		t.Fatalf("estimate = %+v", e)
	}
	// Always a range, in order, with the caveat in both languages.
	if e.Low == nil || e.Point == nil || e.High == nil || e.Low.Poisha >= e.Point.Poisha || e.Point.Poisha >= e.High.Poisha {
		t.Fatalf("estimate is not a proper range: %+v %+v %+v", e.Low, e.Point, e.High)
	}
	if !strings.Contains(e.Disclaimer["en"], "may be higher or lower") || e.Disclaimer["bn"] == "" {
		t.Errorf("disclaimer = %v", e.Disclaimer)
	}
	// The demo base price is ৳450 (x a shop factor of 0.95-1.06 and noise).
	if e.Point.Poisha < 40000 || e.Point.Poisha > 50000 {
		t.Errorf("point estimate ৳%d is far from the demo price level (~৳450)", e.Point.Poisha/100)
	}
	if e.Evidence == nil || e.Evidence.Counts["local_listing"] < 3 || len(e.Evidence.Items) < 3 {
		t.Errorf("evidence = %+v", e.Evidence)
	}
	var jobs int
	_ = a.rt.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'estimate' AND args->>'product_id' = $1`, fmt.Sprint(nazir)).Scan(&jobs)
	if jobs != 1 {
		t.Errorf("%d estimate jobs were queued for one request, want 1", jobs)
	}
	// Cached: asking again returns the stored estimate without a new job.
	var again estimateJSON
	if code := apiPost(t, a.server.URL, "/api/v1/estimates", body, &again); code != 200 || again.State != "ready" || again.Point.Poisha != e.Point.Poisha {
		t.Errorf("cached estimate = %d %+v", code, again)
	}
	// Only the ~1 km cell of the request is stored, not the coordinates.
	var cell string
	_ = a.rt.Store.Pool.QueryRow(context.Background(), `SELECT cell FROM estimates WHERE product_id = $1`, nazir).Scan(&cell)
	if cell != ctg.Cell() {
		t.Errorf("stored cell = %q, want %q", cell, ctg.Cell())
	}

	// Chinigura rice has no local listing anywhere: no estimate, never a
	// guess. Once its online price has been fetched it is still shown.
	chini := demoProductID(t, a, "Chinigura Rice 1 kg")
	onlinePath := fmt.Sprintf("/api/v1/search?product_id=%d&mode=online", chini)
	waitFor(t, "the chinigura online offer", 20*time.Second, func() bool {
		var r searchJSON
		apiGet(t, a.server.URL, onlinePath, &r)
		return len(r.Online.Offers) == 1
	})
	apiPost(t, a.server.URL, "/api/v1/estimates", fmt.Sprintf(`{"product_id": %d, "lat": %f, "lng": %f}`, chini, ctg.Lat, ctg.Lng), nil)
	path = fmt.Sprintf("/api/v1/estimates?product_id=%d&%s", chini, loc)
	waitFor(t, "the insufficient-data verdict", 20*time.Second, func() bool {
		e = estimateJSON{}
		apiGet(t, a.server.URL, path, &e)
		return e.State != "pending"
	})
	if e.State != "insufficient_data" || e.Low != nil || e.Point != nil || e.High != nil || e.Confidence != "" {
		t.Fatalf("thin evidence produced %+v, want insufficient_data with no numbers", e)
	}
	if e.Evidence == nil || e.Evidence.Reason == "" || len(e.Evidence.Items) != 1 || e.Evidence.Items[0].Type != "online_offer" {
		t.Errorf("insufficient_data should carry the online prices that exist: %+v", e.Evidence)
	}

	// The HTML flow: the button posts, the panel polls, the result renders.
	b := newBrowser(t, a.server.URL)
	egg := demoProductID(t, a, "Farm Egg 12 pcs")
	form := url.Values{"product": {fmt.Sprint(egg)}, "lat": {fmt.Sprint(ctg.Lat)}, "lng": {fmt.Sprint(ctg.Lng)}, "radius": {"3000"}, "back": {"/search?product=" + fmt.Sprint(egg)}}
	code, _, hdr := b.post("/estimate", form)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/search?product="+fmt.Sprint(egg) {
		t.Fatalf("no-JS estimate request = %d -> %q", code, hdr.Get("Location"))
	}
	pollURL := "/estimate?" + form.Encode()
	var panel string
	waitFor(t, "the estimate panel", 20*time.Second, func() bool {
		req, _ := http.NewRequest(http.MethodGet, a.server.URL+pollURL, nil)
		req.Header.Set("HX-Request", "true")
		_, panel, _ = b.do(req)
		return !strings.Contains(panel, "hx-get")
	})
	if !strings.Contains(panel, `class="range"`) || !strings.Contains(panel, "This is an estimate, not a quoted price") || strings.Contains(panel, "<html") {
		t.Errorf("estimate panel fragment = %s", panel)
	}
	// Without HTMX the poll URL sends the browser back to the results page.
	if code, _, hdr := b.get(pollURL); code != http.StatusSeeOther || !strings.HasPrefix(hdr.Get("Location"), "/search?product=") {
		t.Errorf("non-HTMX poll = %d -> %q", code, hdr.Get("Location"))
	}
}

// scriptedLLM is an in-process stand-in for an OpenAI-compatible endpoint.
// It behaves like a model: it calls a tool, reads the evidence the tool
// returned, and answers from it. With hallucinate set it cites evidence the
// tools never returned, every time.
type scriptedLLM struct {
	mu          sync.Mutex
	hallucinate bool
	calls       int
}

func (s *scriptedLLM) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	var toolResult string
	for _, m := range req.Messages {
		if m.Role == "tool" {
			toolResult = m.Content
		}
	}
	if toolResult == "" {
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "get_local_listings", Arguments: "{}"}}}, Usage: llm.Usage{PromptTokens: 300, CompletionTokens: 12}}, nil
	}
	var payload struct {
		Evidence []struct {
			ID             string `json:"id"`
			AdjustedPoisha int64  `json:"adjusted_poisha"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(toolResult), &payload); err != nil || len(payload.Evidence) == 0 {
		return llm.Response{}, fmt.Errorf("scripted llm: unusable tool result: %s", toolResult)
	}
	lo, hi := payload.Evidence[0].AdjustedPoisha, payload.Evidence[0].AdjustedPoisha
	ids := []string{}
	for _, e := range payload.Evidence {
		lo, hi = min(lo, e.AdjustedPoisha), max(hi, e.AdjustedPoisha)
		ids = append(ids, e.ID)
	}
	if s.hallucinate {
		ids = []string{"L424242"}
	}
	answer, _ := json.Marshal(map[string]any{
		"low": lo, "point": (lo + hi) / 2, "high": hi, "confidence": "high", "evidence_ids": ids,
		"explanation_bn": "কাছাকাছি দোকানের দাম অনুযায়ী।", "explanation_en": "Based on the nearby shop prices returned by the tools.",
	})
	return llm.Response{Message: llm.Message{Role: "assistant", Content: string(answer)}, Usage: llm.Usage{PromptTokens: 600, CompletionTokens: 80}}, nil
}

func requestAndAwaitEstimate(t *testing.T, a *testApp, productID int64, at geo.Point) estimateJSON {
	t.Helper()
	apiPost(t, a.server.URL, "/api/v1/estimates", fmt.Sprintf(`{"product_id": %d, "lat": %f, "lng": %f, "radius_m": 3000}`, productID, at.Lat, at.Lng), nil)
	var e estimateJSON
	waitFor(t, "the estimate", 20*time.Second, func() bool {
		e = estimateJSON{}
		apiGet(t, a.server.URL, fmt.Sprintf("/api/v1/estimates?product_id=%d&radius_m=3000&lat=%f&lng=%f", productID, at.Lat, at.Lng), &e)
		return e.State != "pending"
	})
	return e
}

// TestLLMAssistedEstimate: with an LLM configured, a validated answer is
// used (method llm_assisted) and its confidence cannot exceed what the
// evidence statistics support.
func TestLLMAssistedEstimate(t *testing.T) {
	t.Parallel()
	fake := &scriptedLLM{}
	a := startApp(t, newDB(t, true), map[string]string{"LLM_ENABLED": "true", "LLM_BASE_URL": "http://llm.invalid/v1", "LLM_MODEL": "scripted-test-model"}, true, fake)
	seedDemo(t, a)
	e := requestAndAwaitEstimate(t, a, demoProductID(t, a, "Miniket Rice 5 kg"), chattogram(t, a))
	if e.State != "ready" || e.Method != "llm_assisted" || e.ModelID != "scripted-test-model" || e.FallbackReason != "" {
		t.Fatalf("estimate = %+v", e)
	}
	if e.ExplanationEn != "Based on the nearby shop prices returned by the tools." || len(e.Evidence.Cited) == 0 {
		t.Errorf("LLM explanation or citations missing: %+v", e)
	}
	// Every cited ID is real evidence gathered in this run.
	have := map[string]bool{}
	for _, it := range e.Evidence.Items {
		have[it.ID] = true
	}
	for _, id := range e.Evidence.Cited {
		if !have[id] {
			t.Errorf("cited evidence %s is not in the gathered evidence", id)
		}
	}
	// The model said "high"; the server caps it at the statistical confidence.
	if e.Confidence == "" || (e.Confidence == "high" && e.Evidence.Counts["local_listing"] < 5) {
		t.Errorf("confidence %q not capped by the evidence (%v)", e.Confidence, e.Evidence.Counts)
	}
	if fake.calls != 2 {
		t.Errorf("LLM calls = %d, want 2 (one tool call, one answer)", fake.calls)
	}
}

// TestLLMHallucinationFallsBackToBaseline: an answer citing evidence that was
// never retrieved is rejected, retried once with the error fed back, and then
// the statistical baseline is used and the reason recorded.
func TestLLMHallucinationFallsBackToBaseline(t *testing.T) {
	t.Parallel()
	fake := &scriptedLLM{hallucinate: true}
	a := startApp(t, newDB(t, true), map[string]string{"LLM_ENABLED": "true", "LLM_BASE_URL": "http://llm.invalid/v1", "LLM_MODEL": "scripted-test-model"}, true, fake)
	seedDemo(t, a)
	e := requestAndAwaitEstimate(t, a, demoProductID(t, a, "Miniket Rice 5 kg"), chattogram(t, a))
	if e.State != "ready" || e.Method != "statistical" || e.ModelID != "" {
		t.Fatalf("estimate = %+v, want the statistical fallback", e)
	}
	if !strings.Contains(e.FallbackReason, "L424242") || !strings.Contains(e.FallbackReason, "failed validation twice") {
		t.Errorf("fallback reason = %q", e.FallbackReason)
	}
	if len(e.Evidence.Cited) != 0 || !strings.Contains(e.ExplanationEn, "Weighted median") {
		t.Errorf("fallback kept LLM output: %+v", e)
	}
	if fake.calls != 3 {
		t.Errorf("LLM calls = %d, want 3 (tool call, rejected answer, one retry)", fake.calls)
	}
}

// TestReadinessTracksSchemaAndDatabase: /livez never touches the database;
// /readyz needs it reachable and migrated.
func TestReadinessTracksSchemaAndDatabase(t *testing.T) {
	t.Parallel()
	dbURL := newDB(t, false) // empty: not migrated
	a := &testApp{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("starting on an unmigrated database panicked: %v", r)
			}
		}()
		// startApp syncs sources, which needs the schema; build by hand.
		a = startUnmigrated(t, dbURL)
	}()
	b := newBrowser(t, a.server.URL)
	if code, body, _ := b.get("/livez"); code != 200 || body != "ok\n" {
		t.Errorf("livez on an unmigrated database = %d %q", code, body)
	}
	if code, body, _ := b.get("/readyz"); code != 503 || !strings.Contains(body, "schema version 0 is older than expected") {
		t.Errorf("readyz on an unmigrated database = %d %q", code, body)
	}
	if _, err := migrations.Up(context.Background(), dbURL); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := b.get("/readyz"); code != 200 || body != "ready\n" {
		t.Errorf("readyz after migrating = %d %q", code, body)
	}
	// A newer schema than this binary expects is still ready (expand/contract:
	// the previous release keeps serving during a rollout).
	if _, err := a.rt.Store.Pool.Exec(context.Background(), `INSERT INTO goose_db_version (version_id, is_applied) VALUES (99999, true)`); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := b.get("/readyz"); code != 200 {
		t.Errorf("readyz with a newer schema = %d, want 200", code)
	}
	a.ready.BeginShutdown()
	if code, body, _ := b.get("/readyz"); code != 503 || !strings.Contains(body, "shutting down") {
		t.Errorf("readyz while shutting down = %d %q", code, body)
	}
	if code, _, _ := b.get("/livez"); code != 200 {
		t.Errorf("livez while shutting down = %d", code)
	}
	// With the database gone: still alive, not ready.
	a.rt.Store.Pool.Close()
	fresh := newBrowser(t, a.server.URL)
	if code, _, _ := fresh.get("/livez"); code != 200 {
		t.Errorf("livez without a database = %d: a database outage must not fail liveness", code)
	}
}

// TestHTMLFlowEndToEnd drives the M2/M5 acceptance flow through the HTML
// endpoints with a cookie jar, as a browser without JavaScript would:
// vendor sign-up -> shop -> admin verify -> listing -> an anonymous shopper
// finds it within the radius and not outside it.
func TestHTMLFlowEndToEnd(t *testing.T) {
	t.Parallel()
	a := startApp(t, newDB(t, true), nil, false, nil)
	ctx := context.Background()
	w := newWorld(t, a.rt.Store) // creates admin@example.test and vendor@example.test
	rice := w.product(t, "Kalijira Rice 1 kg", "কালিজিরা চাল ১ কেজি", "1 kg", "kalojira")
	shopper := newBrowser(t, a.server.URL)
	vendorB := newBrowser(t, a.server.URL)
	adminB := newBrowser(t, a.server.URL)

	// Home page: Bangla by default, security headers on every response.
	req, _ := http.NewRequest(http.MethodGet, a.server.URL+"/", nil)
	req.Header.Set("Accept-Language", "bn")
	code, body, hdr := shopper.do(req)
	if code != 200 || !strings.Contains(body, `<html lang="bn">`) || !strings.Contains(body, "সেরা দাম খুঁজুন") {
		t.Fatalf("home page = %d", code)
	}
	csp := hdr.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") || hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("X-Request-ID") == "" {
		t.Errorf("security headers missing: CSP=%q", csp)
	}
	if !strings.Contains(csp, "https://tile.openstreetmap.org") || !strings.Contains(body, "OpenStreetMap contributors") {
		t.Error("map tile origin not allowed by the CSP or OSM attribution missing")
	}
	if code, body, _ := shopper.get("/"); code != 200 || !strings.Contains(body, "Find the best price") {
		t.Errorf("English home page = %d", code)
	}

	// The dashboard needs a session.
	if code, _, hdr := vendorB.get("/vendor"); code != http.StatusSeeOther || hdr.Get("Location") != "/vendor/login?next=%2Fvendor" {
		t.Errorf("anonymous /vendor = %d -> %q", code, hdr.Get("Location"))
	}
	// Sign-up validation, then success: the session cookie is HttpOnly + Lax.
	code, body, _ = vendorB.post("/vendor/signup", url.Values{"name": {"Karim"}, "email": {"karim@example.test"}, "password": {"short"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "at least 10 characters") {
		t.Errorf("weak password sign-up = %d", code)
	}
	code, _, hdr = vendorB.post("/vendor/signup", url.Values{"name": {"Karim"}, "email": {"karim@example.test"}, "password": {testPassword}})
	if code != http.StatusSeeOther || hdr.Get("Location") != "/vendor" {
		t.Fatalf("sign-up = %d -> %q", code, hdr.Get("Location"))
	}
	cookie := hdr.Get("Set-Cookie")
	if !strings.Contains(cookie, "pk_session=") || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") {
		t.Errorf("session cookie = %q", cookie)
	}
	if code, body, _ := vendorB.get("/vendor"); code != 200 || !strings.Contains(body, "karim@example.test") || !strings.Contains(body, "You have not added a shop yet") {
		t.Errorf("dashboard after sign-up = %d", code)
	}

	// Cross-origin form posts are refused (CSRF protection).
	evil, _ := http.NewRequest(http.MethodPost, a.server.URL+"/vendor/shops/new", strings.NewReader("name=Evil"))
	evil.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	evil.Header.Set("Origin", "https://evil.example")
	if code, _, _ := vendorB.do(evil); code != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d, want 403", code)
	}

	// Create a shop 1 km north of the Chattogram centroid, with a pin.
	at := w.north(1000)
	var upazila int64
	if err := a.rt.Store.Pool.QueryRow(ctx, `SELECT id FROM areas WHERE kind = 'upazila' AND parent_id = $1 ORDER BY id LIMIT 1`, w.areaID).Scan(&upazila); err != nil {
		t.Fatal(err)
	}
	shopForm := url.Values{"name": {"Karim General Store"}, "description": {"Family grocery store since 2009: rice, lentils, oil, spices and toiletries."}, "address": {"45 Market Road"}, "area": {fmt.Sprint(upazila)}, "lat": {fmt.Sprintf("%.6f", at.Lat)}, "lng": {fmt.Sprintf("%.6f", at.Lng)}, "phone": {"01700-000000"}}
	if code, body, _ := vendorB.get("/vendor/shops/new"); code != 200 || !strings.Contains(body, `data-lat-input="lat"`) || !strings.Contains(body, `name="lat"`) {
		t.Errorf("shop form = %d (it must offer a map pin and plain lat/lng fields)", code)
	}
	noPin := url.Values{"name": {"X"}, "description": {"A shop description long enough."}, "address": {"Y"}, "area": {fmt.Sprint(upazila)}}
	if code, body, _ := vendorB.post("/vendor/shops/new", noPin); code != http.StatusUnprocessableEntity || !strings.Contains(body, "Drop a pin on the map") {
		t.Errorf("shop without a pin = %d", code)
	}
	code, _, hdr = vendorB.post("/vendor/shops/new", shopForm)
	if code != http.StatusSeeOther || !strings.HasPrefix(hdr.Get("Location"), "/vendor/shops/") {
		t.Fatalf("create shop = %d -> %q", code, hdr.Get("Location"))
	}
	shopPath := strings.Split(hdr.Get("Location"), "?")[0]
	shopID := strings.TrimPrefix(shopPath, "/vendor/shops/")
	if code, body, _ := vendorB.get(hdr.Get("Location")); code != 200 || !strings.Contains(body, "Shop saved.") || !strings.Contains(body, "Pending review") {
		t.Errorf("shop page after creation = %d", code)
	}

	// Find the product (works without JavaScript) and set a price.
	if code, body, _ := vendorB.get(shopPath + "?pq=kalijira"); code != 200 || !strings.Contains(body, "Kalijira Rice 1 kg") {
		t.Errorf("product picker (no JS) = %d", code)
	}
	req, _ = http.NewRequest(http.MethodGet, a.server.URL+shopPath+"/products?pq=kalojira", nil)
	req.Header.Set("HX-Request", "true")
	if code, body, _ := vendorB.do(req); code != 200 || !strings.Contains(body, "Kalijira Rice 1 kg") || strings.Contains(body, "<html") {
		t.Errorf("product picker fragment = %d", code)
	}
	code, _, hdr = vendorB.post(shopPath+"/listings", url.Values{"product": {fmt.Sprint(rice)}, "price": {"৳ ১৩৫.৫০"}, "in_stock": {"1"}})
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "flash.listing_saved") {
		t.Fatalf("set price = %d -> %q", code, hdr.Get("Location"))
	}
	if code, _, hdr := vendorB.post(shopPath+"/listings", url.Values{"product": {fmt.Sprint(rice)}, "price": {"free"}}); code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "flash.err.price") {
		t.Errorf("bad price = %d -> %q", code, hdr.Get("Location"))
	}

	// The shop is pending: the shopper cannot find it or open its page.
	search := func(b *browser, radius int, at geo.Point) string {
		t.Helper()
		code, body, _ := b.get(fmt.Sprintf("/search?q=kalijira+rice&mode=local&radius=%d&lat=%f&lng=%f", radius, at.Lat, at.Lng))
		if code != 200 {
			t.Fatalf("search = %d", code)
		}
		return body
	}
	if strings.Contains(search(shopper, 3000, w.center), "Karim General Store") {
		t.Error("a pending shop is visible in search")
	}
	if code, _, _ := shopper.get("/shops/" + shopID); code != 404 {
		t.Errorf("pending shop's public page = %d for an anonymous shopper", code)
	}
	if code, _, _ := vendorB.get("/shops/" + shopID); code != 200 {
		t.Errorf("owner cannot preview their pending shop: %d", code)
	}

	// A vendor is not an admin.
	if code, _, _ := vendorB.get("/admin"); code != http.StatusForbidden {
		t.Errorf("vendor on /admin = %d, want 403", code)
	}
	if code, _, _ := vendorB.post("/admin/shops/"+shopID+"/status", url.Values{"status": {"verified"}}); code != http.StatusForbidden {
		t.Errorf("vendor verifying a shop = %d, want 403", code)
	}
	// The admin logs in, sees the queue and verifies the shop.
	if code, body, _ := adminB.post("/vendor/login", url.Values{"email": {"admin@example.test"}, "password": {"wrong password"}}); code != http.StatusUnauthorized || !strings.Contains(body, "Wrong email or password.") {
		t.Errorf("bad login = %d", code)
	}
	if code, _, hdr := adminB.post("/vendor/login", url.Values{"email": {"admin@example.test"}, "password": {testPassword}, "next": {"/admin"}}); code != http.StatusSeeOther || hdr.Get("Location") != "/admin" {
		t.Fatalf("admin login = %d -> %q", code, hdr.Get("Location"))
	}
	if code, body, _ := adminB.get("/admin"); code != 200 || !strings.Contains(body, "Karim General Store") || !strings.Contains(body, "karim@example.test") {
		t.Errorf("admin queue = %d", code)
	}
	if code, _, _ := adminB.post("/admin/shops/"+shopID+"/status", url.Values{"status": {"verified"}}); code != http.StatusSeeOther {
		t.Fatalf("verify shop = %d", code)
	}

	// Now the anonymous shopper finds it within the radius...
	body = search(shopper, 3000, w.center)
	if !strings.Contains(body, "Karim General Store") || !strings.Contains(body, "৳135.50") || !strings.Contains(body, "45 Market Road") || !strings.Contains(body, "Directions") {
		t.Errorf("verified shop's listing not shown within the radius")
	}
	if !strings.Contains(body, "Shops within 3.0 km of your location") || !strings.Contains(body, "1 shops") {
		t.Errorf("result scope summary missing")
	}
	// ...and not outside it (the shop is 1 km away; search 500 m, and from
	// a point 10 km away).
	if strings.Contains(search(shopper, 500, w.center), "Karim General Store") {
		t.Error("shop 1 km away found with a 500 m radius")
	}
	if strings.Contains(search(shopper, 3000, w.north(10_000)), "Karim General Store") {
		t.Error("shop found from 9 km away with a 3 km radius")
	}
	// Bangla query, Bangla page.
	req, _ = http.NewRequest(http.MethodGet, a.server.URL+"/search?"+url.Values{"q": {"কালিজিরা চাল"}, "mode": {"local"}, "lat": {fmt.Sprint(w.center.Lat)}, "lng": {fmt.Sprint(w.center.Lng)}}.Encode(), nil)
	req.Header.Set("Accept-Language", "bn")
	if code, body, _ := shopper.do(req); code != 200 || !strings.Contains(body, "৳১৩৫.৫০") || !strings.Contains(body, "কালিজিরা চাল ১ কেজি") {
		t.Errorf("Bangla search = %d", code)
	}
	// Searching by district works with no coordinates and no JavaScript.
	if code, body, _ := shopper.get(fmt.Sprintf("/search?q=kalijira&mode=local&district=%d", w.areaID)); code != 200 || !strings.Contains(body, "Karim General Store") || !strings.Contains(body, "Shops across all of Chattogram district") {
		t.Errorf("district search = %d", code)
	}
	// The public shop page.
	code, body, _ = shopper.get("/shops/" + shopID)
	if code != 200 || !strings.Contains(body, "Karim General Store") || !strings.Contains(body, "Kalijira Rice 1 kg") || !strings.Contains(body, `data-map`) || !strings.Contains(body, "openstreetmap.org/directions") {
		t.Errorf("public shop page = %d", code)
	}

	// Reports from the shop page; the vendor's bulk upload; logout.
	code, _, hdr = shopper.post("/report", url.Values{"target": {"shop"}, "id": {shopID}, "shop": {shopID}, "reason": {"closed"}, "note": {"shutter was down"}})
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "flash.report_thanks") {
		t.Errorf("report = %d -> %q", code, hdr.Get("Location"))
	}
	if _, _, hdr := shopper.post("/report", url.Values{"target": {"shop"}, "id": {shopID}, "shop": {shopID}, "reason": {"closed"}}); !strings.Contains(hdr.Get("Location"), "flash.report_duplicate") {
		t.Errorf("duplicate report -> %q", hdr.Get("Location"))
	}
	if code, body, _ := adminB.get("/admin"); code != 200 || !strings.Contains(body, "shutter was down") {
		t.Errorf("admin does not see the report: %d", code)
	}
	if code, body, _ := vendorB.get("/vendor/catalog.csv"); code != 200 || !strings.HasPrefix(body, "product_id,name_en,name_bn,brand,size,category\n") || !strings.Contains(body, "Kalijira Rice 1 kg") {
		t.Errorf("catalog CSV = %d", code)
	}
	code, body = uploadCSV(t, vendorB, shopPath+"/bulk", fmt.Sprintf("product_id,price\n%d,140\n424242,10\n", rice))
	if code != 200 || !strings.Contains(body, "1 prices saved.") || !strings.Contains(body, "No product with this ID") {
		t.Errorf("bulk upload = %d", code)
	}
	if !strings.Contains(search(shopper, 3000, w.center), "৳140") {
		t.Error("bulk-uploaded price not visible to shoppers")
	}
	if code, _, _ := vendorB.post("/vendor/logout", nil); code != http.StatusSeeOther {
		t.Errorf("logout = %d", code)
	}
	if code, _, _ := vendorB.get("/vendor"); code != http.StatusSeeOther {
		t.Errorf("dashboard after logout = %d, want a redirect to login", code)
	}

	// JSON API errors use the documented envelope.
	var apiErr struct {
		Error struct{ Code, Message, RequestID string } `json:"error"`
	}
	for path, want := range map[string]string{
		"/api/v1/search":                          "missing_query",
		"/api/v1/search?q=rice&mode=cheap":        "bad_mode",
		"/api/v1/search?q=rice&lat=95&lng=91":     "bad_location",
		"/api/v1/search?q=rice&radius_m=99999999": "bad_parameter",
		"/api/v1/search?product_id=424242":        "product_not_found",
		"/api/v1/search?q=rice&cursor=zzz":        "bad_cursor",
		"/api/v1/shops/424242":                    "shop_not_found",
		"/api/v1/estimates?product_id=1":          "bad_parameter",
		"/api/v1/nope":                            "not_found",
	} {
		code := apiGet(t, a.server.URL, path, &apiErr)
		if code < 400 || apiErr.Error.Code != want {
			t.Errorf("GET %s = %d %q, want error code %q", path, code, apiErr.Error.Code, want)
		}
	}
	var shopJSON struct {
		Name     string `json:"name"`
		Listings []struct {
			NameEn string `json:"name_en"`
		} `json:"listings"`
	}
	if code := apiGet(t, a.server.URL, "/api/v1/shops/"+shopID, &shopJSON); code != 200 || shopJSON.Name != "Karim General Store" || len(shopJSON.Listings) != 1 {
		t.Errorf("shop API = %d %+v", code, shopJSON)
	}
	var products struct {
		Products []struct {
			NameEn string `json:"name_en"`
		} `json:"products"`
	}
	if code := apiGet(t, a.server.URL, "/api/v1/products?q=kalojira", &products); code != 200 || len(products.Products) != 1 {
		t.Errorf("product API (alias search) = %d %+v", code, products)
	}
}

// TestRateLimits: abusive clients get 429 with Retry-After.
func TestRateLimits(t *testing.T) {
	t.Parallel()
	a := startApp(t, newDB(t, true), map[string]string{"RATE_LIMIT_AUTH_PER_MIN": "4", "RATE_LIMIT_SEARCH_PER_MIN": "6"}, false, nil)
	b := newBrowser(t, a.server.URL)
	codes := map[int]int{}
	var retryAfter string
	for range 6 {
		code, _, hdr := b.post("/vendor/login", url.Values{"email": {"nobody@example.test"}, "password": {"whatever password"}})
		codes[code]++
		if code == http.StatusTooManyRequests {
			retryAfter = hdr.Get("Retry-After")
		}
	}
	if codes[http.StatusUnauthorized] != 2 || codes[http.StatusTooManyRequests] != 4 || retryAfter == "" {
		t.Errorf("login attempts by status = %v (Retry-After %q), want 2 allowed (burst) then 429s", codes, retryAfter)
	}
	api := map[int]int{}
	for range 6 {
		api[apiGet(t, a.server.URL, "/api/v1/districts", nil)]++
	}
	if api[200] != 3 || api[http.StatusTooManyRequests] != 3 {
		t.Errorf("API calls by status = %v, want 3 allowed then 429s", api)
	}
	// Health endpoints are never rate limited.
	for range 20 {
		if code, _, _ := b.get("/livez"); code != 200 {
			t.Fatalf("livez rate limited: %d", code)
		}
	}
}
