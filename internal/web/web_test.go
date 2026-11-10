package web

import (
	"bytes"
	"flag"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/auth"
	"github.com/shtmthw/ponnoKhoj/internal/config"
	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/estimate"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/httpx"
	"github.com/shtmthw/ponnoKhoj/internal/pricefinder"
	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/sources"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
	"github.com/shtmthw/ponnoKhoj/internal/vendor"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var fixedNow = time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCatalogsHaveTheSameKeys(t *testing.T) {
	t.Parallel()
	c := testCatalog(t)
	en, bn := c.Keys(LangEn), c.Keys(LangBn)
	sort.Strings(en)
	sort.Strings(bn)
	if strings.Join(en, "\n") != strings.Join(bn, "\n") {
		t.Errorf("the English and Bangla catalogs have different keys")
	}
	verbs := regexp.MustCompile(`%[sdv]`)
	for _, k := range en {
		e, b := c.messages[LangEn][k], c.messages[LangBn][k]
		if strings.TrimSpace(e) == "" || strings.TrimSpace(b) == "" {
			t.Errorf("empty message for %s", k)
		}
		if len(verbs.FindAllString(e, -1)) != len(verbs.FindAllString(b, -1)) {
			t.Errorf("%s: placeholder count differs between languages", k)
		}
	}
}

// TestEveryMessageKeyUsedExists scans templates and handlers for catalog
// keys, so a typo cannot ship as a raw key on a page.
func TestEveryMessageKeyUsedExists(t *testing.T) {
	t.Parallel()
	c := testCatalog(t)
	have := map[string]bool{}
	for _, k := range c.Keys(LangEn) {
		have[k] = true
	}
	literal := regexp.MustCompile(`\.T\(?\s*"([a-z_.0-9]+)"`)
	prefix := regexp.MustCompile(`printf "([a-z_.]+)\.%s"`)
	goKey := regexp.MustCompile(`"((?:flash|verr|error|mode|bulk|auth\.err)\.[a-z_.]+)"`)
	check := func(file, src string) {
		for _, m := range literal.FindAllStringSubmatch(src, -1) {
			if !have[m[1]] {
				t.Errorf("%s uses unknown message key %q", file, m[1])
			}
		}
		for _, m := range goKey.FindAllStringSubmatch(src, -1) {
			if strings.HasSuffix(m[1], ".") || strings.HasSuffix(m[1], ".html") || have[m[1]] {
				continue
			}
			t.Errorf("%s uses unknown message key %q", file, m[1])
		}
		for _, m := range prefix.FindAllStringSubmatch(src, -1) {
			found := false
			for k := range have {
				found = found || strings.HasPrefix(k, m[1]+".")
			}
			if !found {
				t.Errorf("%s builds keys under %q, which has no messages", file, m[1])
			}
		}
	}
	_ = fs.WalkDir(templateFS, "templates", func(p string, d fs.DirEntry, _ error) error {
		if !d.IsDir() {
			raw, _ := fs.ReadFile(templateFS, p)
			check(p, string(raw))
		}
		return nil
	})
	goFiles, _ := filepath.Glob("*.go")
	for _, f := range goFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, _ := os.ReadFile(f)
		check(f, string(raw))
	}
	// Every dynamically built key has a message.
	dynamic := map[string][]string{
		"freshness.":      {"fresh", "stale", "hidden"},
		"confidence.":     {"low", "medium", "high"},
		"method.":         {"statistical", "llm_assisted"},
		"status.":         {"pending", "verified", "suspended"},
		"report.reason.":  {"wrong_price", "closed", "fake"},
		"evidence.type.":  {string(estimate.EvLocal), string(estimate.EvOtherArea), string(estimate.EvHistory), string(estimate.EvOnline), string(estimate.EvComparable)},
		"evidence.ratio.": {"category_local", "global", "none"},
		"bulk.error.":     {vendor.RowBadColumns, vendor.RowBadProduct, vendor.RowBadPrice, vendor.RowBadStock, vendor.RowDuplicate, vendor.RowUnknownProd, vendor.RowNotAllowed},
		"verr.":           {"name.length", "address.length", "area.required", "area.unknown", "location.required", "phone.format", "opening_hours.length", "price.range", "product.unknown", "product.rejected", "brand.length", "category.required", "category.unknown", "size.format"},
	}
	for p, suffixes := range dynamic {
		for _, s := range suffixes {
			if !have[p+s] {
				t.Errorf("missing message %s%s", p, s)
			}
		}
	}
}

func TestLocalizerFormatting(t *testing.T) {
	t.Parallel()
	c := testCatalog(t)
	en, bn := c.For(LangEn), c.For(LangBn)
	en.now, bn.now = func() time.Time { return fixedNow }, func() time.Time { return fixedNow }
	if c.For("fr").Lang != LangBn {
		t.Error("unknown language should fall back to Bangla")
	}
	if en.Other() != LangBn || bn.Other() != LangEn || en.Bangla() || !bn.Bangla() {
		t.Error("language toggle wrong")
	}
	if en.T("no.such.key") != "no.such.key" {
		t.Error("missing key should render as the key")
	}
	if en.Money(125050) != "৳1,250.50" || bn.Money(125050) != "৳১,২৫০.৫০" {
		t.Errorf("money: %s / %s", en.Money(125050), bn.Money(125050))
	}
	if en.Num(1234) != "1234" || bn.Num(1234) != "১২৩৪" {
		t.Error("num wrong")
	}
	for _, tc := range []struct {
		m      float64
		en, bn string
	}{{84, "80 m", "৮০ মি"}, {999, "1000 m", "১০০০ মি"}, {1000, "1.0 km", "১.০ কিমি"}, {2449, "2.4 km", "২.৪ কিমি"}} {
		if en.Dist(tc.m) != tc.en || bn.Dist(tc.m) != tc.bn {
			t.Errorf("Dist(%v) = %q / %q", tc.m, en.Dist(tc.m), bn.Dist(tc.m))
		}
	}
	if got := en.Ago(fixedNow.Add(-20 * time.Minute)); got != "just now" {
		t.Errorf("Ago minutes = %q", got)
	}
	if got := en.Ago(fixedNow.Add(-5 * time.Hour)); got != "5 h ago" {
		t.Errorf("Ago hours = %q", got)
	}
	if got := bn.Ago(fixedNow.Add(-72 * time.Hour)); got != "৩ দিন আগে" {
		t.Errorf("Ago days = %q", got)
	}
	// Dates are shown in Bangladesh time (UTC+6).
	if got := en.Date(fixedNow); got != "2026-11-10 12:00" {
		t.Errorf("Date = %q", got)
	}
	if en.Name("Rice", "চাল") != "Rice" || bn.Name("Rice", "চাল") != "চাল" || bn.Name("Rice", "") != "Rice" || en.Name("", "চাল") != "চাল" {
		t.Error("Name fallback wrong")
	}
	if bn.Qty(domain.Quantity{Value: 5000, Unit: domain.UnitGram}) != "৫ কেজি" {
		t.Error("Qty wrong")
	}
}

func TestLanguageOf(t *testing.T) {
	t.Parallel()
	tests := []struct{ cookie, accept, want string }{
		{"", "", LangBn}, {"", "en-US,en;q=0.9", LangEn}, {"", "bn-BD,bn;q=0.9,en;q=0.8", LangBn},
		{"", "fr-FR, en;q=0.5", LangEn}, {"en", "bn", LangEn}, {"bn", "en", LangBn}, {"xx", "en", LangEn}, {"", "de", LangBn},
	}
	for _, tc := range tests {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.cookie != "" {
			r.AddCookie(&http.Cookie{Name: langCookie, Value: tc.cookie})
		}
		r.Header.Set("Accept-Language", tc.accept)
		if got := languageOf(r); got != tc.want {
			t.Errorf("languageOf(cookie=%q, accept=%q) = %q, want %q", tc.cookie, tc.accept, got, tc.want)
		}
	}
}

func TestSafeNext(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"/vendor": "/vendor", "/search?q=x&mode=all": "/search?q=x&mode=all", "": "/home", "https://evil.example": "/home",
		"//evil.example": "/home", "/\\evil.example": "/home", "javascript:alert(1)": "/home", "/a\r\nSet-Cookie: x": "/home",
	} {
		if got := safeNext(in, "/home"); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCSVSafe(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"Rice": "Rice", "=1+1": "'=1+1", "+cmd": "'+cmd", "-2": "'-2", "@SUM(A1)": "'@SUM(A1)", "": ""} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q", in, got)
		}
	}
}

func testServer(t *testing.T, demo bool) http.Handler {
	t.Helper()
	cfg, err := config.Load(func(k string) (string, bool) {
		v, ok := map[string]string{
			"DATABASE_URL": "postgres://unused", "APP_SECRET": strings.Repeat("s", 32), "COOKIE_SECURE": "false",
			"PUBLIC_BASE_URL": "http://shop.test", "DEMO_SHOP_ENABLED": map[bool]string{true: "true", false: "false"}[demo],
		}[k]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	lim := func() *httpx.RateLimiter { return httpx.NewRateLimiter(1000) }
	authSvc, err := auth.NewService(nil, auth.Config{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Deps{
		Cfg: cfg, Auth: authSvc, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ClientIP: func(*http.Request) string { return "192.0.2.1" },
		Limits:   Limits{Search: lim(), Estimate: lim(), Poll: lim(), Auth: lim(), Report: lim()},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := httpx.NewRouter()
	srv.Register(rt)
	return rt
}

func get(h http.Handler, target string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestStaticAssets(t *testing.T) {
	t.Parallel()
	h := testServer(t, false)
	assets, err := NewAssets()
	if err != nil {
		t.Fatal(err)
	}
	hashed := assets.Path("app.css")
	if !regexp.MustCompile(`^/static/app\.[0-9a-f]{12}\.css$`).MatchString(hashed) {
		t.Fatalf("hashed path = %q", hashed)
	}
	rec := get(h, hashed)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Errorf("hashed asset: %d %v", rec.Code, rec.Header())
	}
	// Plain names work too (Leaflet's CSS references its images relatively)
	// but are cached only briefly.
	rec = get(h, "/static/vendor/leaflet/images/marker-icon.png")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=3600" || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("plain asset: %d %v", rec.Code, rec.Header())
	}
	for _, p := range []string{"/static/nope.css", "/static/app.000000000000.css", "/static/"} {
		if rec := get(h, p); rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	// Path traversal never reaches a file outside the embedded directory.
	if rec := get(h, "/static/../web.go"); rec.Code == 200 {
		t.Error("path traversal out of /static/ served a file")
	}
	// The vendored libraries are present and are the pinned builds.
	for name, marker := range map[string]string{"vendor/htmx/htmx.min.js": `version:"2.0.11"`, "vendor/leaflet/leaflet.js": "Leaflet 1.9.4"} {
		rec := get(h, assets.Path(name))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), marker) {
			t.Errorf("%s is missing or not the pinned version (no %q)", name, marker)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("Path of an unknown asset did not panic")
		}
	}()
	assets.Path("missing.js")
}

func TestOnlyVendoredAndGlueJavaScript(t *testing.T) {
	t.Parallel()
	var js []string
	_ = fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, _ error) error {
		if !d.IsDir() && strings.HasSuffix(p, ".js") {
			js = append(js, p)
		}
		return nil
	})
	sort.Strings(js)
	want := []string{"static/app.js", "static/vendor/htmx/htmx.min.js", "static/vendor/leaflet/leaflet.js"}
	if strings.Join(js, ",") != strings.Join(want, ",") {
		t.Errorf("JavaScript files = %v, want exactly %v (see the language policy in README.md)", js, want)
	}
	// Templates must not contain inline scripts, inline handlers or inline
	// styles: the Content-Security-Policy would block them.
	_ = fs.WalkDir(templateFS, "templates", func(p string, d fs.DirEntry, _ error) error {
		if d.IsDir() {
			return nil
		}
		raw, _ := fs.ReadFile(templateFS, p)
		src := string(raw)
		if regexp.MustCompile(`(?i)<script(?:\s[^>]*)?>\s*[^<\s]`).MatchString(src) || regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(src) || regexp.MustCompile(`(?i)\sstyle\s*=`).MatchString(src) || strings.Contains(src, "<style") {
			t.Errorf("%s contains inline script, handler or style", p)
		}
		return nil
	})
}

// Only real searches spend the search budget: after a visitor is rate
// limited, the home page and the empty search form still load.
func TestSearchLimitSparesTheHomePage(t *testing.T) {
	t.Parallel()
	lim := httpx.NewRateLimiter(1)
	limited := lim.Middleware(func(*http.Request) string { return "192.0.2.1" }, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	h := searchesOnly(limited)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, step := range []struct {
		target string
		want   int
	}{
		{"/?q=rice", http.StatusOK},
		{"/search?q=rice", http.StatusTooManyRequests},
		{"/search?product=3", http.StatusTooManyRequests},
		{"/", http.StatusOK},
		{"/search?mode=local&district=16", http.StatusOK},
	} {
		if rec := get(h, step.target); rec.Code != step.want {
			t.Errorf("GET %s = %d, want %d", step.target, rec.Code, step.want)
		}
	}
}

func TestRobotsLangAndNotFound(t *testing.T) {
	t.Parallel()
	h := testServer(t, false)
	rec := get(h, "/robots.txt")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Disallow: /vendor/") || !strings.Contains(rec.Body.String(), "User-agent: ponnoKhojBot\nAllow: /demo-shop/") {
		t.Errorf("robots.txt = %q", rec.Body.String())
	}
	// /favicon.ico points at the hashed SVG icon, which is served as SVG.
	rec = get(h, "/favicon.ico")
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusMovedPermanently || !strings.HasPrefix(loc, "/static/favicon.") || !strings.HasSuffix(loc, ".svg") {
		t.Errorf("favicon redirect = %d %q", rec.Code, loc)
	} else if icon := get(h, loc); icon.Code != 200 || icon.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("favicon = %d %q", icon.Code, icon.Header().Get("Content-Type"))
	}
	rec = get(h, "/lang?set=en&next=/search%3Fq%3Drice")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/search?q=rice" {
		t.Errorf("lang redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].Name != langCookie || c[0].Value != "en" || !c[0].HttpOnly {
		t.Errorf("lang cookie = %+v", c)
	}
	rec = get(h, "/lang?set=klingon&next=https://evil.example/")
	if rec.Header().Get("Location") != "/" || len(rec.Result().Cookies()) != 0 {
		t.Errorf("bad lang request: %q %v", rec.Header().Get("Location"), rec.Result().Cookies())
	}
	// Unknown pages get a translated 404 in the requested language.
	rec = get(h, "/no/such/page", "Accept-Language", "en")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "We could not find that page.") || !strings.Contains(rec.Body.String(), `<html lang="en">`) {
		t.Errorf("404 page = %d", rec.Code)
	}
	rec = get(h, "/no/such/page")
	if !strings.Contains(rec.Body.String(), "পাতাটি খুঁজে পাওয়া যায়নি") || !strings.Contains(rec.Body.String(), `<html lang="bn">`) {
		t.Error("default language of the 404 page is not Bangla")
	}
	// The demo shop is not served unless enabled.
	if rec := get(h, "/demo-shop/search?q=rice"); rec.Code != 404 {
		t.Errorf("demo shop reachable while disabled: %d", rec.Code)
	}
	// Signed-out visitors are sent to the login page, remembering where to return.
	rec = get(h, "/vendor/shops/7")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/vendor/login?next=%2Fvendor%2Fshops%2F7" {
		t.Errorf("auth redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get(h, "/admin"); rec.Code != http.StatusSeeOther {
		t.Errorf("signed-out /admin = %d", rec.Code)
	}
	// Login and sign-up forms render without a database.
	for path, want := range map[string]string{"/vendor/login": "Log in", "/vendor/signup": "How listing works"} {
		rec := get(h, path, "Accept-Language", "en")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s = %d", path, rec.Code)
		}
	}
}

func TestDemoShopFeedsTheRealParser(t *testing.T) {
	t.Parallel()
	h := testServer(t, true)
	rec := get(h, "/demo-shop/search?q=Miniket+Rice+5+kg")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "This shop is fictional") {
		t.Fatalf("demo shop search = %d", rec.Code)
	}
	base, _ := url.Parse("http://internal:8080/demo-shop/search")
	res, err := sources.ParseStructuredData(rec.Body.Bytes(), base)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]sources.RawOffer{}
	for _, o := range res.Offers {
		byTitle[o.Title] = o
	}
	o, ok := byTitle["Miniket Rice 5kg (মিনিকেট চাল)"]
	if !ok || o.Price != 43800 || !o.InStock {
		t.Fatalf("offers = %+v", res.Offers)
	}
	// Offer links use the public base URL, not the internal fetch address.
	if o.URL != "http://shop.test/demo-shop/p/miniket-rice-5kg" {
		t.Errorf("offer URL = %q", o.URL)
	}
	// The whole catalog: every usable price shape parses, the unusable ones
	// (range, USD, "call for price") are skipped rather than guessed.
	shop := newDemoShop("http://shop.test")
	var buf httptest.ResponseRecorder
	buf.Body = &bytes.Buffer{}
	shop.render(&buf, "all", shop.items)
	all, err := sources.ParseStructuredData(buf.Body.Bytes(), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Offers) != len(shop.items)-3 || all.Skipped != 3 {
		t.Errorf("catalog: %d offers, %d skipped of %d items", len(all.Offers), all.Skipped, len(shop.items))
	}
	if rec := get(h, "/demo-shop/p/miniket-rice-5kg"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "application/ld+json") {
		t.Errorf("demo product page = %d", rec.Code)
	}
	if rec := get(h, "/demo-shop/p/nope"); rec.Code != 404 {
		t.Errorf("unknown demo product = %d", rec.Code)
	}
	if len(shop.match("")) != 0 || len(shop.match("zzzz")) != 0 || len(shop.match("মিনিকেট চাল")) == 0 {
		t.Error("demo shop matching wrong")
	}
}

// ---- golden files ----

var assetHash = regexp.MustCompile(`\.[0-9a-f]{12}\.(css|js)`)

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	got = assetHash.ReplaceAll(got, []byte(".HASH.$1"))
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (run: go test ./internal/web -update): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from its golden file; run `go test ./internal/web -update` and review the diff", name)
	}
}

func renderGolden(t *testing.T, file, lang, title string, data any) []byte {
	t.Helper()
	pages, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	assets, err := NewAssets()
	if err != nil {
		t.Fatal(err)
	}
	l := testCatalog(t).For(lang)
	l.now = func() time.Time { return fixedNow }
	p := &Page{
		L: l, Title: title, Path: "/search?q=rice", Assets: assets,
		TileURL: "https://tile.openstreetmap.org/{z}/{x}/{y}.png", TileAttribution: "© OpenStreetMap contributors",
		DemoMode: true, Data: data,
	}
	if strings.HasPrefix(file, "vendor_") || file == "admin.html" {
		p.User = &auth.User{ID: 1, Email: "vendor@ponnokhoj.test", DisplayName: "Demo Vendor", Role: domain.RoleAdmin}
	}
	var buf bytes.Buffer
	if err := pages[file].ExecuteTemplate(&buf, "layout", p); err != nil {
		t.Fatalf("render %s (%s): %v", file, lang, err)
	}
	out := buf.String()
	// No raw message key may leak into a page.
	if m := regexp.MustCompile(`>\s*((?:[a-z_]+\.)+[a-z_]+)\s*<`).FindStringSubmatch(out); m != nil && !strings.Contains(m[1], "ponnokhoj.test") {
		t.Errorf("%s (%s) shows an untranslated key: %q", file, lang, m[1])
	}
	return buf.Bytes()
}

func searchFixture() SearchVM {
	rice := pricefinder.Product{ID: 1, NameEn: "Miniket Rice 5 kg", NameBn: "মিনিকেট চাল ৫ কেজি", Quantity: domain.Quantity{Value: 5000, Unit: domain.UnitGram}, IsDemo: true}
	form := SearchForm{Q: "miniket rice 5kg", Mode: "all", Where: "point", Lat: "22.335109", Lng: "91.834073", RadiusM: 3000}
	point := &pricefinder.ResolvedPlace{Kind: pricefinder.ScopePoint, Point: &geo.Point{Lat: 22.335109, Lng: 91.834073}}
	dist := 880.0
	vm := SearchVM{
		Form: form, HasQuery: true, MapLat: 22.335109, MapLng: 91.834073, MapZoom: 12, RadiusF: 3000, PointMode: true, Scope: point,
		Modes: []ModeOption{{Value: "all", Label: "mode.all", Active: true}, {Value: "local", Label: "mode.local"}, {Value: "online", Label: "mode.online"}, {Value: "estimate", Label: "mode.estimate"}},
		Radii: []RadiusOption{
			{Value: 1000, Meters: 1000, URL: searchURL(form, map[string]string{"radius": "1000"})},
			{Value: 3000, Meters: 3000, Active: true, URL: searchURL(form, map[string]string{"radius": "3000"})},
			{Value: 10000, Meters: 10000, URL: searchURL(form, map[string]string{"radius": "10000"})},
		},
		Districts:  []db.ListDistrictsRow{{ID: 16, NameEn: "Chattogram", NameBn: "চট্টগ্রাম", Lat: 22.335109, Lng: 91.834073}, {ID: 55, NameEn: "Dhaka", NameBn: "ঢাকা", Lat: 23.7115253, Lng: 90.4111451}},
		Categories: []db.Category{{ID: 2, Slug: "rice-grains", NameEn: "Rice & Grains", NameBn: "চাল ও শস্য"}},
		SelfURL:    searchURL(form, nil),
		NextURL:    searchURL(form, map[string]string{"cursor": "abc"}),
		Markers:    markersJSON([]marker{{Lat: 22.34, Lng: 91.83, Label: `Rahim's "Best" <Store> — ৳405`}}),
		Tabs:       []TabLink{{Label: "mode.all", URL: "/search?mode=all&q=x", Active: true}, {Label: "mode.local", URL: "/search?mode=local&q=x"}},
		ProductLinks: []ProductLink{
			{NameEn: rice.NameEn, NameBn: rice.NameBn, URL: "/search?product=1", Active: true},
			{NameEn: "Miniket Rice 1 kg", NameBn: "মিনিকেট চাল ১ কেজি", URL: "/search?product=2"},
		},
		Resp: pricefinder.Response{
			Query: search.ParseQuery("miniket rice 5kg"), Mode: domain.ModeAll, Products: []pricefinder.Product{rice}, Selected: &rice,
			Local: &pricefinder.LocalResult{Scope: point, RadiusM: 3000, Total: 2, NextCursor: "abc", Listings: []pricefinder.LocalListing{
				{ListingID: 11, Price: 40500, InStock: true, UpdatedAt: fixedNow.Add(-26 * time.Hour), Freshness: domain.Fresh, ShopID: 3, ShopName: `Rahim's "Best" <Store>`, ShopAddress: "12 Station Road <b>bold</b>", Location: geo.Point{Lat: 22.34, Lng: 91.83}, DistanceM: 884, HasDistance: true, AreaEn: "Chattogram", AreaBn: "চট্টগ্রাম", DirectionsURL: geo.DirectionsURL(geo.Point{Lat: 22.34, Lng: 91.83}), IsDemo: true},
				{ListingID: 12, Price: 41250, InStock: false, UnderReview: true, UpdatedAt: fixedNow.Add(-12 * 24 * time.Hour), Freshness: domain.Stale, ShopID: 4, ShopName: "করিম স্টোর", ShopAddress: "স্টেশন রোড", Location: geo.Point{Lat: 22.35, Lng: 91.84}, DistanceM: 2449, HasDistance: true, AreaEn: "Hathazari", AreaBn: "হাটহাজারী", ParentAreaEn: "Chattogram", ParentAreaBn: "চট্টগ্রাম", DirectionsURL: geo.DirectionsURL(geo.Point{Lat: 22.35, Lng: 91.84})},
			}},
			Online: &pricefinder.OnlineResult{SourcesEnabled: 1, Covered: true, Refreshing: true, FailedSources: []string{"demoshop"}, Offers: []pricefinder.Offer{
				{ID: 1, SourceID: "demoshop", SourceName: "ponnoKhoj Demo Shop (fictional)", IsDemo: true, URL: "http://localhost:8080/demo-shop/p/miniket-rice-5kg", Title: "Miniket Rice 5kg (মিনিকেট চাল)", Price: 43800, InStock: true, ObservedAt: fixedNow.Add(-3 * time.Hour)},
			}},
		},
	}
	vm.Estimate = &EstimateVM{
		State: "ready", ProductID: 1, PlaceFields: placeFields(form), Place: point, RadiusM: 3000, RadiusF: 3000, Back: vm.SelfURL, PollURL: "/estimate?product=1",
		View: estimate.View{
			Status: domain.EstimateReady, Low: 38900, Point: 40500, High: 42800, Confidence: domain.ConfidenceMedium, Method: domain.MethodLLMAssisted, ModelID: "demo-model",
			ExplanationEn: "Four nearby shops agree closely. <script>alert(1)</script>", ExplanationBn: "কাছের চারটি দোকানের দাম কাছাকাছি।",
			CreatedAt: fixedNow.Add(-time.Hour),
			Evidence: estimate.StoredEvidence{
				Items: []estimate.Evidence{
					{ID: "L11", Type: estimate.EvLocal, PricePoisha: 40500, AdjustedPoisha: 40500, DistanceM: &dist, AgeDays: 1.1, Weight: 0.87},
					{ID: "O1", Type: estimate.EvOnline, PricePoisha: 43800, AdjustedPoisha: 41610, AgeDays: 0.1, Weight: 0.6},
				},
				Counts: map[string]int{"local_listing": 1, "online_offer": 1}, Ratio: estimate.Ratio{Value: 0.95, Scope: "global", Pairs: 5}, RingM: 3000,
			},
		},
	}
	return vm
}

func TestGoldenSearchPage(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{LangEn, LangBn} {
		out := renderGolden(t, "search.html", lang, "Miniket Rice 5 kg", searchFixture())
		golden(t, "search_results_"+lang, out)
		s := string(out)
		// Vendor-supplied text is escaped everywhere it appears, including
		// inside the JSON handed to the map.
		if strings.Contains(s, "<Store>") || strings.Contains(s, "<b>bold</b>") || strings.Contains(s, "<script>alert(1)</script>") {
			t.Errorf("%s: unescaped user-controlled text in the page", lang)
		}
		if !strings.Contains(s, "Rahim&#39;s &#34;Best&#34; &lt;Store&gt;") {
			t.Errorf("%s: shop name is not HTML-escaped as expected", lang)
		}
		// The tile template reaches Leaflet with its {z}/{x}/{y} placeholders
		// intact: an attribute name containing "url" would make html/template
		// percent-encode them and every tile request would 404.
		if !strings.Contains(s, `data-map-tiles="https://tile.openstreetmap.org/{z}/{x}/{y}.png"`) {
			t.Errorf("%s: map tile URL template is not passed through verbatim", lang)
		}
		// Estimates are always labelled, shown as a range, with the caveat.
		disclaimer := map[string]string{LangEn: "the real price may be higher or lower", LangBn: "প্রকৃত দাম এর চেয়ে বেশি বা কম হতে পারে"}[lang]
		rng := map[string]string{LangEn: "৳389 – ৳428", LangBn: "৳৩৮৯ – ৳৪২৮"}[lang]
		if !strings.Contains(s, disclaimer) || !strings.Contains(s, rng) {
			t.Errorf("%s: estimate lacks the disclaimer or the range", lang)
		}
	}
}

func TestGoldenHomeAndEmptyStates(t *testing.T) {
	t.Parallel()
	home := searchFixture()
	home.HasQuery, home.Form.Q, home.Estimate = false, "", nil
	home.Resp = pricefinder.Response{}
	golden(t, "home_bn", renderGolden(t, "search.html", LangBn, "সেরা দাম খুঁজুন", home))

	// No location, no online sources, estimate needs a location.
	none := searchFixture()
	none.Resp.Local = &pricefinder.LocalResult{NeedsLocation: true, RadiusM: 3000}
	none.Resp.Online = &pricefinder.OnlineResult{SourcesEnabled: 0}
	none.Estimate = &EstimateVM{State: pricefinder.EstimateNeedsLocation, ProductID: 1, RadiusM: 3000, RadiusF: 3000}
	none.Scope = nil
	out := string(renderGolden(t, "search.html", LangEn, "x", none))
	for _, want := range []string{"Choose a district or drop a pin to see prices", "No online shop is connected", "Choose a district or drop a pin to get an estimate"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty-state page lacks %q", want)
		}
	}
	// No product matched at all.
	nomatch := searchFixture()
	nomatch.Resp = pricefinder.Response{}
	nomatch.Estimate = nil
	if out := string(renderGolden(t, "search.html", LangEn, "x", nomatch)); !strings.Contains(out, "No matching product") {
		t.Error("no-match page lacks its message")
	}
}

func TestEstimatePanelStates(t *testing.T) {
	t.Parallel()
	pages, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	l := testCatalog(t).For(LangEn)
	l.now = func() time.Time { return fixedNow }
	render := func(vm *EstimateVM) string {
		var buf bytes.Buffer
		if err := pages["search.html"].ExecuteTemplate(&buf, "estimate", map[string]any{"L": l, "Data": vm}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	base := EstimateVM{ProductID: 1, PlaceFields: map[string]string{"where": "point", "lat": "22.3", "lng": "91.8"}, RadiusM: 3000, RadiusF: 3000, PollURL: "/estimate?product=1&x=1", Back: "/search?q=rice"}

	pending := base
	pending.State = "pending"
	out := render(&pending)
	if !strings.Contains(out, `hx-get="/estimate?product=1&amp;x=1"`) || !strings.Contains(out, `hx-trigger="every 2s"`) || !strings.Contains(out, "Working out an estimate") {
		t.Errorf("pending panel does not poll: %s", out)
	}

	for _, state := range []string{"none", "suggested", "failed"} {
		vm := base
		vm.State = state
		out := render(&vm)
		if strings.Contains(out, "hx-get") || !strings.Contains(out, `action="/estimate"`) || !strings.Contains(out, `name="product" value="1"`) || !strings.Contains(out, "not a quoted price") {
			t.Errorf("%s panel: missing request form or disclaimer, or it polls: %s", state, out)
		}
	}
	suggested := base
	suggested.State = "suggested"
	if !strings.Contains(render(&suggested), "No shop nearby lists this product") {
		t.Error("suggested panel lacks its prompt")
	}

	// Insufficient data: no range, no point value, only the online prices
	// that do exist.
	insufficient := base
	insufficient.State = "insufficient_data"
	insufficient.View = estimate.View{Evidence: estimate.StoredEvidence{Reason: estimate.ReasonTooFew, Items: []estimate.Evidence{{ID: "O1", Type: estimate.EvOnline, PricePoisha: 16400, AdjustedPoisha: 16400}}}}
	out = render(&insufficient)
	if !strings.Contains(out, "no estimate is shown rather than a guess") || !strings.Contains(out, "৳164") || strings.Contains(out, `class="range"`) || strings.Contains(out, "Most likely") {
		t.Errorf("insufficient panel wrong: %s", out)
	}

	// A statistical fallback after a failed LLM refinement says so.
	ready := base
	ready.State = "ready"
	ready.View = estimate.View{Low: 100, Point: 200, High: 300, Confidence: domain.ConfidenceLow, Method: domain.MethodStatistical, FallbackReason: "llm timeout", ExplanationEn: "e", ExplanationBn: "b", CreatedAt: fixedNow}
	out = render(&ready)
	if !strings.Contains(out, "Method: statistical") || !strings.Contains(out, "The AI refinement was not usable") || strings.Contains(out, "llm timeout") {
		t.Errorf("fallback panel wrong: %s", out)
	}
}

func TestGoldenVendorAndAdminPages(t *testing.T) {
	t.Parallel()
	shop := db.GetShopRow{ID: 3, OwnerID: 1, Name: "Rahim Store", Address: "12 Station Road", Status: "pending", Lat: 22.34, Lng: 91.83, AreaEn: "Kotwali", AreaBn: "কোতোয়ালী", ParentAreaEn: "Chattogram", ParentAreaBn: "চট্টগ্রাম", Phone: "+880 1700-000000", OpeningHours: "9–9", IsDemo: true}
	vshop := VendorShopVM{
		Shop: shop, CSVMaxRows: 2000, ProductQuery: "rice",
		Listings: []VendorListingVM{{ID: 11, ProductID: 1, NameEn: "Miniket Rice 5 kg", NameBn: "মিনিকেট চাল ৫ কেজি", ProductStatus: "active", Price: 40500, InStock: true, UnderReview: true, UpdatedAt: fixedNow.Add(-2 * time.Hour), Freshness: domain.Fresh}},
		Products: []pricefinder.Product{{ID: 2, NameEn: "Miniket Rice 1 kg", NameBn: "মিনিকেট চাল ১ কেজি"}},
		Bulk:     &BulkVM{Applied: 3, Flagged: 1, Errors: []vendor.RowError{{Line: 4, Code: vendor.RowBadPrice, Detail: "free"}}},
	}
	golden(t, "vendor_shop_en", renderGolden(t, "vendor_shop.html", LangEn, shop.Name, vshop))
	golden(t, "vendor_shop_bn", renderGolden(t, "vendor_shop.html", LangBn, shop.Name, vshop))

	pub := ShopVM{Shop: shop, DirectionsURL: geo.DirectionsURL(geo.Point{Lat: 22.34, Lng: 91.83}), Markers: markersJSON([]marker{{Lat: 22.34, Lng: 91.83, Label: shop.Name}}),
		Listings: []ShopListingVM{{ID: 11, ProductID: 1, NameEn: "Miniket Rice 5 kg", NameBn: "মিনিকেট চাল ৫ কেজি", Price: 40500, InStock: true, UpdatedAt: fixedNow.Add(-2 * time.Hour), Freshness: domain.Fresh}}}
	golden(t, "shop_bn", renderGolden(t, "shop.html", LangBn, shop.Name, pub))

	seven := int64(7)
	ok := fixedNow.Add(-time.Hour)
	admin := AdminVM{
		PendingShops: []db.ListShopsByStatusRow{{ID: 3, Name: "Rahim Store", Address: "12 Station Road", AreaEn: "Kotwali", AreaBn: "কোতোয়ালী", OwnerEmail: "rahim@example.com"}},
		Reports:      []AdminReportVM{{Target: "listing", TargetID: 11, ShopID: 3, ShopName: "Rahim Store", ProductEn: "Miniket Rice 5 kg", Price: 40500, Reason: "wrong_price", Note: "it was 450 in the shop"}},
		Proposals:    []AdminProposalVM{{ListProposedProductsRow: db.ListProposedProductsRow{ID: 40, NameEn: "Puffed Rice 500 g", NameBn: "মুড়ি ৫০০ গ্রাম", CategoryEn: "Rice & Grains", ProposerEmail: "rahim@example.com"}, Quantity: domain.Quantity{Value: 500, Unit: domain.UnitGram}}},
		Flagged:      []AdminFlaggedVM{{ID: 12, ProductID: 20, NameEn: "Potato 1 kg", NameBn: "আলু ১ কেজি", ShopID: 3, ShopName: "Rahim Store", Price: 12000}},
		Sources:      []db.ListSourcesRow{{ID: "demoshop", Name: "ponnoKhoj Demo Shop (fictional)", ComplianceStatus: "demo", Enabled: true, LastSuccessAt: &ok, LastError: "timeout", ConsecutiveFailures: 2}},
		Audit:        []db.ListAuditRow{{ID: 1, ActorID: &seven, ActorEmail: "admin@ponnokhoj.test", Action: "shop_verified", EntityType: "shop", EntityID: 3, CreatedAt: fixedNow}, {ID: 2, Action: "auto_hide", EntityType: "listing", EntityID: 11, CreatedAt: fixedNow}},
	}
	golden(t, "admin_en", renderGolden(t, "admin.html", LangEn, "Admin review", admin))

	// The remaining pages render in both languages without error.
	for _, lang := range []string{LangEn, LangBn} {
		renderGolden(t, "vendor_dashboard.html", lang, "d", DashboardVM{Shops: []db.ListShopsByOwnerRow{{ID: 3, Name: "Rahim Store", Address: "a", Status: "verified", AutoHidden: true, ListingCount: 4}}})
		renderGolden(t, "vendor_dashboard.html", lang, "d", DashboardVM{})
		renderGolden(t, "vendor_shop_form.html", lang, "f", ShopFormVM{Action: "/vendor/shops/new", Cancel: "/vendor", Error: "bad", AreaGroups: []AreaGroup{{DistrictID: 16, NameEn: "Chattogram", NameBn: "চট্টগ্রাম", Upazilas: []db.ListUpazilasRow{{ID: 9, NameEn: "Hathazari", NameBn: "হাটহাজারী", DistrictID: 16, DistrictEn: "Chattogram", DistrictBn: "চট্টগ্রাম"}}}}, Description: "We sell groceries.", MapLat: 23.7, MapLng: 90.4, MapZoom: 7})
		renderGolden(t, "vendor_product_new.html", lang, "p", ProposeVM{ShopID: 3, Error: "bad", Categories: []db.Category{{ID: 2, NameEn: "Rice", NameBn: "চাল"}}})
		renderGolden(t, "auth.html", lang, "Log in", AuthVM{Action: "/vendor/login", Error: "Wrong email or password."})
		renderGolden(t, "auth.html", lang, "Sign up", AuthVM{SignUp: true, Action: "/vendor/signup"})
		renderGolden(t, "error.html", lang, "Error 404", errorVM{Message: "gone"})
		renderGolden(t, "admin.html", lang, "a", AdminVM{})
		renderGolden(t, "shop.html", lang, "s", ShopVM{Shop: shop})
	}
}
