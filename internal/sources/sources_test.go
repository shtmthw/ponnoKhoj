package sources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/search"
)

func TestParseStructuredDataFixture(t *testing.T) {
	t.Parallel()
	page, err := os.ReadFile("../../test/fixtures/sources/search_itemlist.html")
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://shop.example/search?q=miniket+rice")
	res, err := ParseStructuredData(page, base)
	if err != nil {
		t.Fatal(err)
	}
	want := []RawOffer{
		{Title: "Miniket Rice 5kg (মিনিকেট চাল)", URL: "https://shop.example/p/miniket-rice-5kg", Price: 43800, InStock: true},
		{Title: "Premium Miniket Rice - 1 KG", URL: "https://shop.example/p/miniket-rice-1kg", Price: 8900, InStock: false},
		{Title: "Nazirshail Rice 5 kg", URL: "https://shop.example/p/nazirshail", Price: 49200, InStock: true},
		{Title: "Soybean Oil 5 Ltr", URL: "https://shop.example/p/oil-5l", Price: 92500, InStock: true},
		{Title: "Sugar 1kg", URL: "https://shop.example/p/sugar", Price: 14600, InStock: true},
		{Title: "Mustard Oil 1 L", URL: "https://shop.example/p/mustard?seller=a", Price: 30500, InStock: true},
		{Title: "Mustard Oil 1 L", URL: "https://shop.example/p/mustard?seller=b", Price: 131050, InStock: true},
		{Title: "LED Bulb 12W", URL: "https://shop.example/p/led-bulb", Price: 24000, InStock: true},
		{Title: "Tea 400gm", URL: "https://shop.example/p/tea", Price: 23800, InStock: true},
	}
	if len(res.Offers) != len(want) {
		t.Fatalf("got %d offers, want %d: %+v", len(res.Offers), len(want), res.Offers)
	}
	for i, w := range want {
		if res.Offers[i] != w {
			t.Errorf("offer %d = %+v, want %+v", i, res.Offers[i], w)
		}
	}
	// Skipped: price range, USD, "call for price", no currency, no offer,
	// javascript: URL, and the broken JSON block. The duplicate of the first
	// product (same URL and title) is deduplicated, not skipped.
	if res.Skipped != 7 {
		t.Errorf("skipped = %d, want 7", res.Skipped)
	}
}

func TestParseStructuredDataEdgeCases(t *testing.T) {
	t.Parallel()
	base, _ := url.Parse("https://shop.example/")
	for name, page := range map[string]string{
		"empty":           "",
		"no json-ld":      "<html><body><p>hello</p></body></html>",
		"empty script":    `<script type="application/ld+json"></script>`,
		"scalar":          `<script type="application/ld+json">42</script>`,
		"wrong case type": `<script type="APPLICATION/LD+JSON">{"@type":"Thing"}</script>`,
		"unterminated":    `<script type="application/ld+json">{"@type":"Product"`,
	} {
		res, err := ParseStructuredData([]byte(page), base)
		if err != nil || len(res.Offers) != 0 {
			t.Errorf("%s: offers %v, err %v", name, res.Offers, err)
		}
	}
	// Deep nesting is bounded, not a stack overflow.
	deep := strings.Repeat(`{"a":`, 200) + `{"@type":"Product","name":"Deep","url":"/d","offers":{"price":"5","priceCurrency":"BDT"}}` + strings.Repeat(`}`, 200)
	res, err := ParseStructuredData([]byte(`<script type="application/ld+json">`+deep+`</script>`), base)
	if err != nil || len(res.Offers) != 0 {
		t.Errorf("deep nesting: offers %v, err %v", res.Offers, err)
	}
	// A nil base keeps absolute URLs and rejects relative ones.
	page := `<script type="application/ld+json">[{"@type":"Product","name":"Abs","url":"https://a.example/x","offers":{"price":"5","priceCurrency":"BDT"}},{"@type":"Product","name":"Rel","url":"/x","offers":{"price":"5","priceCurrency":"BDT"}}]</script>`
	res, err = ParseStructuredData([]byte(page), nil)
	if err != nil || len(res.Offers) != 1 || res.Offers[0].Title != "Abs" || res.Skipped != 1 {
		t.Errorf("nil base: %+v, %v", res, err)
	}
	// Very long titles are truncated.
	long := `<script type="application/ld+json">{"@type":"Product","name":"` + strings.Repeat("চ", 500) + `","url":"/x","offers":{"price":"5","priceCurrency":"BDT"}}</script>`
	res, _ = ParseStructuredData([]byte(long), base)
	if len(res.Offers) != 1 || len([]rune(res.Offers[0].Title)) != maxTitleRunes {
		t.Errorf("long title not truncated: %d runes", len([]rune(res.Offers[0].Title)))
	}
}

func FuzzParseStructuredData(f *testing.F) {
	fixture, err := os.ReadFile("../../test/fixtures/sources/search_itemlist.html")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(fixture)
	f.Add([]byte(`<script type="application/ld+json">{"@type":"Product","name":"x","url":"/x","offers":[{"price":1e400,"priceCurrency":"BDT"}]}</script>`))
	f.Add([]byte(`<script type="application/ld+json">[[[[{"@graph":{"@type":["Product"],"offers":{"@type":"AggregateOffer","lowPrice":null}}}]]]]</script>`))
	f.Add([]byte("<script type=application/ld+json>\xff\xfe{</script>"))
	base, _ := url.Parse("https://shop.example/search")
	f.Fuzz(func(t *testing.T, page []byte) {
		res, err := ParseStructuredData(page, base)
		if err != nil {
			return
		}
		if len(res.Offers) > maxOffersPerDoc {
			t.Fatalf("%d offers exceed the cap", len(res.Offers))
		}
		for _, o := range res.Offers {
			if !o.Price.Valid() {
				t.Fatalf("offer with invalid price: %+v", o)
			}
			u, err := url.Parse(o.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				t.Fatalf("offer with unsafe URL: %+v", o)
			}
			if o.Title == "" || len([]rune(o.Title)) > maxTitleRunes {
				t.Fatalf("offer with bad title: %+v", o)
			}
		}
	})
}

func TestBreaker(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	b := NewBreaker(3, time.Minute)
	b.now = func() time.Time { return now }
	for i := range 2 {
		if !b.Allow() {
			t.Fatalf("closed breaker rejected call %d", i)
		}
		b.Record(false)
	}
	if b.Open() {
		t.Fatal("opened before the threshold")
	}
	b.Allow()
	b.Record(false) // third consecutive failure
	if !b.Open() || b.Allow() {
		t.Fatal("breaker should be open and rejecting")
	}
	now = now.Add(59 * time.Second)
	if b.Allow() {
		t.Fatal("allowed during cooldown")
	}
	now = now.Add(2 * time.Second)
	if !b.Allow() {
		t.Fatal("half-open probe not allowed after cooldown")
	}
	if b.Allow() {
		t.Fatal("second concurrent probe allowed")
	}
	b.Record(false) // probe failed: re-open for a full cooldown
	if b.Allow() {
		t.Fatal("allowed right after a failed probe")
	}
	now = now.Add(61 * time.Second)
	if !b.Allow() {
		t.Fatal("probe not allowed after second cooldown")
	}
	b.Record(true)
	if b.Open() || !b.Allow() {
		t.Fatal("successful probe should close the breaker")
	}
	// A success resets the failure streak.
	b.Record(false)
	b.Record(true)
	b.Record(false)
	b.Record(false)
	if b.Open() {
		t.Fatal("non-consecutive failures opened the breaker")
	}
}

// testSite is a fake online shop for fetcher tests.
type testSite struct {
	*httptest.Server
	mu      sync.Mutex
	hits    map[string]int
	agents  []string
	cookies int
	handler func(w http.ResponseWriter, r *http.Request)
	robots  func(w http.ResponseWriter, r *http.Request)
}

func newTestSite(t *testing.T) *testSite {
	t.Helper()
	s := &testSite{hits: map[string]int{}}
	s.robots = func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "User-agent: *\nDisallow: /private/\n")
	}
	s.handler = func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.agents = append(s.agents, r.Header.Get("User-Agent"))
		if len(r.Cookies()) > 0 {
			s.cookies++
		}
		robots, handler := s.robots, s.handler
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "track", Value: "1", Path: "/"})
		if r.URL.Path == "/robots.txt" {
			robots(w, r)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *testSite) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

// fastFetcher is a Fetcher with no real waiting.
func fastFetcher(cfg FetcherConfig) (*Fetcher, *[]time.Duration) {
	cfg.MinInterval = time.Microsecond
	f := NewFetcher(cfg)
	var slept []time.Duration
	var mu sync.Mutex
	f.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
		return nil
	}
	return f, &slept
}

func TestFetcherGetAndIdentity(t *testing.T) {
	t.Parallel()
	site := newTestSite(t)
	f, _ := fastFetcher(FetcherConfig{ContactURL: "https://ponnokhoj.example/about"})
	for range 3 {
		body, err := f.Get(context.Background(), site.URL+"/search?q=rice")
		if err != nil || string(body) != "ok" {
			t.Fatalf("Get = %q, %v", body, err)
		}
	}
	// robots.txt is fetched once and cached.
	if site.count("/robots.txt") != 1 || site.count("/search") != 3 {
		t.Errorf("hits = %v", site.hits)
	}
	wantUA := "ponnoKhojBot/1.0 (+https://ponnokhoj.example/about)"
	for _, ua := range site.agents {
		if ua != wantUA {
			t.Errorf("User-Agent = %q, want %q", ua, wantUA)
		}
	}
	if f.UserAgent() != wantUA {
		t.Errorf("UserAgent() = %q", f.UserAgent())
	}
	// The server set a cookie on every response; none was ever sent back.
	if site.cookies != 0 {
		t.Errorf("%d requests carried cookies", site.cookies)
	}
}

func TestFetcherRobots(t *testing.T) {
	t.Parallel()
	site := newTestSite(t)
	f, _ := fastFetcher(FetcherConfig{})
	if _, err := f.Get(context.Background(), site.URL+"/private/prices"); !errors.Is(err, ErrRobotsDisallowed) {
		t.Errorf("disallowed path err = %v", err)
	}
	if site.count("/private/prices") != 0 {
		t.Error("a disallowed URL was fetched")
	}
	if ErrorClass(ErrRobotsDisallowed) != "robots" {
		t.Error("robots error class")
	}

	// A group naming our bot overrides the wildcard group.
	site2 := newTestSite(t)
	site2.robots = func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "User-agent: ponnoKhojBot\nAllow: /demo-shop/\nDisallow: /\n\nUser-agent: *\nDisallow: /demo-shop/\n")
	}
	f2, _ := fastFetcher(FetcherConfig{})
	if _, err := f2.Get(context.Background(), site2.URL+"/demo-shop/search?q=x"); err != nil {
		t.Errorf("bot-specific allow not honored: %v", err)
	}
	if _, err := f2.Get(context.Background(), site2.URL+"/other"); !errors.Is(err, ErrRobotsDisallowed) {
		t.Errorf("bot-specific disallow not honored: %v", err)
	}

	// robots.txt 404: no restrictions (RFC 9309).
	site3 := newTestSite(t)
	site3.robots = func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }
	f3, _ := fastFetcher(FetcherConfig{})
	if _, err := f3.Get(context.Background(), site3.URL+"/private/x"); err != nil {
		t.Errorf("404 robots should allow: %v", err)
	}

	// robots.txt 5xx: the whole site is off-limits (fail closed).
	site4 := newTestSite(t)
	site4.robots = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }
	f4, _ := fastFetcher(FetcherConfig{})
	if _, err := f4.Get(context.Background(), site4.URL+"/search"); !errors.Is(err, ErrRobotsDisallowed) {
		t.Errorf("5xx robots should disallow: %v", err)
	}
	if site4.count("/search") != 0 {
		t.Error("fetched despite unreachable robots.txt")
	}
}

func TestFetcherRobotsCacheExpiry(t *testing.T) {
	t.Parallel()
	site := newTestSite(t)
	f, _ := fastFetcher(FetcherConfig{RobotsTTL: time.Hour})
	now := time.Now()
	f.now = func() time.Time { return now }
	for range 2 {
		if _, err := f.Get(context.Background(), site.URL+"/a"); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(61 * time.Minute)
	if _, err := f.Get(context.Background(), site.URL+"/a"); err != nil {
		t.Fatal(err)
	}
	if site.count("/robots.txt") != 2 {
		t.Errorf("robots.txt fetched %d times, want 2 (once per TTL)", site.count("/robots.txt"))
	}
}

func TestFetcherRetries(t *testing.T) {
	t.Parallel()
	site := newTestSite(t)
	var calls atomic.Int32
	site.handler = func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			fmt.Fprint(w, "third time lucky")
		}
	}
	f, slept := fastFetcher(FetcherConfig{})
	body, err := f.Get(context.Background(), site.URL+"/flaky")
	if err != nil || string(body) != "third time lucky" {
		t.Fatalf("Get = %q, %v", body, err)
	}
	if len(*slept) != 2 {
		t.Fatalf("slept %v, want two backoffs", *slept)
	}
	// First backoff is jittered around 0.5 s; the second honors Retry-After.
	if (*slept)[0] < 250*time.Millisecond || (*slept)[0] > 750*time.Millisecond {
		t.Errorf("first backoff = %v, want within [250ms, 750ms]", (*slept)[0])
	}
	if (*slept)[1] != 2*time.Second {
		t.Errorf("second wait = %v, want Retry-After 2s", (*slept)[1])
	}
}

func TestFetcherDoesNotRetryClientErrors(t *testing.T) {
	t.Parallel()
	site := newTestSite(t)
	site.handler = func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }
	f, slept := fastFetcher(FetcherConfig{})
	_, err := f.Get(context.Background(), site.URL+"/missing")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 404 || ErrorClass(err) != "http_4xx" || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v", err)
	}
	if site.count("/missing") != 1 || len(*slept) != 0 {
		t.Errorf("a 404 was retried: hits %d, slept %v", site.count("/missing"), *slept)
	}
}

func TestFetcherGivesUp(t *testing.T) {
	t.Parallel()
	site := newTestSite(t)
	site.handler = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	f, slept := fastFetcher(FetcherConfig{MaxAttempts: 3})
	_, err := f.Get(context.Background(), site.URL+"/down")
	if ErrorClass(err) != "http_5xx" || site.count("/down") != 3 || len(*slept) != 2 {
		t.Errorf("err = %v, hits = %d, slept = %v", err, site.count("/down"), *slept)
	}

	// A Retry-After longer than we are willing to wait stops the retries.
	site2 := newTestSite(t)
	site2.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}
	f2, slept2 := fastFetcher(FetcherConfig{})
	_, err = f2.Get(context.Background(), site2.URL+"/busy")
	if ErrorClass(err) != "http_429" || site2.count("/busy") != 1 || len(*slept2) != 0 {
		t.Errorf("long Retry-After: err = %v, hits = %d, slept = %v", err, site2.count("/busy"), *slept2)
	}
}

func TestFetcherBodyCap(t *testing.T) {
	t.Parallel()
	site := newTestSite(t)
	site.handler = func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, strings.Repeat("x", 2000)) }
	f, _ := fastFetcher(FetcherConfig{MaxBodyBytes: 1000})
	_, err := f.Get(context.Background(), site.URL+"/big")
	if !errors.Is(err, ErrBodyTooLarge) || ErrorClass(err) != "too_large" || site.count("/big") != 1 {
		t.Errorf("err = %v, hits = %d", err, site.count("/big"))
	}
	f2, _ := fastFetcher(FetcherConfig{MaxBodyBytes: 2000})
	if body, err := f2.Get(context.Background(), site.URL+"/big"); err != nil || len(body) != 2000 {
		t.Errorf("body at exactly the cap: %d bytes, %v", len(body), err)
	}
}

func TestFetcherCircuitBreaker(t *testing.T) {
	t.Parallel()
	site := newTestSite(t)
	site.handler = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	f, _ := fastFetcher(FetcherConfig{MaxAttempts: 1, BreakerThreshold: 2, BreakerCooldown: time.Hour})
	for range 2 {
		if _, err := f.Get(context.Background(), site.URL+"/x"); ErrorClass(err) != "http_5xx" {
			t.Fatalf("err = %v", err)
		}
	}
	_, err := f.Get(context.Background(), site.URL+"/x")
	if !errors.Is(err, ErrCircuitOpen) || ErrorClass(err) != "circuit_open" {
		t.Errorf("breaker did not open: %v", err)
	}
	if site.count("/x") != 2 {
		t.Errorf("open breaker still sent requests: %d hits", site.count("/x"))
	}
	// robots.txt refusals are not failures and never trip the breaker.
	site2 := newTestSite(t)
	f2, _ := fastFetcher(FetcherConfig{BreakerThreshold: 2})
	for range 5 {
		if _, err := f2.Get(context.Background(), site2.URL+"/private/x"); !errors.Is(err, ErrRobotsDisallowed) {
			t.Fatalf("err = %v", err)
		}
	}
	if f2.breaker.Open() {
		t.Error("robots refusals opened the breaker")
	}
}

func TestFetcherRefusesCrossHostRedirect(t *testing.T) {
	t.Parallel()
	other := newTestSite(t)
	site := newTestSite(t)
	site.handler = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/local":
			http.Redirect(w, r, "/landed", http.StatusFound)
		case "/away":
			http.Redirect(w, r, other.URL+"/stolen", http.StatusFound)
		default:
			fmt.Fprint(w, "landed")
		}
	}
	f, _ := fastFetcher(FetcherConfig{MaxAttempts: 1})
	if body, err := f.Get(context.Background(), site.URL+"/local"); err != nil || string(body) != "landed" {
		t.Errorf("same-host redirect: %q, %v", body, err)
	}
	if _, err := f.Get(context.Background(), site.URL+"/away"); err == nil || !strings.Contains(err.Error(), "cross-host redirect") {
		t.Errorf("cross-host redirect err = %v", err)
	}
	if other.count("/stolen") != 0 {
		t.Error("followed a redirect to another host")
	}
}

func TestFetcherRateLimitAndTimeouts(t *testing.T) {
	t.Parallel()
	site := newTestSite(t)
	f := NewFetcher(FetcherConfig{MinInterval: 120 * time.Millisecond})
	start := time.Now()
	for range 3 { // robots.txt + 3 pages = 4 requests = 3 intervals
		if _, err := f.Get(context.Background(), site.URL+"/page"); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 330*time.Millisecond {
		t.Errorf("4 requests took %v, want >= 3 x 120ms: the per-host limiter is not spacing them", elapsed)
	}

	// A hung server is cut off by the request timeout.
	slow := newTestSite(t)
	slow.handler = func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}
	f2, _ := fastFetcher(FetcherConfig{RequestTimeout: 100 * time.Millisecond, MaxAttempts: 1})
	_, err := f2.Get(context.Background(), slow.URL+"/hang")
	if ErrorClass(err) != "timeout" {
		t.Errorf("hung request err = %v (class %s)", err, ErrorClass(err))
	}

	// A cancelled context stops immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f2.Get(ctx, site.URL+"/page2"); !errors.Is(err, context.Canceled) || ErrorClass(err) != "canceled" {
		t.Errorf("cancelled err = %v", err)
	}
}

func TestFetcherBadURLsAndNetworkErrors(t *testing.T) {
	t.Parallel()
	f, _ := fastFetcher(FetcherConfig{MaxAttempts: 1})
	for _, u := range []string{"", "ftp://example.com/x", "/relative", "http://", "::bad::"} {
		if _, err := f.Get(context.Background(), u); !errors.Is(err, ErrBadURL) || ErrorClass(err) != "bad_url" {
			t.Errorf("Get(%q) err = %v", u, err)
		}
	}
	// Nothing listens here: robots.txt is unreachable, so the site is off-limits.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	if _, err := f.Get(context.Background(), deadURL+"/x"); !errors.Is(err, ErrRobotsDisallowed) {
		t.Errorf("dead host err = %v", err)
	}
	if ErrorClass(nil) != "none" || ErrorClass(errors.New("boom")) != "network" {
		t.Error("ErrorClass defaults wrong")
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tests := map[string]time.Duration{
		"":                              0,
		"5":                             5 * time.Second,
		"0":                             0,
		"-3":                            0,
		"soon":                          0,
		"999999999":                     24 * time.Hour,
		"Tue, 10 Nov 2026 12:00:30 GMT": 30 * time.Second,
		"Tue, 10 Nov 2026 11:00:00 GMT": 0,
	}
	for in, want := range tests {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
	for attempt := 1; attempt <= 10; attempt++ {
		base := 500 * time.Millisecond << min(attempt-1, 6)
		if d := backoff(attempt); d < base/2 || d > base*3/2 {
			t.Errorf("backoff(%d) = %v outside [%v, %v]", attempt, d, base/2, base*3/2)
		}
	}
}

func TestStructuredDataSourceAndRegistry(t *testing.T) {
	t.Parallel()
	fixture, err := os.ReadFile("../../test/fixtures/sources/search_itemlist.html")
	if err != nil {
		t.Fatal(err)
	}
	site := newTestSite(t)
	var gotQuery string
	site.handler = func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		_, _ = w.Write(fixture)
	}
	f, _ := fastFetcher(FetcherConfig{})
	src := NewStructuredDataSource("fixture", site.URL+"/shop/", "/search?q={query}", f)
	res, err := src.Search(context.Background(), search.ParseQuery("Miniket Rice 5 kg"))
	if err != nil {
		t.Fatal(err)
	}
	if src.ID() != "fixture" || gotQuery != "Miniket Rice 5 kg" || site.count("/shop/search") != 1 {
		t.Errorf("id %q, query %q, hits %v", src.ID(), gotQuery, site.hits)
	}
	if len(res.Offers) != 9 || res.Offers[0].URL != site.URL+"/p/miniket-rice-5kg" {
		t.Errorf("offers = %+v", res.Offers)
	}
	// Failures are wrapped with the source ID and keep their class.
	site.handler = func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }
	if _, err := src.Search(context.Background(), search.ParseQuery("x")); err == nil || !strings.Contains(err.Error(), "source fixture") || ErrorClass(err) != "http_4xx" {
		t.Errorf("search error = %v", err)
	}

	// Registry: nothing is enabled unless flagged.
	none, err := NewRegistry(RegistryConfig{Enabled: func(string) bool { return false }}, nil)
	if err != nil || len(none.Enabled()) != 0 || none.IsEnabled(DemoShopID) || len(none.Descriptors()) != len(Catalog) {
		t.Errorf("default registry: %+v, %v", none, err)
	}
	reg, err := NewRegistry(RegistryConfig{
		Enabled: func(id string) bool { return id == DemoShopID }, ContactURL: "https://x.example", DemoShopBaseURL: site.URL + "/demo-shop",
	}, []string{DemoShopID})
	if err != nil || len(reg.Enabled()) != 1 || reg.Enabled()[0].ID() != DemoShopID || reg.BaseURL(DemoShopID) != site.URL+"/demo-shop" {
		t.Fatalf("demo registry: %v", err)
	}
	// A typo in the flag list is an error, not a silently disabled source.
	if _, err := NewRegistry(RegistryConfig{Enabled: func(string) bool { return false }}, []string{"daraz"}); err == nil || !strings.Contains(err.Error(), "unknown source") {
		t.Errorf("unknown source err = %v", err)
	}
	var rows []SourceRow
	if err := reg.Sync(context.Background(), func(_ context.Context, row SourceRow) error { rows = append(rows, row); return nil }); err != nil || len(rows) != len(Catalog) || !rows[0].Enabled {
		t.Errorf("sync rows = %+v, %v", rows, err)
	}
	boom := errors.New("db down")
	if err := reg.Sync(context.Background(), func(context.Context, SourceRow) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("sync error = %v", err)
	}
}

func TestDescriptorCanEnable(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]bool{"approved": true, "demo": true, "pending_review": false, "forbidden": false, "": false} {
		if got := (Descriptor{Compliance: status}).CanEnable(); got != want {
			t.Errorf("CanEnable(%q) = %v", status, got)
		}
	}
	// Every catalogued source must have a valid ID and decision.
	for _, d := range Catalog {
		if d.ID == "" || d.Name == "" || d.AccessMethod == "" || d.Compliance == "" {
			t.Errorf("incomplete catalog entry: %+v", d)
		}
	}
}
