//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/jobs"
	"github.com/shtmthw/ponnoKhoj/internal/observability"
	"github.com/shtmthw/ponnoKhoj/internal/pricefinder"
	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/seed"
	"github.com/shtmthw/ponnoKhoj/internal/sources"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// upazilaOf returns an upazila of the world's district by name.
func upazilaOf(t *testing.T, w *world, name string) int64 {
	t.Helper()
	rows, err := w.st.ListUpazilasByDistrict(context.Background(), &w.areaID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.NameEn == name {
			return r.ID
		}
	}
	t.Fatalf("upazila %s not found", name)
	return 0
}

// shopIn creates a verified shop registered in an area.
func (w *world) shopIn(t *testing.T, name string, areaID int64, at geo.Point) int64 {
	t.Helper()
	id, err := w.st.CreateShop(context.Background(), db.CreateShopParams{
		OwnerID: w.vendor.ID, Name: name, Description: "Integration test shop selling groceries.", Address: "Test address",
		AreaID: areaID, Lat: at.Lat, Lng: at.Lng, Status: string(domain.ShopVerified),
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestAreaScopedLocalSearch: with only a district, every shop in the
// district (district-level and in any upazila) is listed; with an upazila,
// only that upazila's shops; an exact point keeps the radius search.
func TestAreaScopedLocalSearch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openStore(t, newDB(t, true))
	w := newWorld(t, st)
	rice := w.product(t, "Area Rice 5 kg", "এরিয়া চাল ৫ কেজি", "5 kg")
	hathazari, patiya := upazilaOf(t, w, "Hathazari"), upazilaOf(t, w, "Patiya")
	city := w.shopIn(t, "City shop", w.areaID, w.north(500))
	north := w.shopIn(t, "Hathazari shop", hathazari, w.north(15_000))
	south := w.shopIn(t, "Patiya shop", patiya, w.north(-20_000))
	for shop, price := range map[int64]int64{city: 41000, north: 39000, south: 40000} {
		w.listing(t, shop, rice, price, time.Hour)
	}
	// A shop in another district never shows up.
	dhaka, err := st.GetAreaByName(ctx, db.GetAreaByNameParams{Kind: "district", NameEn: "Dhaka"})
	if err != nil {
		t.Fatal(err)
	}
	w.listing(t, w.shopIn(t, "Dhaka shop", dhaka, geo.Point{Lat: 23.71, Lng: 90.41}), rice, 30000, time.Hour)

	f := finder(st, 20)
	names := func(p *pricefinder.Place) ([]string, *pricefinder.LocalResult) {
		t.Helper()
		resp, err := f.Search(ctx, pricefinder.Request{ProductID: rice, Mode: domain.ModeAll, Place: p, RadiusM: 3000})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, l := range resp.Local.Listings {
			out = append(out, l.ShopName)
			if l.HasDistance != (p.Kind == pricefinder.ScopePoint) {
				t.Errorf("%s: HasDistance = %v for scope %s", l.ShopName, l.HasDistance, p.Kind)
			}
		}
		return out, resp.Local
	}

	got, local := names(pricefinder.AreaPlace(w.areaID))
	if strings.Join(got, "|") != "Hathazari shop|Patiya shop|City shop" || local.Total != 3 {
		t.Errorf("district scope = %v (total %d), want every Chattogram shop, cheapest first", got, local.Total)
	}
	if local.Scope.Kind != pricefinder.ScopeDistrict || local.Scope.AreaEn != "Chattogram" || local.Scope.Point == nil {
		t.Errorf("district scope = %+v", local.Scope)
	}
	got, local = names(pricefinder.AreaPlace(hathazari))
	if strings.Join(got, "|") != "Hathazari shop" || local.Scope.Kind != pricefinder.ScopeUpazila || local.Scope.DistrictEn != "Chattogram" {
		t.Errorf("upazila scope = %v, %+v", got, local.Scope)
	}
	if l := local.Listings[0]; l.AreaEn != "Hathazari" || l.ParentAreaEn != "Chattogram" {
		t.Errorf("listing area = %s, %s", l.AreaEn, l.ParentAreaEn)
	}
	// The upazila's representative point is the mean of its shops (the area
	// dataset has no upazila coordinates).
	if p := local.Scope.Point; p == nil || geo.DistanceMeters(*p, w.north(15_000)) > 10 {
		t.Errorf("upazila point = %v, want the Hathazari shop's position", local.Scope.Point)
	}
	// An upazila with no shops falls back to the district centroid.
	empty := upazilaOf(t, w, "Sandwip")
	_, local = names(pricefinder.AreaPlace(empty))
	if local.Total != 0 || local.Scope.Point == nil || geo.DistanceMeters(*local.Scope.Point, w.center) > 1 {
		t.Errorf("empty upazila = total %d, point %v", local.Total, local.Scope.Point)
	}
	got, _ = names(pricefinder.PointPlace(w.center))
	if strings.Join(got, "|") != "City shop" {
		t.Errorf("3 km around the centre = %v", got)
	}
	if _, err := f.Search(ctx, pricefinder.Request{ProductID: rice, Mode: domain.ModeLocal, Place: pricefinder.AreaPlace(424242)}); err == nil {
		t.Error("an unknown area was accepted")
	}

	// Estimates for area scopes are placed and sized by the area.
	resp, err := f.Search(ctx, pricefinder.Request{ProductID: rice, Mode: domain.ModeEstimate, Place: pricefinder.AreaPlace(w.areaID)})
	if err != nil || resp.Estimate.Place == nil || resp.Estimate.RadiusM != 10_000 || resp.Estimate.Cell != w.center.Cell() {
		t.Errorf("district estimate = %+v, %v", resp.Estimate, err)
	}
	resp, _ = f.Search(ctx, pricefinder.Request{ProductID: rice, Mode: domain.ModeEstimate, Place: pricefinder.AreaPlace(hathazari)})
	if resp.Estimate.RadiusM != 5_000 || resp.Estimate.Cell != w.north(15_000).Cell() {
		t.Errorf("upazila estimate = radius %d, cell %s", resp.Estimate.RadiusM, resp.Estimate.Cell)
	}
}

// TestAreaSearchThroughHTTP drives the same rules through the HTML page,
// the HTMX upazila picker and the JSON API.
func TestAreaSearchThroughHTTP(t *testing.T) {
	t.Parallel()
	a := startApp(t, newDB(t, true), nil, false, nil)
	w := newWorld(t, a.rt.Store)
	rice := w.product(t, "Scope Rice 5 kg", "স্কোপ চাল ৫ কেজি", "5 kg")
	hathazari := upazilaOf(t, w, "Hathazari")
	w.listing(t, w.shopIn(t, "Upazila Store", hathazari, w.north(15_000)), rice, 39000, time.Hour)
	w.listing(t, w.shopIn(t, "City Store", w.areaID, w.north(300)), rice, 41000, time.Hour)
	b := newBrowser(t, a.server.URL)

	_, body, _ := b.get(fmt.Sprintf("/search?q=scope+rice&mode=local&district=%d", w.areaID))
	if !strings.Contains(body, "Shops across all of Chattogram district") || !strings.Contains(body, "Upazila Store") || !strings.Contains(body, "City Store") {
		t.Error("district page does not list the whole district")
	}
	if !strings.Contains(body, fmt.Sprintf(`<option value="%d">Hathazari</option>`, hathazari)) {
		t.Error("district page does not offer the district's upazilas")
	}
	_, body, _ = b.get(fmt.Sprintf("/search?q=scope+rice&mode=local&district=%d&upazila=%d", w.areaID, hathazari))
	if !strings.Contains(body, "Shops in Hathazari upazila, Chattogram") || !strings.Contains(body, "Upazila Store") || strings.Contains(body, "City Store") {
		t.Error("upazila page is not limited to the upazila")
	}
	if !strings.Contains(body, "Show all of Chattogram") {
		t.Error("upazila page has no way back to the whole district")
	}
	// An upazila from another district is ignored rather than mixed in.
	dhaka, _ := a.rt.Store.GetAreaByName(context.Background(), db.GetAreaByNameParams{Kind: "district", NameEn: "Dhaka"})
	_, body, _ = b.get(fmt.Sprintf("/search?q=scope+rice&mode=local&district=%d&upazila=%d", dhaka, hathazari))
	if !strings.Contains(body, "Shops across all of Dhaka district") || strings.Contains(body, "Upazila Store") {
		t.Error("a mismatched upazila was honored")
	}
	// The exact-location mode keeps the radius and offers to change it.
	_, body, _ = b.get(fmt.Sprintf("/search?q=scope+rice&mode=local&where=point&lat=%f&lng=%f&radius=1000", w.center.Lat, w.center.Lng))
	if !strings.Contains(body, "Shops within 1.0 km of your location") || !strings.Contains(body, "City Store") || strings.Contains(body, "Upazila Store") || !strings.Contains(body, "radius=25000") {
		t.Error("point page wrong (scope text, radius filter or radius links)")
	}
	// HTMX upazila options for a district.
	_, body, _ = b.get(fmt.Sprintf("/areas/upazilas?district=%d", w.areaID))
	if !strings.Contains(body, "All of the district") || !strings.Contains(body, ">Hathazari</option>") || strings.Contains(body, "<html") {
		t.Errorf("upazila options fragment = %.200s", body)
	}

	var res searchJSON
	apiGet(t, a.server.URL, fmt.Sprintf("/api/v1/search?product_id=%d&mode=local&upazila_id=%d", rice, hathazari), &res)
	if res.Local.Total != 1 || res.Local.Listings[0].ShopName != "Upazila Store" {
		t.Errorf("API upazila search = %+v", res.Local)
	}
	var scoped struct {
		Local struct {
			Scope    struct{ Kind, AreaEn, DistrictEn string } `json:"scope"`
			Listings []struct {
				DistanceM *float64 `json:"distance_m"`
				Area      string   `json:"area_en"`
			} `json:"listings"`
		} `json:"local"`
	}
	apiGet(t, a.server.URL, fmt.Sprintf("/api/v1/search?product_id=%d&mode=local&district_id=%d", rice, w.areaID), &scoped)
	if scoped.Local.Scope.Kind != "district" || len(scoped.Local.Listings) != 2 || scoped.Local.Listings[0].DistanceM != nil || scoped.Local.Listings[0].Area != "Hathazari" {
		t.Errorf("API district search = %+v", scoped.Local)
	}
	if code := apiGet(t, a.server.URL, fmt.Sprintf("/api/v1/search?product_id=%d&upazila_id=424242", rice), nil); code != 400 {
		t.Errorf("unknown upazila = %d, want 400", code)
	}
	if code := apiPost(t, a.server.URL, "/api/v1/estimates", fmt.Sprintf(`{"product_id": %d, "upazila_id": %d}`, rice, hathazari), nil); code != 202 {
		t.Errorf("estimate by upazila = %d, want 202", code)
	}
}

// fakePageSource stands in for a real shop's product pages.
type fakePageSource struct {
	id    string
	pages map[string]sources.ParseResult
	asked []string
}

func (f *fakePageSource) ID() string { return f.id }
func (f *fakePageSource) Owns(u string) bool {
	return strings.HasPrefix(u, "https://"+f.id+".example/")
}
func (f *fakePageSource) ReadPage(_ context.Context, u string) (sources.ParseResult, error) {
	f.asked = append(f.asked, u)
	if res, ok := f.pages[u]; ok {
		return res, nil
	}
	return sources.ParseResult{}, &sources.StatusError{Code: 404}
}

// TestProductPageRefresh: page readers are asked only about the pages mapped
// to the product, offers are filed under the product only when they match,
// and online search reports whether any enabled shop covers the product.
func TestProductPageRefresh(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openStore(t, newDB(t, true))
	w := newWorld(t, st)
	router := w.product(t, "Archer C6 AC1200 Gigabit Router", "আর্চার C6 রাউটার", "1 pc")
	other := w.product(t, "Unrelated Kettle", "কেটলি", "1 pc")
	for _, id := range []string{"shopa", "shopb"} {
		if err := st.UpsertSource(ctx, db.UpsertSourceParams{ID: id, Name: strings.ToUpper(id), BaseUrl: "https://" + id + ".example", AccessMethod: "structured_data", ComplianceStatus: "approved", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	pageA, pageB := "https://shopa.example/archer-c6", "https://shopb.example/archer-c6"
	for src, u := range map[string]string{"shopa": pageA, "shopb": pageB} {
		if err := st.UpsertProductSourceURL(ctx, db.UpsertProductSourceURLParams{ProductID: router, SourceID: src, Url: u}); err != nil {
			t.Fatal(err)
		}
	}
	a := &fakePageSource{id: "shopa", pages: map[string]sources.ParseResult{pageA: {Offers: []sources.RawOffer{{Title: "TP-Link Archer C6 AC1200 Dual Band Gigabit WiFi Router", URL: pageA, Price: 353000, InStock: true}}}}}
	// Shop B's page now shows another model: the offer must be rejected.
	bb := &fakePageSource{id: "shopb", pages: map[string]sources.ParseResult{pageB: {Offers: []sources.RawOffer{{Title: "TP-Link Archer C64 AC1200 WiFi Router", URL: pageB, Price: 300000, InStock: true}}}}}
	r := jobs.NewRefresher(st, []sources.Source{a, bb}, observability.NewMetrics(), quietLog())
	if err := r.Refresh(ctx, router); err != nil {
		t.Fatal(err)
	}
	if len(a.asked) != 1 || a.asked[0] != pageA || len(bb.asked) != 1 {
		t.Errorf("pages asked: %v %v", a.asked, bb.asked)
	}
	offers, err := st.ListOffersByProduct(ctx, db.ListOffersByProductParams{ProductID: router, ObservedSince: time.Now().Add(-time.Hour), MaxResults: 10})
	if err != nil || len(offers) != 1 || offers[0].SourceID != "shopa" || offers[0].PricePoisha != 353000 {
		t.Fatalf("offers = %+v, %v", offers, err)
	}
	// A product with no mapped pages is not asked about at all.
	a.asked, bb.asked = nil, nil
	if err := r.Refresh(ctx, other); err != nil || len(a.asked)+len(bb.asked) != 0 {
		t.Errorf("unmapped product asked: %v %v (%v)", a.asked, bb.asked, err)
	}

	f := pricefinder.NewService(st, nil, noEnqueue{}, pricefinder.Config{
		DefaultRadiusM: 3000, MaxRadiusM: 25000, Freshness: domain.DefaultFreshnessPolicy(),
		OfferCacheTTL: time.Hour, OfferMaxAge: 24 * time.Hour, SourcesEnabled: 2,
	}, observability.NewMetrics(), quietLog())
	resp, err := f.Search(ctx, pricefinder.Request{ProductID: router, Mode: domain.ModeOnline})
	if err != nil || !resp.Online.Covered || len(resp.Online.Offers) != 1 {
		t.Errorf("router online = %+v, %v", resp.Online, err)
	}
	resp, err = f.Search(ctx, pricefinder.Request{ProductID: other, Mode: domain.ModeOnline})
	if err != nil || resp.Online.Covered || resp.Online.Refreshing {
		t.Errorf("uncovered product online = %+v, %v", resp.Online, err)
	}
}

// TestReferenceCatalogSeed: the real-product catalog loads idempotently with
// its product pages, without touching the sources' enabled flags.
func TestReferenceCatalogSeed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openStore(t, newDB(t, true))
	if err := st.UpsertSource(ctx, db.UpsertSourceParams{ID: sources.StarTechID, Name: "Star Tech", BaseUrl: "https://www.startech.com.bd", AccessMethod: "structured_data", ComplianceStatus: "approved", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	cats, err := seed.Categories(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		n, err := seed.Catalog(ctx, st, cats)
		if err != nil || n != 20 {
			t.Fatalf("catalog seed = %d, %v", n, err)
		}
	}
	var products, pages int
	if err := st.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM products WHERE NOT is_demo AND brand <> ''), (SELECT count(*) FROM product_source_urls)`).Scan(&products, &pages); err != nil {
		t.Fatal(err)
	}
	if products != 20 || pages != 40 {
		t.Errorf("seeded %d products and %d pages, want 20 and 40", products, pages)
	}
	srcs, _ := st.ListSources(ctx)
	for _, s := range srcs {
		if s.ID == sources.StarTechID && !s.Enabled {
			t.Error("seeding disabled an enabled source")
		}
		if s.ID == sources.TechlandID && s.Enabled {
			t.Error("seeding enabled a source")
		}
	}
	// The catalog is searchable in both languages and by alias.
	f := finder(st, 20)
	for q, want := range map[string]string{"archer c6": "Archer C6 AC1200 Gigabit Router", "রাউটার আর্চার": "Archer C6 AC1200 Gigabit Router", "pendrive 32gb": "Cruzer Glide 32GB USB 3.0 Pendrive", "zolo 20000mah": "Zolo 20000mAh 22.5W Power Bank"} {
		got, err := f.Candidates(ctx, search.ParseQuery(q), nil, 3)
		if err != nil || len(got) == 0 || got[0].NameEn != want {
			t.Errorf("search %q = %+v, %v; want %q first", q, got, err, want)
		}
	}
}

// TestShopDescription: shops need a description, and admins see it.
func TestShopDescription(t *testing.T) {
	t.Parallel()
	a := startApp(t, newDB(t, true), nil, false, nil)
	w := newWorld(t, a.rt.Store)
	vendorB, adminB := newBrowser(t, a.server.URL), newBrowser(t, a.server.URL)
	vendorB.post("/vendor/login", url.Values{"email": {"vendor@example.test"}, "password": {testPassword}})
	at := w.north(800)
	form := url.Values{"name": {"Described Store"}, "address": {"1 Road"}, "area": {fmt.Sprint(w.areaID)}, "lat": {fmt.Sprint(at.Lat)}, "lng": {fmt.Sprint(at.Lng)}}
	form.Set("description", "too short")
	if code, body, _ := vendorB.post("/vendor/shops/new", form); code != 422 || !strings.Contains(body, "20 to 1000 characters") {
		t.Errorf("short description = %d", code)
	}
	form.Set("description", "Mobile accessories and electronics.\n\n\nOpen since 2015, next to the mosque.")
	if code, _, _ := vendorB.post("/vendor/shops/new", form); code != 303 {
		t.Fatalf("create = %d", code)
	}
	adminB.post("/vendor/login", url.Values{"email": {"admin@example.test"}, "password": {testPassword}})
	_, body, _ := adminB.get("/admin")
	if !strings.Contains(body, "Mobile accessories and electronics.\n\nOpen since 2015, next to the mosque.") {
		t.Error("admin queue does not show the (normalized) description")
	}
	// District-level registration ("city area not listed") is accepted.
	if !strings.Contains(body, "Described Store") || !strings.Contains(body, "Chattogram") {
		t.Error("district-level shop missing from the queue")
	}
}
