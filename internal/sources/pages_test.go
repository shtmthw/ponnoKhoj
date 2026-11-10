package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../test/fixtures/sources/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseProductPageMicrodata(t *testing.T) {
	t.Parallel()
	page, _ := url.Parse("https://www.startech.com.bd/tp-link-archer-c6-ac1200-gigabit-router")
	res, err := ParseProductPage(readFixture(t, "startech_product_archer_c6.html"), page)
	if err != nil {
		t.Fatal(err)
	}
	want := RawOffer{
		Title: "TP-Link Archer C6 AC1200 Dual Band Gigabit WiFi Router",
		URL:   page.String(), Price: 353000, InStock: true,
	}
	if len(res.Offers) != 1 || res.Offers[0] != want {
		t.Fatalf("offers = %+v, want exactly %+v", res.Offers, want)
	}
}

func TestParseProductPageJSONLD(t *testing.T) {
	t.Parallel()
	page, _ := url.Parse("https://www.techlandbd.com/tp-link-archer-c6-ac1200-gigabit-router")
	res, err := ParseProductPage(readFixture(t, "techland_product_archer_c6.html"), page)
	if err != nil {
		t.Fatal(err)
	}
	want := RawOffer{
		Title: "TP-Link Archer C6 AC1200 Wireless MU-MIMO Gigabit 4 Antenna Router",
		URL:   page.String(), Price: 353000, InStock: true,
	}
	if len(res.Offers) != 1 || res.Offers[0] != want {
		t.Fatalf("offers = %+v, want exactly %+v", res.Offers, want)
	}
}

func TestMicrodataRules(t *testing.T) {
	t.Parallel()
	page, _ := url.Parse("https://shop.example/p/thing")
	html := `<html><body>
	<div itemscope itemtype="https://schema.org/Product">
	  <h1 itemprop="name">  Widget
	    Pro  </h1>
	  <div itemprop="brand" itemscope itemtype="https://schema.org/Brand"><span itemprop="name">Acme</span></div>
	  <div itemprop="offers" itemscope itemtype="https://schema.org/Offer">
	    <meta itemprop="price" content="1250.50"><meta itemprop="priceCurrency" content="BDT">
	    <link itemprop="availability" href="https://schema.org/OutOfStock">
	  </div>
	</div>
	<div itemscope itemtype="https://schema.org/Product"><span itemprop="name">No offer</span></div>
	<div itemscope itemtype="https://schema.org/Product"><span itemprop="name">Dollars</span>
	  <div itemprop="offers" itemscope itemtype="https://schema.org/Offer"><meta itemprop="price" content="12"><meta itemprop="priceCurrency" content="USD"></div></div>
	<div itemscope itemtype="https://schema.org/Question"><span itemprop="name">Is it good?</span></div>
	<span itemprop="name">orphan property outside any item</span>
	</body></html>`
	res, err := ParseProductPage([]byte(html), page)
	if err != nil {
		t.Fatal(err)
	}
	want := RawOffer{Title: "Widget Pro", URL: page.String(), Price: 125050, InStock: false}
	if len(res.Offers) != 1 || res.Offers[0] != want {
		t.Fatalf("offers = %+v, want %+v", res.Offers, want)
	}
	// The offer-less product and the USD product are skipped, not guessed.
	if res.Skipped != 2 {
		t.Errorf("skipped = %d, want 2", res.Skipped)
	}
	// microValue rules for the value-carrying elements.
	items := parseMicrodata([]byte(`<div itemscope itemtype="x/T"><a itemprop="u" href="/a">A</a><img itemprop="i" src="/i.png"><time itemprop="t" datetime="2026-11-10">today</time><data itemprop="d" value="42">x</data></div>`))
	if len(items) != 1 || items[0]["u"] != "/a" || items[0]["i"] != "/i.png" || items[0]["t"] != "2026-11-10" || items[0]["d"] != "42" || items[0]["@type"] != "T" {
		t.Errorf("microdata values = %+v", items)
	}
	// Multiple types and repeated properties.
	items = parseMicrodata([]byte(`<div itemscope itemtype="https://schema.org/Product https://schema.org/Thing"><span itemprop="name">a</span><span itemprop="name">b</span></div>`))
	if types, ok := items[0]["@type"].([]any); !ok || len(types) != 2 || !hasType(items[0], "Product") {
		t.Errorf("multiple types = %+v", items[0]["@type"])
	}
	if names, ok := items[0]["name"].([]any); !ok || len(names) != 2 {
		t.Errorf("repeated property = %+v", items[0]["name"])
	}
	// Pages that are not HTML at all yield nothing.
	if res, err := ParseProductPage([]byte("\x00\x01 not html"), page); err != nil || len(res.Offers) != 0 {
		t.Errorf("garbage page: %+v, %v", res, err)
	}
}

func FuzzParseProductPage(f *testing.F) {
	f.Add(readFixtureF(f, "startech_product_archer_c6.html"))
	f.Add(readFixtureF(f, "techland_product_archer_c6.html"))
	f.Add([]byte(`<div itemscope itemtype="https://schema.org/Product"><span itemprop="name">x</span><div itemprop="offers" itemscope><meta itemprop="price" content="1e999"><meta itemprop="priceCurrency" content="BDT"></div></div>`))
	f.Add([]byte(strings.Repeat(`<div itemscope itemprop="offers">`, 300)))
	page, _ := url.Parse("https://shop.example/p/x")
	f.Fuzz(func(t *testing.T, b []byte) {
		res, err := ParseProductPage(b, page)
		if err != nil {
			return
		}
		for _, o := range res.Offers {
			if !o.Price.Valid() || o.Title == "" || !strings.HasPrefix(o.URL, "http") {
				t.Fatalf("bad offer %+v", o)
			}
		}
	})
}

func readFixtureF(f *testing.F, name string) []byte {
	b, err := os.ReadFile("../../test/fixtures/sources/" + name)
	if err != nil {
		f.Fatal(err)
	}
	return b
}

func TestProductPageSource(t *testing.T) {
	t.Parallel()
	fixture := readFixture(t, "techland_product_archer_c6.html")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nAllow: /\nDisallow: /*?\n"))
		case "/tp-link-archer-c6-ac1200-gigabit-router":
			_, _ = w.Write(fixture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f, _ := fastFetcher(FetcherConfig{MaxAttempts: 1})
	f.client.Transport = srv.Client().Transport // trust the test server's certificate
	src, err := NewProductPageSource("techland", srv.URL, f)
	if err != nil {
		t.Fatal(err)
	}
	if src.ID() != "techland" || !src.Owns(srv.URL+"/x") || src.Owns("https://evil.example/x") || src.Owns("http://"+strings.TrimPrefix(srv.URL, "https://")+"/x") {
		t.Error("Owns is wrong")
	}
	res, err := src.ReadPage(context.Background(), srv.URL+"/tp-link-archer-c6-ac1200-gigabit-router")
	if err != nil || len(res.Offers) != 1 || res.Offers[0].Price != 353000 {
		t.Fatalf("ReadPage = %+v, %v", res, err)
	}
	// Another shop's URL is refused before any request.
	if _, err := src.ReadPage(context.Background(), "https://www.startech.com.bd/x"); !errors.Is(err, ErrBadURL) {
		t.Errorf("foreign URL err = %v", err)
	}
	// robots.txt is honored for product pages too.
	if _, err := src.ReadPage(context.Background(), srv.URL+"/tp-link-archer-c6?ref=1"); !errors.Is(err, ErrRobotsDisallowed) {
		t.Errorf("disallowed URL err = %v", err)
	}
	if _, err := src.ReadPage(context.Background(), srv.URL+"/missing"); ErrorClass(err) != "http_4xx" {
		t.Errorf("missing page err = %v", err)
	}
	if _, err := NewProductPageSource("x", "http://plain.example", f); err == nil {
		t.Error("a non-https shop was accepted")
	}
}

func TestCatalogCompliance(t *testing.T) {
	t.Parallel()
	byID := map[string]Descriptor{}
	for _, d := range Catalog {
		byID[d.ID] = d
	}
	// The review outcomes recorded in docs/sources.md.
	for id, want := range map[string]string{DemoShopID: "demo", StarTechID: "approved", TechlandID: "approved", "ryans": "forbidden", "ultratech": "pending_review"} {
		if byID[id].Compliance != want {
			t.Errorf("%s compliance = %q, want %q", id, byID[id].Compliance, want)
		}
	}
	for _, d := range Catalog {
		if d.ID != DemoShopID && !strings.HasPrefix(d.BaseURL, "https://") {
			t.Errorf("%s has no https base URL", d.ID)
		}
	}
	all := func(string) bool { return true }
	for _, bad := range []string{"ryans", "ultratech"} {
		if _, err := NewRegistry(RegistryConfig{Enabled: all}, []string{bad}); err == nil || !strings.Contains(err.Error(), "cannot be enabled") {
			t.Errorf("enabling %s: err = %v", bad, err)
		}
	}
	reg, err := NewRegistry(RegistryConfig{Enabled: func(id string) bool { return id == StarTechID || id == TechlandID }, ContactURL: "https://x.example"}, []string{StarTechID, TechlandID})
	if err != nil {
		t.Fatal(err)
	}
	enabled := reg.Enabled()
	if len(enabled) != 2 {
		t.Fatalf("enabled = %d sources", len(enabled))
	}
	for _, s := range enabled {
		if _, ok := s.(PageReader); !ok {
			t.Errorf("%s is not a page reader", s.ID())
		}
		if _, ok := s.(Searcher); ok {
			t.Errorf("%s can search, but its robots.txt forbids automated search", s.ID())
		}
	}
	rows := reg.Rows()
	for _, r := range rows {
		if r.ID == StarTechID && (!r.Enabled || r.BaseURL != "https://www.startech.com.bd") {
			t.Errorf("startech row = %+v", r)
		}
		if r.ID == "ryans" && r.Enabled {
			t.Error("a forbidden source is enabled")
		}
	}
	if (Descriptor{Compliance: "approved"}).CanEnable() != true || domain.MaxMoney <= 0 {
		t.Error("sanity")
	}
}
