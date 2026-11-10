//go:build e2e

// Package e2e tests a running ponnoKhoj deployment (the kind cluster from
// `make kind-up`) through its public entry point, the Gateway:
//
//	E2E_BASE_URL=http://localhost:8088 E2E_KUBE_CONTEXT=kind-ponnokhoj \
//	  go test -tags e2e -count=1 -v ./test/e2e/
//
// The demo admin password is read from the dev overlay's git-ignored secrets
// file (deploy/k8s/overlays/dev/.env.dev), which kind-up creates.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func baseURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("E2E_BASE_URL")
	if u == "" {
		t.Skip("set E2E_BASE_URL (e.g. http://localhost:8088)")
	}
	return strings.TrimRight(u, "/")
}

func devSecret(t *testing.T, key string) string {
	t.Helper()
	f, err := os.Open("../../deploy/k8s/overlays/dev/.env.dev")
	if err != nil {
		t.Skipf("dev secrets file not found (run make kind-up): %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok && k == key {
			return v
		}
	}
	t.Fatalf("%s not in the dev secrets file", key)
	return ""
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T) *client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: baseURL(t), http: &http.Client{
		Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) do(method, path string, form url.Values) (int, string, http.Header) {
	c.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, body)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Accept-Language", "en")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", c.base)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func (c *client) json(path string, v any) int {
	c.t.Helper()
	code, body, _ := c.do(http.MethodGet, path, nil)
	if v != nil {
		if err := json.Unmarshal([]byte(body), v); err != nil {
			c.t.Fatalf("GET %s: not JSON: %.200s", path, body)
		}
	}
	return code
}

func districtID(t *testing.T, c *client, name string) int64 {
	t.Helper()
	var d struct {
		Districts []struct {
			ID     int64   `json:"id"`
			NameEn string  `json:"name_en"`
			Lat    float64 `json:"lat"`
			Lng    float64 `json:"lng"`
		} `json:"districts"`
	}
	c.json("/api/v1/districts", &d)
	for _, x := range d.Districts {
		if x.NameEn == name {
			return x.ID
		}
	}
	t.Fatalf("district %s not found", name)
	return 0
}

func TestHealth(t *testing.T) {
	c := newClient(t)
	for path, want := range map[string]string{"/livez": "ok\n", "/readyz": "ready\n"} {
		if code, body, _ := c.do(http.MethodGet, path, nil); code != 200 || body != want {
			t.Errorf("%s = %d %q", path, code, body)
		}
	}
	code, body, hdr := c.do(http.MethodGet, "/", nil)
	if code != 200 || !strings.Contains(body, "ponnoKhoj") || !strings.Contains(hdr.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("home = %d", code)
	}
	// The metrics port is not routed through the Gateway.
	if code, _, _ := c.do(http.MethodGet, "/metrics", nil); code != 404 {
		t.Errorf("/metrics through the gateway = %d, want 404", code)
	}
}

func TestSeededLocalSearch(t *testing.T) {
	c := newClient(t)
	ctg := districtID(t, c, "Chattogram")
	var district, near, tiny struct {
		Local struct {
			Total int64 `json:"total"`
			Scope struct {
				Kind string `json:"kind"`
			} `json:"scope"`
		} `json:"local"`
	}
	c.json(fmt.Sprintf("/api/v1/search?q=miniket+rice+5kg&mode=local&district_id=%d", ctg), &district)
	if district.Local.Total < 5 || district.Local.Scope.Kind != "district" {
		t.Errorf("district-wide demo search = %+v", district.Local)
	}
	// Exact location: the demo shops cluster around the district centroid.
	c.json("/api/v1/search?q=miniket+rice+5kg&mode=local&lat=22.335109&lng=91.834073&radius_m=3000", &near)
	c.json("/api/v1/search?q=miniket+rice+5kg&mode=local&lat=22.335109&lng=91.834073&radius_m=100", &tiny)
	if near.Local.Total == 0 || near.Local.Scope.Kind != "point" || tiny.Local.Total >= near.Local.Total {
		t.Errorf("radius search: 3 km = %d, 100 m = %d", near.Local.Total, tiny.Local.Total)
	}
}

// TestVendorToShopperFlow is the M2 acceptance flow through the HTML
// endpoints: sign-up -> shop -> admin verify -> listing -> an anonymous
// shopper finds it within the radius and not outside it.
func TestVendorToShopperFlow(t *testing.T) {
	vendor, admin, shopper := newClient(t), newClient(t), newClient(t)
	stamp := time.Now().UnixNano()
	email := fmt.Sprintf("e2e-%d@example.test", stamp)
	if code, _, _ := vendor.do(http.MethodPost, "/vendor/signup", url.Values{"name": {"E2E Vendor"}, "email": {email}, "password": {"e2e-password-123"}}); code != http.StatusSeeOther {
		t.Fatalf("sign-up = %d", code)
	}
	ctg := districtID(t, vendor, "Chattogram")
	// A spot ~30 km from the demo shops, so only this shop is near it.
	lat, lng := 22.60, 91.70
	shopName := fmt.Sprintf("E2E Store %d", stamp)
	code, _, hdr := vendor.do(http.MethodPost, "/vendor/shops/new", url.Values{
		"name": {shopName}, "description": {"End-to-end test shop: groceries and household items."},
		"address": {"1 Test Road"}, "area": {fmt.Sprint(ctg)}, "lat": {fmt.Sprint(lat)}, "lng": {fmt.Sprint(lng)},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("create shop = %d", code)
	}
	shopPath := strings.Split(hdr.Get("Location"), "?")[0]
	shopID := strings.TrimPrefix(shopPath, "/vendor/shops/")
	var products struct {
		Products []struct {
			ID int64 `json:"id"`
		} `json:"products"`
	}
	vendor.json("/api/v1/products?q=sugar+1kg", &products)
	if len(products.Products) == 0 {
		t.Fatal("demo catalog has no sugar")
	}
	if code, _, _ := vendor.do(http.MethodPost, shopPath+"/listings", url.Values{"product": {fmt.Sprint(products.Products[0].ID)}, "price": {"129.50"}, "in_stock": {"1"}}); code != http.StatusSeeOther {
		t.Fatalf("set price = %d", code)
	}
	search := func(radius int) string {
		_, body, _ := shopper.do(http.MethodGet, fmt.Sprintf("/search?q=sugar+1kg&mode=local&where=point&lat=%f&lng=%f&radius=%d", lat, lng, radius), nil)
		return body
	}
	if strings.Contains(search(3000), shopName) {
		t.Fatal("pending shop visible before verification")
	}
	if code, _, _ := admin.do(http.MethodPost, "/vendor/login", url.Values{"email": {"admin@ponnokhoj.test"}, "password": {devSecret(t, "SEED_ADMIN_PASSWORD")}}); code != http.StatusSeeOther {
		t.Fatalf("admin login = %d", code)
	}
	if _, body, _ := admin.do(http.MethodGet, "/admin", nil); !strings.Contains(body, shopName) || !strings.Contains(body, "End-to-end test shop") {
		t.Fatal("pending shop (with its description) not in the admin queue")
	}
	if code, _, _ := admin.do(http.MethodPost, "/admin/shops/"+shopID+"/status", url.Values{"status": {"verified"}}); code != http.StatusSeeOther {
		t.Fatalf("verify = %d", code)
	}
	if body := search(3000); !strings.Contains(body, shopName) || !strings.Contains(body, "৳129.50") {
		t.Error("verified shop not found within the radius")
	}
	_, far, _ := shopper.do(http.MethodGet, "/search?q=sugar+1kg&mode=local&where=point&lat=22.335109&lng=91.834073&radius=3000", nil)
	if strings.Contains(far, shopName) {
		t.Error("shop ~30 km away found within 3 km")
	}
}

// TestEstimateJob: an estimate request is processed by the worker into a
// validated result (LLM-assisted when the model answers, otherwise the
// statistical fallback) or an honest insufficient_data.
func TestEstimateJob(t *testing.T) {
	c := newClient(t)
	ctg := districtID(t, c, "Chattogram")
	var products struct {
		Products []struct {
			ID int64 `json:"id"`
		} `json:"products"`
	}
	c.json("/api/v1/products?q=nazirshail+rice+5kg", &products)
	if len(products.Products) == 0 {
		t.Fatal("no demo product")
	}
	body := fmt.Sprintf(`{"product_id": %d, "district_id": %d}`, products.Products[0].ID, ctg)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, c.base+"/api/v1/estimates", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 202 && resp.StatusCode != 200 {
		t.Fatalf("request estimate = %d", resp.StatusCode)
	}
	var e struct {
		State  string `json:"state"`
		Method string `json:"method"`
		Low    *struct {
			Poisha int64 `json:"poisha"`
		} `json:"low"`
		High *struct {
			Poisha int64 `json:"poisha"`
		} `json:"high"`
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		e.State = ""
		c.json(fmt.Sprintf("/api/v1/estimates?product_id=%d&district_id=%d", products.Products[0].ID, ctg), &e)
		if e.State != "pending" && e.State != "" {
			break
		}
		time.Sleep(3 * time.Second)
	}
	switch e.State {
	case "ready":
		if e.Low == nil || e.High == nil || e.Low.Poisha >= e.High.Poisha || (e.Method != "statistical" && e.Method != "llm_assisted") {
			t.Errorf("ready estimate malformed: %+v", e)
		}
		t.Logf("estimate ready via %s", e.Method)
	case "insufficient_data":
		t.Log("insufficient data (no estimate rather than a guess)")
	default:
		t.Errorf("estimate state = %q", e.State)
	}
}

// TestRollingRestartWithoutErrors restarts the web Deployment while
// requests keep flowing through the Gateway; graceful shutdown (readiness
// fails, drain, then Shutdown) must keep every request successful.
func TestRollingRestartWithoutErrors(t *testing.T) {
	kctx := os.Getenv("E2E_KUBE_CONTEXT")
	if kctx == "" {
		t.Skip("set E2E_KUBE_CONTEXT to test a rolling restart")
	}
	c := newClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	var ok, failed atomic.Int64
	var mu sync.Mutex
	var failures []string
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/search?q=sugar&mode=local&district=16", nil)
				resp, err := c.http.Do(req)
				if ctx.Err() != nil {
					return
				}
				if err != nil || resp.StatusCode != 200 {
					failed.Add(1)
					mu.Lock()
					if len(failures) < 5 {
						failures = append(failures, fmt.Sprint(err, statusOf(resp)))
					}
					mu.Unlock()
				} else {
					ok.Add(1)
				}
				if resp != nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
				time.Sleep(50 * time.Millisecond)
			}
		}()
	}
	run := func(args ...string) {
		out, err := exec.CommandContext(context.Background(), "kubectl", append([]string{"--context", kctx, "-n", "ponnokhoj"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("kubectl %v: %v\n%s", args, err, out)
		}
	}
	time.Sleep(2 * time.Second)
	run("rollout", "restart", "deployment/ponnokhoj-web")
	run("rollout", "status", "deployment/ponnokhoj-web", "--timeout=5m")
	time.Sleep(10 * time.Second) // the old pod drains and exits
	cancel()
	wg.Wait()
	t.Logf("during the rolling restart: %d ok, %d failed", ok.Load(), failed.Load())
	if failed.Load() > 0 {
		t.Errorf("%d requests failed during the rolling restart, e.g. %v", failed.Load(), failures)
	}
	if ok.Load() < 50 {
		t.Errorf("only %d requests completed; the test did not exercise the restart", ok.Load())
	}
}

func statusOf(r *http.Response) string {
	if r == nil {
		return ""
	}
	return r.Status
}
