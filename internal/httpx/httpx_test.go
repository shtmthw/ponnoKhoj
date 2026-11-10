package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"
)

type observed struct {
	route, method string
	status        int
}

type fakeObserver struct {
	mu   sync.Mutex
	seen []observed
}

func (f *fakeObserver) ObserveHTTP(route, method string, status int, _ time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, observed{route, method, status})
}

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestObserveRecordsRouteStatusAndRequestID(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	obs := &fakeObserver{}
	rt := NewRouter()
	rt.HandleFunc("GET /shops/{id}", func(w http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) == "" {
			t.Error("no request ID in handler context")
		}
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "short and stout")
	})
	rt.HandleFunc("GET /plain", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	h := Chain(rt, Observe(testLogger(&logs), obs, noop.NewTracerProvider().Tracer("t"), func(*http.Request) string { return "203.0.113.9" }))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shops/42?lat=22.335109&lng=91.834073", nil))
	if rec.Code != http.StatusTeapot || rec.Header().Get("X-Request-ID") == "" {
		t.Errorf("status %d, request id %q", rec.Code, rec.Header().Get("X-Request-ID"))
	}
	// The route pattern is recorded, never the raw path (bounded cardinality).
	if len(obs.seen) != 1 || obs.seen[0] != (observed{"GET /shops/{id}", "GET", http.StatusTeapot}) {
		t.Errorf("observed = %+v", obs.seen)
	}
	// Coordinates in the query string must never reach the logs.
	line := logs.String()
	if strings.Contains(line, "22.335109") || strings.Contains(line, "91.834073") || strings.Contains(line, "lat=") {
		t.Errorf("log line leaks the query string: %s", line)
	}
	for _, want := range []string{`"route":"GET /shops/{id}"`, `"status":418`, `"path":"/shops/42"`, `"client_ip":"203.0.113.9"`, `"request_id":"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log line lacks %s: %s", want, line)
		}
	}

	// An implicit 200 and an unmatched route.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/plain", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))
	if obs.seen[1].status != 200 || obs.seen[2].route != "unmatched" || obs.seen[2].status != 404 {
		t.Errorf("observed = %+v", obs.seen)
	}

	// A sane inbound request ID is reused; a hostile one is replaced.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plain", nil)
	req.Header.Set("X-Request-ID", "gateway-abc_123")
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-ID") != "gateway-abc_123" {
		t.Errorf("inbound request ID not reused: %q", rec.Header().Get("X-Request-ID"))
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/plain", nil)
	req.Header.Set("X-Request-ID", "evil\"injection}{")
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-ID"); got == "" || strings.ContainsAny(got, "\"{}") {
		t.Errorf("hostile request ID was kept: %q", got)
	}
	if RequestID(context.Background()) != "" {
		t.Error("request ID outside a request")
	}
}

func TestRecover(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	obs := &fakeObserver{}
	rt := NewRouter()
	rt.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	rt.HandleFunc("GET /abort", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	h := Chain(rt, Observe(testLogger(&logs), obs, noop.NewTracerProvider().Tracer("t"), func(*http.Request) string { return "" }), Recover(testLogger(&logs)))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "kaboom") {
		t.Errorf("panic response = %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logs.String(), "kaboom") || !strings.Contains(logs.String(), "panic in handler") {
		t.Error("panic was not logged")
	}
	if obs.seen[0].status != 500 {
		t.Errorf("panic observed as %d", obs.seen[0].status)
	}
	// ErrAbortHandler is net/http's own signal and must propagate.
	defer func() {
		if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // sentinel identity
			t.Errorf("recovered %v, want ErrAbortHandler", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	rec := httptest.NewRecorder()
	SecurityHeaders(SecurityConfig{TileOrigin: "https://tile.openstreetmap.org", HSTS: true})(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "style-src 'self'", "img-src 'self' data: https://tile.openstreetmap.org", "frame-ancestors 'none'", "object-src 'none'", "form-action 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP allows inline or eval: %s", csp)
	}
	h := rec.Header()
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" || h.Get("Referrer-Policy") == "" || !strings.Contains(h.Get("Strict-Transport-Security"), "max-age=31536000") {
		t.Errorf("headers = %v", h)
	}
	rec = httptest.NewRecorder()
	SecurityHeaders(SecurityConfig{})(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Header().Get("Strict-Transport-Security") != "" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "img-src 'self' data:;") {
		t.Errorf("dev headers = %v", rec.Header())
	}
}

func TestCrossOrigin(t *testing.T) {
	t.Parallel()
	h := CrossOrigin()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	do := func(method string, headers map[string]string) int {
		req := httptest.NewRequest(method, "http://ponnokhoj.example/vendor/login", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do(http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site"}); c != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", c)
	}
	if c := do(http.MethodPost, map[string]string{"Origin": "https://evil.example"}); c != http.StatusForbidden {
		t.Errorf("foreign-origin POST = %d, want 403", c)
	}
	if c := do(http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin"}); c != http.StatusOK {
		t.Errorf("same-origin POST = %d", c)
	}
	if c := do(http.MethodPost, map[string]string{"Origin": "http://ponnokhoj.example"}); c != http.StatusOK {
		t.Errorf("same-origin (Origin header) POST = %d", c)
	}
	if c := do(http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site"}); c != http.StatusOK {
		t.Errorf("cross-site GET = %d (safe methods are allowed)", c)
	}
	if c := do(http.MethodPost, nil); c != http.StatusOK {
		t.Errorf("non-browser POST = %d", c)
	}
}

func TestBodyLimitAndTimeout(t *testing.T) {
	t.Parallel()
	read := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, "too big", http.StatusRequestEntityTooLarge)
				return
			}
		}
		_, _ = io.WriteString(w, "ok")
	})
	h := BodyLimit(10)(read)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789ABCDEF")))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789")))
	if rec.Code != http.StatusOK {
		t.Errorf("body at the limit = %d", rec.Code)
	}

	slow := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	rec = httptest.NewRecorder()
	Timeout(30*time.Millisecond)(slow).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "timed out") {
		t.Errorf("timeout response = %d %q", rec.Code, rec.Body.String())
	}
}

func TestWriteJSONAndError(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusCreated, map[string]int{"n": 1})
	if rec.Code != http.StatusCreated || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" || strings.TrimSpace(rec.Body.String()) != `{"n":1}` {
		t.Errorf("WriteJSON = %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusBadRequest, "bad_mode", "nope")
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "bad_mode" || body.Error.Message != "nope" || rec.Code != 400 {
		t.Errorf("WriteError = %d %+v (%v)", rec.Code, body, err)
	}
}

func TestClientIP(t *testing.T) {
	t.Parallel()
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	r := NewClientIPResolver(trusted)
	tests := []struct {
		name, remote string
		xff          []string
		want         string
	}{
		{"direct client, no proxy", "203.0.113.7:4444", nil, "203.0.113.7"},
		{"untrusted peer cannot spoof", "203.0.113.7:4444", []string{"1.2.3.4"}, "203.0.113.7"},
		{"trusted proxy, single hop", "10.1.2.3:80", []string{"198.51.100.5"}, "198.51.100.5"},
		{"trusted proxy, client-supplied prefix ignored", "10.1.2.3:80", []string{"6.6.6.6, 198.51.100.5"}, "198.51.100.5"},
		{"two trusted proxies", "10.1.2.3:80", []string{"198.51.100.5, 10.9.9.9"}, "198.51.100.5"},
		{"multiple header lines", "10.1.2.3:80", []string{"6.6.6.6", "198.51.100.5, 10.9.9.9"}, "198.51.100.5"},
		{"all hops trusted", "10.1.2.3:80", []string{"10.5.5.5"}, "10.1.2.3"},
		{"garbage in chain", "10.1.2.3:80", []string{"198.51.100.5, not-an-ip"}, "10.1.2.3"},
		{"trusted proxy without header", "10.1.2.3:80", nil, "10.1.2.3"},
		{"ipv6 trusted proxy", "[fd00::1]:80", []string{"2001:db8::5"}, "2001:db8::5"},
		{"ipv4-mapped peer", "[::ffff:10.1.2.3]:80", []string{"198.51.100.5"}, "198.51.100.5"},
		{"no port", "203.0.113.7", nil, "203.0.113.7"},
		{"unparsable remote", "pipe", nil, "pipe"},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tc.remote
		for _, v := range tc.xff {
			req.Header.Add("X-Forwarded-For", v)
		}
		if got := r.ClientIP(req); got != tc.want {
			t.Errorf("%s: ClientIP = %q, want %q", tc.name, got, tc.want)
		}
	}
	// With no trusted proxies, X-Forwarded-For is never honored.
	none := NewClientIPResolver(nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.1.2.3:80"
	req.Header.Set("X-Forwarded-For", "198.51.100.5")
	if got := none.ClientIP(req); got != "10.1.2.3" {
		t.Errorf("no trusted proxies: ClientIP = %q", got)
	}
}

func TestRateLimiter(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	l := NewRateLimiter(60) // 1 per second, burst 30
	l.now = func() time.Time { return now }
	allowed := 0
	for range 100 {
		if l.Allow("a") {
			allowed++
		}
	}
	if allowed != 30 {
		t.Errorf("burst allowed %d, want 30", allowed)
	}
	if !l.Allow("b") {
		t.Error("another key was limited by key a")
	}
	now = now.Add(10 * time.Second)
	allowed = 0
	for range 100 {
		if l.Allow("a") {
			allowed++
		}
	}
	if allowed != 10 {
		t.Errorf("after 10 s, allowed %d, want 10", allowed)
	}
	// Idle buckets are swept.
	now = now.Add(10 * time.Minute)
	l.Allow("c")
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n != 1 {
		t.Errorf("%d buckets after sweep, want 1", n)
	}
	if NewRateLimiter(1).burst != 1 {
		t.Error("burst must be at least 1")
	}

	// Middleware: 429 with Retry-After once the bucket is empty.
	lim := NewRateLimiter(2)
	var limited atomic.Int32
	h := lim.Middleware(func(*http.Request) string { return "ip" }, func(w http.ResponseWriter, _ *http.Request) {
		limited.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	codes := []int{}
	var last *httptest.ResponseRecorder
	for range 3 {
		last = httptest.NewRecorder()
		h.ServeHTTP(last, httptest.NewRequest(http.MethodGet, "/", nil))
		codes = append(codes, last.Code)
	}
	if codes[0] != 200 || codes[2] != 429 || limited.Load() == 0 || last.Header().Get("Retry-After") != "30" {
		t.Errorf("codes = %v, Retry-After = %q", codes, last.Header().Get("Retry-After"))
	}
}

func TestHealthHandlers(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	LivezHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rec.Code != 200 || rec.Body.String() != "ok\n" {
		t.Errorf("livez = %d %q", rec.Code, rec.Body.String())
	}

	ready := &Readiness{}
	var dbErr error
	h := ReadyzHandler(ready, func(context.Context) error { return dbErr })
	get := func() (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code, rec.Body.String()
	}
	if code, body := get(); code != 200 || body != "ready\n" {
		t.Errorf("ready = %d %q", code, body)
	}
	dbErr = errors.New("database unreachable")
	if code, body := get(); code != 503 || !strings.Contains(body, "database unreachable") {
		t.Errorf("db down = %d %q", code, body)
	}
	dbErr = nil
	ready.BeginShutdown()
	if code, body := get(); code != 503 || !strings.Contains(body, "shutting down") || !ready.ShuttingDown() {
		t.Errorf("shutting down = %d %q", code, body)
	}
	// The check gets a short deadline.
	slow := ReadyzHandler(&Readiness{}, func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("readiness check has no deadline")
		}
		return nil
	})
	slow.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
}

// TestGracefulShutdown exercises the documented sequence: on cancellation
// readiness fails at once, the server keeps serving during the drain delay,
// and an in-flight request that outlives the drain still completes.
func TestGracefulShutdown(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	ready := &Readiness{}
	started := make(chan struct{})
	release := make(chan struct{})
	rt := NewRouter()
	rt.Handle("GET /readyz", ReadyzHandler(ready, func(context.Context) error { return nil }))
	rt.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "finished")
	})
	rt.HandleFunc("GET /fast", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "fast") })
	metrics := http.NewServeMux()
	metrics.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "# metrics") })

	srv, err := NewServer(context.Background(), testLogger(&logs), ServerConfig{Addr: "127.0.0.1:0", MetricsAddr: "127.0.0.1:0", DrainDelay: 400 * time.Millisecond, ShutdownTimeout: 5 * time.Second}, rt, metrics, ready)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	base := "http://" + srv.Addr()
	client := &http.Client{Timeout: 5 * time.Second}
	get := func(url string) (int, string) {
		resp, err := client.Get(url)
		if err != nil {
			return 0, err.Error()
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, _ := get(base + "/readyz"); code != 200 {
		t.Fatalf("readyz before shutdown = %d", code)
	}
	if code, body := get("http://" + srv.MetricsAddr() + "/metrics"); code != 200 || body != "# metrics" {
		t.Fatalf("metrics listener = %d %q", code, body)
	}
	// The metrics port does not serve the application.
	if code, _ := get("http://" + srv.MetricsAddr() + "/fast"); code != 404 {
		t.Errorf("metrics port served an app route: %d", code)
	}

	slowResult := make(chan string, 1)
	go func() {
		_, body := get(base + "/slow")
		slowResult <- body
	}()
	<-started

	shutdownAt := time.Now()
	cancel() // SIGTERM

	// (1) Readiness fails immediately, (2) while the server still serves.
	deadline := time.Now().Add(300 * time.Millisecond)
	sawUnready := false
	for time.Now().Before(deadline) {
		if code, _ := get(base + "/readyz"); code == 503 {
			sawUnready = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawUnready {
		t.Error("readiness did not start failing during the drain delay")
	}
	if code, body := get(base + "/fast"); code != 200 || body != "fast" {
		t.Errorf("request during the drain delay = %d %q; the server must keep serving while it drains", code, body)
	}

	// (3) The in-flight request outlives the drain delay and still completes.
	time.Sleep(500 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("server exited while a request was in flight: %v", err)
	default:
	}
	close(release)
	if body := <-slowResult; body != "finished" {
		t.Errorf("in-flight request body = %q, want it to complete", body)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
	if elapsed := time.Since(shutdownAt); elapsed < 400*time.Millisecond {
		t.Errorf("shutdown took %v, less than the drain delay", elapsed)
	}
	// After shutdown the port is closed.
	if code, _ := get(base + "/fast"); code != 0 {
		t.Errorf("server still answering after shutdown: %d", code)
	}
}

func TestShutdownDeadlineCutsOffStuckRequests(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	ready := &Readiness{}
	started := make(chan struct{})
	rt := NewRouter()
	rt.HandleFunc("GET /stuck", func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	srv, err := NewServer(context.Background(), testLogger(&logs), ServerConfig{Addr: "127.0.0.1:0", MetricsAddr: "127.0.0.1:0", DrainDelay: 0, ShutdownTimeout: 150 * time.Millisecond}, rt, http.NewServeMux(), ready)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	go func() {
		resp, err := http.Get("http://" + srv.Addr() + "/stuck")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "shutdown") {
			t.Errorf("Run = %v, want a shutdown deadline error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown deadline was not enforced")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("shutdown took %v", time.Since(start))
	}
}

func TestNewServerReportsBindErrors(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	first, err := NewServer(context.Background(), testLogger(&logs), ServerConfig{Addr: "127.0.0.1:0", MetricsAddr: "127.0.0.1:0"}, http.NewServeMux(), http.NewServeMux(), &Readiness{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.mainLn.Close(); _ = first.metricsLn.Close() }()
	if _, err := NewServer(context.Background(), testLogger(&logs), ServerConfig{Addr: first.Addr(), MetricsAddr: "127.0.0.1:0"}, http.NewServeMux(), http.NewServeMux(), &Readiness{}); err == nil {
		t.Error("binding a used main port succeeded")
	}
	if _, err := NewServer(context.Background(), testLogger(&logs), ServerConfig{Addr: "127.0.0.1:0", MetricsAddr: first.MetricsAddr()}, http.NewServeMux(), http.NewServeMux(), &Readiness{}); err == nil {
		t.Error("binding a used metrics port succeeded")
	}
}
