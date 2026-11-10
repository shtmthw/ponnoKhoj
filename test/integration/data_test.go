//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/auth"
	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/estimate"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/migrations"
	"github.com/shtmthw/ponnoKhoj/internal/moderation"
	"github.com/shtmthw/ponnoKhoj/internal/observability"
	"github.com/shtmthw/ponnoKhoj/internal/pricefinder"
	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/seed"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
	"github.com/shtmthw/ponnoKhoj/internal/vendor"
)

func tableExists(t *testing.T, st *store.Store, name string) bool {
	t.Helper()
	var exists bool
	if err := st.Pool.QueryRow(context.Background(), `SELECT to_regclass('public.' || $1) IS NOT NULL`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// TestMigrationsUpDownUp is the M1 acceptance check: every migration has a
// working down migration, and the schema can be rebuilt afterwards.
func TestMigrationsUpDownUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbURL := newDB(t, false)
	st := openStore(t, dbURL)

	if v, err := st.SchemaVersion(ctx); err != nil || v != 0 {
		t.Fatalf("unmigrated database reports version %d, %v", v, err)
	}
	// Two migrators at once (two Jobs racing): the advisory lock serializes
	// them and both succeed.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = migrations.Up(ctx, dbURL)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent migrate up: %v", err)
		}
	}
	want := migrations.ExpectedVersion()
	if v, err := migrations.Version(ctx, dbURL); err != nil || v != want {
		t.Fatalf("after up: version %d, %v; want %d", v, err, want)
	}
	if v, _ := st.SchemaVersion(ctx); v != want {
		t.Errorf("store.SchemaVersion = %d, want %d", v, want)
	}
	for _, table := range []string{"areas", "products", "shops", "listings", "offers", "estimates", "reports", "audit_log", "river_job"} {
		if !tableExists(t, st, table) {
			t.Errorf("table %s missing after up", table)
		}
	}

	// One step down removes only the newest migration.
	res, err := migrations.Down(ctx, dbURL, false)
	if err != nil || len(res) != 1 || res[0].Version != want {
		t.Fatalf("down one: %+v, %v", res, err)
	}
	if tableExists(t, st, "product_source_urls") || !tableExists(t, st, "estimates") {
		t.Error("down one step (00004) removed the wrong tables")
	}
	// Two steps: 00003 resets the trigram operator cost.
	if res, err := migrations.Down(ctx, dbURL, false); err != nil || len(res) != 1 || res[0].Version != want-1 {
		t.Fatalf("down two: %+v, %v", res, err)
	}
	if applied, err := st.TrigramCostApplied(ctx); err != nil || applied {
		t.Errorf("down 00003 left the trigram operator cost in place (applied=%v, err=%v)", applied, err)
	}
	// Three steps: the online/estimate tables go, the core stays.
	if res, err := migrations.Down(ctx, dbURL, false); err != nil || len(res) != 1 || res[0].Version != want-2 {
		t.Fatalf("down three: %+v, %v", res, err)
	}
	if tableExists(t, st, "estimates") || !tableExists(t, st, "shops") {
		t.Error("down three steps removed the wrong tables")
	}
	// All the way down: no application or River tables remain.
	if _, err := migrations.Down(ctx, dbURL, true); err != nil {
		t.Fatalf("down all: %v", err)
	}
	for _, table := range []string{"areas", "products", "shops", "listings", "offers", "estimates", "river_job"} {
		if tableExists(t, st, table) {
			t.Errorf("table %s still exists after down --all", table)
		}
	}
	if v, _ := st.SchemaVersion(ctx); v != 0 {
		t.Errorf("version after down --all = %d", v)
	}
	// And up again.
	if _, err := migrations.Up(ctx, dbURL); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if v, _ := migrations.Version(ctx, dbURL); v != want {
		t.Errorf("version after second up = %d", v)
	}
	// Up on an up-to-date database is a no-op.
	if res, err := migrations.Up(ctx, dbURL); err != nil || len(res) != 0 {
		t.Errorf("idempotent up: %+v, %v", res, err)
	}
}

// TestPointHelperArgumentOrder pins the single place that converts
// (latitude, longitude) into PostGIS's longitude-first order.
func TestPointHelperArgumentOrder(t *testing.T) {
	t.Parallel()
	st := openStore(t, newDB(t, true))
	var x, y float64
	err := st.Pool.QueryRow(context.Background(), `SELECT ST_X(pk_point(22.335109, 91.834073)::geometry), ST_Y(pk_point(22.335109, 91.834073)::geometry)`).Scan(&x, &y)
	if err != nil {
		t.Fatal(err)
	}
	if x != 91.834073 || y != 22.335109 {
		t.Errorf("pk_point(lat, lng) produced X=%v (want longitude 91.834073), Y=%v (want latitude 22.335109)", x, y)
	}
	// A round trip through a shop row returns the same lat/lng.
	w := newWorld(t, st)
	id := w.shop(t, "Roundtrip", geo.Point{Lat: 22.5, Lng: 91.25}, domain.ShopVerified)
	shop, err := st.GetShop(context.Background(), id)
	if err != nil || shop.Lat != 22.5 || shop.Lng != 91.25 {
		t.Errorf("shop round trip: lat %v lng %v (%v)", shop.Lat, shop.Lng, err)
	}
	// PostGIS distance agrees with the Go haversine within 0.5%.
	var d float64
	if err := st.Pool.QueryRow(context.Background(), `SELECT ST_Distance(pk_point(22.335109, 91.834073), pk_point(23.7115253, 90.4111451))`).Scan(&d); err != nil {
		t.Fatal(err)
	}
	h := geo.DistanceMeters(geo.Point{Lat: 22.335109, Lng: 91.834073}, geo.Point{Lat: 23.7115253, Lng: 90.4111451})
	if diff := (d - h) / d; diff > 0.005 || diff < -0.005 {
		t.Errorf("PostGIS distance %.0f m vs haversine %.0f m", d, h)
	}
}

func finder(st *store.Store, pageSize int) *pricefinder.Service {
	est := estimate.NewService(st, estimate.DefaultParams(), time.Hour, nil, observability.NewMetrics(), quietLog())
	return pricefinder.NewService(st, est, noEnqueue{}, pricefinder.Config{
		DefaultRadiusM: 3000, MaxRadiusM: 25000, Freshness: domain.DefaultFreshnessPolicy(),
		OfferCacheTTL: time.Hour, OfferMaxAge: 24 * time.Hour, PageSize: pageSize,
	}, observability.NewMetrics(), quietLog())
}

type noEnqueue struct{}

func (noEnqueue) EnqueueRefreshOffers(context.Context, int64) error         { return nil }
func (noEnqueue) EnqueueEstimate(context.Context, int64, string, int) error { return nil }

// TestSeedAndProductSearch is the rest of the M1 acceptance check: the
// seeded database answers trigram product queries (English, Bangla, aliases)
// and radius queries, and both can use their indexes.
func TestSeedAndProductSearch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openStore(t, newDB(t, true))
	authSvc, err := auth.NewService(st, auth.Config{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	opts := seed.DemoOptions{AdminPassword: testPassword, VendorPassword: testPassword}
	sum, err := seed.Demo(ctx, st, authSvc, opts)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Products != len(seed.DemoCatalog) || sum.Shops != 26 || sum.Listings < 300 {
		t.Errorf("demo seed summary = %+v", sum)
	}
	areas, _ := st.CountAreas(ctx)
	if areas != 8+64+494 {
		t.Errorf("areas = %d, want 566 (8 divisions, 64 districts, 494 upazilas)", areas)
	}
	// Idempotent: seeding again changes no counts and keeps logins working.
	before := [3]int64{}
	before[0], _ = st.CountProducts(ctx)
	before[1], _ = st.CountShops(ctx)
	before[2], _ = st.CountListings(ctx)
	if _, err := seed.Demo(ctx, st, authSvc, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Areas(ctx, st); err != nil {
		t.Fatal(err)
	}
	after := [3]int64{}
	after[0], _ = st.CountProducts(ctx)
	after[1], _ = st.CountShops(ctx)
	after[2], _ = st.CountListings(ctx)
	if n, _ := st.CountAreas(ctx); before != after || n != areas {
		t.Errorf("re-seeding changed counts: %v -> %v, areas %d", before, after, n)
	}
	if _, _, err := authSvc.Login(ctx, seed.DemoAdminEmail, testPassword); err != nil {
		t.Errorf("demo admin cannot log in: %v", err)
	}
	// Everything the demo seed created is marked as demo.
	var notDemo int
	if err := st.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM shops WHERE NOT is_demo) + (SELECT count(*) FROM products WHERE NOT is_demo) + (SELECT count(*) FROM users WHERE NOT is_demo)`).Scan(&notDemo); err != nil || notDemo != 0 {
		t.Errorf("%d demo rows are not flagged is_demo (%v)", notDemo, err)
	}

	f := finder(st, 20)
	queries := []struct{ q, want string }{
		{"miniket rice 5kg", "Miniket Rice 5 kg"},
		{"Miniket Rice 5 KG", "Miniket Rice 5 kg"},
		{"miniket rice 5000 gm", "Miniket Rice 5 kg"},
		{"মিনিকেট চাল ৫ কেজি", "Miniket Rice 5 kg"},
		{"চাল ১ কেজি", "Miniket Rice 1 kg"},
		{"minikat rise 5kg", "Miniket Rice 5 kg"}, // misspelled
		{"peyaj", "Onion (Local) 1 kg"},           // romanized alias
		{"পেঁয়াজ", "Onion (Local) 1 kg"},
		{"soyabin tel 1 ltr", "Soybean Oil 1 L"},
		{"সয়াবিন তেল ৫ লিটার", "Soybean Oil 5 L"},
		{"dim 1 dozen", "Farm Egg 12 pcs"},
		{"ডিম ১ ডজন", "Farm Egg 12 pcs"},
		{"lobon", "Iodized Salt 1 kg"},
		{"নাজিরশাইল", "Nazirshail Rice 5 kg"},
	}
	for _, tc := range queries {
		got, err := f.Candidates(ctx, search.ParseQuery(tc.q), nil, 5)
		if err != nil {
			t.Fatalf("%q: %v", tc.q, err)
		}
		if len(got) == 0 || got[0].NameEn != tc.want {
			names := []string{}
			for _, p := range got {
				names = append(names, p.NameEn)
			}
			t.Errorf("search %q: top results %v, want %q first", tc.q, names, tc.want)
		}
	}
	if got, _ := f.Candidates(ctx, search.ParseQuery("xylophone quartz"), nil, 5); len(got) != 0 {
		t.Errorf("nonsense query matched %d products", len(got))
	}
	// A category filter narrows the candidates.
	cats, _ := st.ListCategories(ctx)
	for _, c := range cats {
		if c.Slug != "cooking-oil" {
			continue
		}
		got, _ := f.Candidates(ctx, search.ParseQuery("1 l"), &c.ID, 10)
		for _, p := range got {
			if p.CategoryID != c.ID {
				t.Errorf("category filter leaked %q", p.NameEn)
			}
		}
		if len(got) == 0 {
			t.Error("category filter returned nothing")
		}
	}

	// Index use. The demo data is tiny (30 products, 26 shops), and on tables
	// that small Postgres rightly prefers sequential scans. So this grows the
	// tables to a realistic size with synthetic rows and then checks the
	// planner's own choice, with no enable_* settings changed.
	// The plans are recorded in docs/architecture.md.
	var catID, ownerID, areaID int64
	if err := st.Pool.QueryRow(ctx, `SELECT (SELECT id FROM categories ORDER BY id LIMIT 1), (SELECT id FROM users ORDER BY id LIMIT 1), (SELECT id FROM areas WHERE kind = 'district' ORDER BY id LIMIT 1)`).Scan(&catID, &ownerID, &areaID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `
		INSERT INTO products (category_id, name_en, name_bn, unit, quantity, search_key, is_demo)
		SELECT $1, 'Synthetic ' || g, 'Synthetic ' || g, 'g', 1000,
		       substr(md5(g::text), 1, 8) || ' ' || substr(md5((g * 7)::text), 1, 6) || ' ' || substr(md5((g * 13)::text), 1, 7) || ' 1000g', true
		FROM generate_series(1, 20000) g`, catID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `
		INSERT INTO product_aliases (product_id, alias, alias_key)
		SELECT id, 'alias', substr(md5((id * 31)::text), 1, 9) FROM products WHERE name_en LIKE 'Synthetic %'`); err != nil {
		t.Fatal(err)
	}
	// 20,000 synthetic shops spread over a 2.5 x 3 degree box.
	if _, err := st.Pool.Exec(ctx, `
		INSERT INTO shops (owner_id, name, address, area_id, location, status, is_demo)
		SELECT $1, 'Synthetic shop ' || g, 'synthetic', $2,
		       pk_point(22 + (hashtext('a' || g) & 65535) / 65535.0 * 2.5, 89 + (hashtext('b' || g) & 65535) / 65535.0 * 3), 'verified', true
		FROM generate_series(1, 20000) g`, ownerID, areaID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `ANALYZE products; ANALYZE product_aliases; ANALYZE shops; ANALYZE listings`); err != nil {
		t.Fatal(err)
	}
	// explain returns the plan of a query, optionally after a SET LOCAL.
	explain := func(setLocal, sql string, args ...any) string {
		tx, err := st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if setLocal != "" {
			if _, err := tx.Exec(ctx, setLocal); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := tx.Query(ctx, "EXPLAIN "+sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		return strings.Join(plan, "\n")
	}
	// The product search query (the text of sqlc's SearchProducts), as the
	// planner chooses to run it. Migration 00003 gave the trigram operator
	// its real cost; without that the planner scans all 20,030 rows.
	if applied, err := st.TrigramCostApplied(ctx); err != nil || !applied {
		t.Fatalf("migration 00003 did not set the trigram operator cost (applied=%v, err=%v)", applied, err)
	}
	const productSearch = `
		WITH matched AS (
		    SELECT p.id FROM products p WHERE $1::text <% p.search_key
		    UNION
		    SELECT a.product_id FROM product_aliases a WHERE $1::text <% a.alias_key
		)
		SELECT p.id FROM matched m JOIN products p ON p.id = m.id WHERE p.status = 'active'`
	plan := explain("", productSearch, "miniket rice 5000g")
	t.Logf("product search plan (20,030 products):\n%s", plan)
	for _, index := range []string{"products_search_key_trgm", "product_aliases_key_trgm"} {
		if !strings.Contains(plan, index) {
			t.Errorf("product search does not use %s", index)
		}
	}
	if strings.Contains(plan, "Seq Scan") {
		t.Error("product search scans a whole table")
	}
	// For the record: the plan the default operator cost produces.
	t.Logf("product search plan with pg_trgm's default operator cost:\n%s",
		explain("ALTER FUNCTION word_similarity_op(text, text) COST 1", productSearch, "miniket rice 5000g"))
	// The real catalog is still found among the synthetic rows.
	if got, err := f.Candidates(ctx, search.ParseQuery("মিনিকেট চাল ৫ কেজি"), nil, 3); err != nil || len(got) == 0 || got[0].NameEn != "Miniket Rice 5 kg" {
		t.Errorf("search among 20k products: %+v, %v", got, err)
	}
	// The radius predicate of local search: the planner's own choice.
	plan = explain("", `SELECT s.id FROM shops s WHERE s.status = 'verified' AND NOT s.auto_hidden AND ST_DWithin(s.location, pk_point($1, $2), 3000)`, 22.335109, 91.834073)
	t.Logf("radius query plan (20,026 shops):\n%s", plan)
	if !strings.Contains(plan, "shops_location_gist") || strings.Contains(plan, "Seq Scan on shops") {
		t.Error("radius query does not use the GiST index on shops.location")
	}
}

// TestLocalSearchRadiusFreshnessAndPaging covers the local mode rules.
func TestLocalSearchRadiusFreshnessAndPaging(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openStore(t, newDB(t, true))
	w := newWorld(t, st)
	day := 24 * time.Hour
	rice := w.product(t, "Test Rice 5 kg", "টেস্ট চাল ৫ কেজি", "5 kg")

	near := w.shop(t, "Near (1 km)", w.north(1000), domain.ShopVerified)
	mid := w.shop(t, "Mid (2.5 km)", w.north(2500), domain.ShopVerified)
	edge := w.shop(t, "Just inside (2.95 km)", w.north(2950), domain.ShopVerified)
	far := w.shop(t, "Far (5 km)", w.north(5000), domain.ShopVerified)
	pending := w.shop(t, "Pending (0.5 km)", w.north(500), domain.ShopPending)
	suspended := w.shop(t, "Suspended (0.6 km)", w.north(600), domain.ShopSuspended)
	old := w.shop(t, "Old price (1.5 km)", w.north(1500), domain.ShopVerified)
	tie := w.shop(t, "Same price, further (2 km)", w.north(2000), domain.ShopVerified)

	w.listing(t, near, rice, 41000, 1*day)  // fresh
	w.listing(t, mid, rice, 40000, 10*day)  // stale, cheapest
	w.listing(t, edge, rice, 43000, 6*day)  // fresh, at the fresh threshold side
	w.listing(t, far, rice, 30000, 1*day)   // outside 3 km
	w.listing(t, pending, rice, 10000, 0)   // shop not verified
	w.listing(t, suspended, rice, 10000, 0) // shop suspended
	w.listing(t, old, rice, 20000, 31*day)  // hidden: older than 30 days
	w.listing(t, tie, rice, 41000, 29*day)  // stale, ties with "near" on price

	f := finder(st, 20)
	resp, err := f.Search(ctx, pricefinder.Request{Query: "test rice 5kg", Mode: domain.ModeLocal, Place: pricefinder.PointPlace(w.center), RadiusM: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Selected == nil || resp.Selected.ID != rice || resp.Local == nil || resp.Online != nil || resp.Estimate != nil {
		t.Fatalf("local-mode response shape wrong: %+v", resp)
	}
	var names []string
	for _, l := range resp.Local.Listings {
		names = append(names, l.ShopName)
	}
	// Cheapest first; equal prices ordered by distance.
	want := []string{"Mid (2.5 km)", "Near (1 km)", "Same price, further (2 km)", "Just inside (2.95 km)"}
	if strings.Join(names, "|") != strings.Join(want, "|") || resp.Local.Total != 4 {
		t.Fatalf("local results = %v (total %d), want %v", names, resp.Local.Total, want)
	}
	byName := map[string]pricefinder.LocalListing{}
	for _, l := range resp.Local.Listings {
		byName[l.ShopName] = l
	}
	if byName["Near (1 km)"].Freshness != domain.Fresh || byName["Mid (2.5 km)"].Freshness != domain.Stale || byName["Same price, further (2 km)"].Freshness != domain.Stale || byName["Just inside (2.95 km)"].Freshness != domain.Fresh {
		t.Errorf("freshness states wrong: %+v", byName)
	}
	if d := byName["Near (1 km)"].DistanceM; d < 980 || d > 1020 {
		t.Errorf("distance to the 1 km shop = %.0f m", d)
	}
	if !strings.HasPrefix(byName["Near (1 km)"].DirectionsURL, "https://www.openstreetmap.org/directions?to=") {
		t.Errorf("directions link = %q", byName["Near (1 km)"].DirectionsURL)
	}

	// A wider radius brings in the far shop, which is the cheapest.
	resp, err = f.Search(ctx, pricefinder.Request{Query: "test rice 5kg", Mode: domain.ModeLocal, Place: pricefinder.PointPlace(w.center), RadiusM: 6000})
	if err != nil || resp.Local.Total != 5 || resp.Local.Listings[0].ShopName != "Far (5 km)" {
		t.Errorf("6 km search: total %d, first %q (%v)", resp.Local.Total, resp.Local.Listings[0].ShopName, err)
	}
	// A tight radius finds nothing and suggests an estimate in "all" mode.
	resp, err = f.Search(ctx, pricefinder.Request{Query: "test rice 5kg", Mode: domain.ModeAll, Place: pricefinder.PointPlace(w.center), RadiusM: 300})
	if err != nil || resp.Local.Total != 0 || resp.Estimate == nil || resp.Estimate.State != pricefinder.EstimateSuggested {
		t.Errorf("300 m search: total %d, estimate %+v (%v)", resp.Local.Total, resp.Estimate, err)
	}
	// No location: local mode says so instead of guessing one.
	resp, err = f.Search(ctx, pricefinder.Request{Query: "test rice 5kg", Mode: domain.ModeAll})
	if err != nil || !resp.Local.NeedsLocation || resp.Estimate.State != pricefinder.EstimateNeedsLocation {
		t.Errorf("no-location search: %+v (%v)", resp.Local, err)
	}
	// The radius is clamped to the configured maximum.
	resp, _ = f.Search(ctx, pricefinder.Request{Query: "test rice 5kg", Mode: domain.ModeLocal, Place: pricefinder.PointPlace(w.center), RadiusM: 9_000_000})
	if resp.Local.RadiusM != 25000 {
		t.Errorf("radius not clamped: %d", resp.Local.RadiusM)
	}

	// Keyset pagination: one result per page walks the same order with no
	// repeats and no gaps.
	paged := finder(st, 1)
	var walked []string
	cursor := ""
	for range 10 {
		resp, err := paged.Search(ctx, pricefinder.Request{ProductID: rice, Mode: domain.ModeLocal, Place: pricefinder.PointPlace(w.center), RadiusM: 3000, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range resp.Local.Listings {
			walked = append(walked, l.ShopName)
		}
		if cursor = resp.Local.NextCursor; cursor == "" {
			break
		}
	}
	if strings.Join(walked, "|") != strings.Join(want, "|") {
		t.Errorf("paged walk = %v, want %v", walked, want)
	}
	if _, err := paged.Search(ctx, pricefinder.Request{ProductID: rice, Mode: domain.ModeLocal, Place: pricefinder.PointPlace(w.center), Cursor: "not-a-cursor"}); !errors.Is(err, pricefinder.ErrBadCursor) {
		t.Errorf("bad cursor err = %v", err)
	}
	if _, err := paged.Search(ctx, pricefinder.Request{ProductID: 999999, Mode: domain.ModeLocal}); !errors.Is(err, pricefinder.ErrProductNotFound) {
		t.Errorf("unknown product err = %v", err)
	}

	// Search locations are recorded only as a ~1 km cell, never as coordinates.
	// Counts are aggregated in memory and written on flush.
	for _, svc := range []*pricefinder.Service{f, paged} {
		if err := svc.FlushStats(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var cells []string
	rows, err := st.Pool.Query(ctx, `SELECT DISTINCT cell FROM search_stats`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		cells = append(cells, c)
	}
	rows.Close()
	for _, c := range cells {
		if c != "none" && c != w.center.Cell() {
			t.Errorf("unexpected search_stats cell %q", c)
		}
	}
	if len(cells) != 2 {
		t.Errorf("search_stats cells = %v, want the centre's cell and \"none\"", cells)
	}
}

func TestAuthFlow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openStore(t, newDB(t, true))
	svc, err := auth.NewService(st, auth.Config{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	u, err := svc.SignUp(ctx, "  Rahim@Example.TEST ", "correct horse battery", "Rahim")
	if err != nil || u.Email != "rahim@example.test" || u.Role != domain.RoleVendor {
		t.Fatalf("sign up: %+v, %v", u, err)
	}
	if _, err := svc.SignUp(ctx, "rahim@example.test", "another password", "Imposter"); !errors.Is(err, auth.ErrEmailTaken) {
		t.Errorf("duplicate email err = %v", err)
	}
	for _, bad := range [][3]string{{"nope", "long enough password", "N"}, {"a@b.test", "short", "N"}, {"a@b.test", "long enough password", " "}} {
		if _, err := svc.SignUp(ctx, bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("invalid sign-up accepted: %v", bad)
		}
	}
	// The password is stored as an argon2id hash, never in the clear.
	row, _ := st.GetUserByEmail(ctx, "rahim@example.test")
	if !strings.HasPrefix(row.PasswordHash, "$argon2id$") || strings.Contains(row.PasswordHash, "correct horse") {
		t.Errorf("stored password hash = %q", row.PasswordHash)
	}

	token, logged, err := svc.Login(ctx, "RAHIM@example.test", "correct horse battery")
	if err != nil || logged.ID != u.ID || token == "" {
		t.Fatalf("login: %v", err)
	}
	// Only the token's hash is stored.
	var stored int
	_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE encode(token_hash, 'escape') LIKE '%' || $1 || '%'`, token).Scan(&stored)
	if stored != 0 {
		t.Error("the raw session token is stored in the database")
	}
	if got, err := svc.Authenticate(ctx, token); err != nil || got.ID != u.ID {
		t.Errorf("authenticate: %+v, %v", got, err)
	}
	for _, bad := range []string{"", "garbage", strings.Repeat("A", 43)} {
		if _, err := svc.Authenticate(ctx, bad); !errors.Is(err, auth.ErrNoSession) {
			t.Errorf("Authenticate(%q) err = %v", bad, err)
		}
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("session survives logout: %v", err)
	}
	if err := svc.Logout(ctx, "garbage"); err != nil {
		t.Errorf("logout with a bad token: %v", err)
	}

	// Unknown account and wrong password are indistinguishable.
	if _, _, err := svc.Login(ctx, "nobody@example.test", "whatever password"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("unknown account err = %v", err)
	}
	// Lockout: after five consecutive failures even the right password is refused.
	for i := range auth.LockoutThreshold {
		if _, _, err := svc.Login(ctx, "rahim@example.test", "wrong password"); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("failed attempt %d err = %v", i+1, err)
		}
	}
	if _, _, err := svc.Login(ctx, "rahim@example.test", "correct horse battery"); !errors.Is(err, auth.ErrLocked) {
		t.Errorf("after %d failures err = %v, want ErrLocked", auth.LockoutThreshold, err)
	}
	row, _ = st.GetUserByEmail(ctx, "rahim@example.test")
	if row.LockedUntil == nil || time.Until(*row.LockedUntil) < 30*time.Second || time.Until(*row.LockedUntil) > 90*time.Second {
		t.Errorf("locked_until = %v, want about a minute from now", row.LockedUntil)
	}
	// Once the lock expires a correct login works and clears the counter.
	if _, err := st.Pool.Exec(ctx, `UPDATE users SET locked_until = now() - interval '1 second' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "rahim@example.test", "correct horse battery"); err != nil {
		t.Errorf("login after lock expiry: %v", err)
	}
	if row, _ = st.GetUserByEmail(ctx, "rahim@example.test"); row.FailedLogins != 0 || row.LockedUntil != nil {
		t.Errorf("failure counter not reset: %+v", row)
	}

	// Expired sessions do not authenticate and are swept.
	expired, _ := auth.NewService(st, auth.Config{SessionTTL: -time.Minute})
	oldToken, _, err := expired.Login(ctx, "rahim@example.test", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, oldToken); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("expired session err = %v", err)
	}
	if n, err := st.DeleteExpiredSessions(ctx); err != nil || n != 1 {
		t.Errorf("swept %d expired sessions (%v), want 1", n, err)
	}
}

// TestVendorListingAndModeration is the M2 flow at the service level.
func TestVendorListingAndModeration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openStore(t, newDB(t, true))
	w := newWorld(t, st)
	f := finder(st, 20)
	mod := moderation.NewService(st, 3, "report-hash-secret-0123456789abcdef")
	rice := w.product(t, "Test Rice 5 kg", "টেস্ট চাল ৫ কেজি", "5 kg")
	found := func(radius int) []string {
		t.Helper()
		resp, err := f.Search(ctx, pricefinder.Request{ProductID: rice, Mode: domain.ModeLocal, Place: pricefinder.PointPlace(w.center), RadiusM: radius})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, l := range resp.Local.Listings {
			names = append(names, l.ShopName)
		}
		return names
	}

	// A new shop starts pending and is invisible until an admin verifies it.
	in := vendor.ShopInput{Name: "Rahim Store", Description: "Grocery shop near the station: rice, lentils, oil and spices.", Address: "12 Station Road", AreaID: w.areaID, Location: w.north(800), Phone: "+880 1700-000000"}
	shopID, err := w.vendors.CreateShop(ctx, w.vendor, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.vendors.SetListing(ctx, w.vendor, shopID, rice, 40500, true)
	if err != nil || res.Flagged {
		t.Fatalf("set listing: %+v, %v", res, err)
	}
	if got := found(3000); len(got) != 0 {
		t.Errorf("a pending shop's listing is visible: %v", got)
	}
	if err := mod.SetShopStatus(ctx, w.vendor, shopID, domain.ShopVerified); !errors.Is(err, moderation.ErrForbidden) {
		t.Errorf("a vendor verified their own shop: %v", err)
	}
	if err := mod.SetShopStatus(ctx, w.admin, shopID, domain.ShopVerified); err != nil {
		t.Fatal(err)
	}
	if got := found(3000); len(got) != 1 || got[0] != "Rahim Store" {
		t.Errorf("verified shop not found within radius: %v", got)
	}
	if got := found(500); len(got) != 0 {
		t.Errorf("shop 800 m away found within 500 m: %v", got)
	}

	// Validation and ownership.
	if _, err := w.vendors.CreateShop(ctx, w.vendor, vendor.ShopInput{Name: "No pin", Description: "A shop description long enough.", Address: "x", AreaID: w.areaID}); err == nil {
		t.Error("shop without a location accepted")
	}
	if _, err := w.vendors.CreateShop(ctx, w.vendor, vendor.ShopInput{Name: "Bad area", Description: "A shop description long enough.", Address: "x", AreaID: 999999, Location: w.center}); err == nil {
		t.Error("shop with an unknown area accepted")
	}
	other, _ := w.auth.SignUp(ctx, "other@example.test", testPassword, "Other")
	if _, err := w.vendors.SetListing(ctx, other, shopID, rice, 100, true); !errors.Is(err, vendor.ErrForbidden) {
		t.Errorf("another vendor set a price in this shop: %v", err)
	}
	if _, err := w.vendors.OwnedShop(ctx, other, shopID); !errors.Is(err, vendor.ErrForbidden) {
		t.Errorf("OwnedShop for a stranger: %v", err)
	}
	if err := w.vendors.UpdateShop(ctx, other, shopID, in); !errors.Is(err, vendor.ErrNotFound) {
		t.Errorf("another vendor edited this shop: %v", err)
	}
	if _, err := w.vendors.SetListing(ctx, w.vendor, shopID, 999999, 100, true); err == nil {
		t.Error("listing for an unknown product accepted")
	}
	if _, err := w.vendors.SetListing(ctx, w.vendor, shopID, rice, 0, true); err == nil {
		t.Error("zero price accepted")
	}
	// The database itself refuses a non-positive price.
	if _, err := st.Pool.Exec(ctx, `UPDATE listings SET price_poisha = 0 WHERE id = $1`, res.ID); err == nil {
		t.Error("CHECK constraint did not stop a zero price")
	}
	in.Name, in.Location = "Rahim Store (moved)", w.north(900)
	if err := w.vendors.UpdateShop(ctx, w.vendor, shopID, in); err != nil {
		t.Fatal(err)
	}
	if got := found(3000); len(got) != 1 || got[0] != "Rahim Store (moved)" {
		t.Errorf("after edit: %v", got)
	}

	// Price history: a changed price is recorded, an unchanged one is not.
	if _, err := w.vendors.SetListing(ctx, w.vendor, shopID, rice, 41000, true); err != nil {
		t.Fatal(err)
	}
	if _, err := w.vendors.SetListing(ctx, w.vendor, shopID, rice, 41000, false); err != nil {
		t.Fatal(err)
	}
	var history int
	_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM listing_price_history WHERE listing_id = $1`, res.ID).Scan(&history)
	if history != 2 {
		t.Errorf("price history rows = %d, want 2 (40500 and 41000)", history)
	}

	// Price anomaly guard: needs five comparables; flags > 3x and < 1/3 of
	// the local median; a flagged listing is accepted but is not evidence.
	for i := range 4 {
		s := w.shop(t, "Comparable "+string(rune('A'+i)), w.north(float64(1000+200*i)), domain.ShopVerified)
		w.listing(t, s, rice, int64(40000+500*i), time.Hour)
	}
	if res, _ := w.vendors.SetListing(ctx, w.vendor, shopID, rice, 400000, true); res.Flagged {
		t.Error("flagged with only four comparables")
	}
	fifth := w.shop(t, "Comparable E", w.north(1900), domain.ShopVerified)
	w.listing(t, fifth, rice, 42000, time.Hour)
	res, err = w.vendors.SetListing(ctx, w.vendor, shopID, rice, 400000, true)
	if err != nil || !res.Flagged {
		t.Fatalf("10x price not flagged: %+v, %v", res, err)
	}
	if res, _ := w.vendors.SetListing(ctx, w.vendor, shopID, rice, 10000, true); !res.Flagged {
		t.Error("quarter price not flagged")
	}
	if res, _ := w.vendors.SetListing(ctx, w.vendor, shopID, rice, 45000, true); res.Flagged {
		t.Error("normal price flagged")
	}
	res, _ = w.vendors.SetListing(ctx, w.vendor, shopID, rice, 400000, true)
	flagged, _ := st.ListFlaggedListings(ctx, 10)
	if len(flagged) != 1 || flagged[0].ID != res.ID {
		t.Fatalf("flagged queue = %+v", flagged)
	}
	evidence := func() int {
		rows, err := st.EvidenceListings(ctx, db.EvidenceListingsParams{Lat: w.center.Lat, Lng: w.center.Lng, ProductID: rice, VisibleSince: time.Now().Add(-30 * 24 * time.Hour), MaxM: 5000, MinM: 0, MaxResults: 100})
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	if n := evidence(); n != 5 {
		t.Errorf("evidence listings = %d, want 5 (the flagged one excluded)", n)
	}
	// It is still shown to shoppers, labelled as under review.
	resp, _ := f.Search(ctx, pricefinder.Request{ProductID: rice, Mode: domain.ModeLocal, Place: pricefinder.PointPlace(w.center), RadiusM: 3000})
	last := resp.Local.Listings[len(resp.Local.Listings)-1]
	if last.ShopID != shopID || !last.UnderReview {
		t.Errorf("flagged listing not shown as under review: %+v", last)
	}
	if err := mod.ReviewAnomaly(ctx, w.admin, res.ID, false); err != nil {
		t.Fatal(err)
	}
	if n := evidence(); n != 6 {
		t.Errorf("evidence listings after approval = %d, want 6", n)
	}
	if err := mod.ReviewAnomaly(ctx, w.admin, res.ID, false); !errors.Is(err, moderation.ErrNotFound) {
		t.Errorf("reviewing an unflagged listing: %v", err)
	}

	// Reports: distinct reporters count once each; the third hides the listing.
	target := res.ID
	r1, err := mod.Report(ctx, moderation.TargetListing, target, domain.ReportWrongPrice, "it was 450", "198.51.100.1")
	if err != nil || r1.Hidden || r1.Duplicate {
		t.Fatalf("first report: %+v, %v", r1, err)
	}
	if dup, _ := mod.Report(ctx, moderation.TargetListing, target, domain.ReportFake, "", "198.51.100.1"); !dup.Duplicate {
		t.Error("the same reporter counted twice")
	}
	_, _ = mod.Report(ctx, moderation.TargetListing, target, domain.ReportWrongPrice, "", "198.51.100.2")
	if got := found(3000); len(got) != 6 {
		t.Errorf("listing hidden before the threshold: %d visible", len(got))
	}
	r3, err := mod.Report(ctx, moderation.TargetListing, target, domain.ReportWrongPrice, "", "198.51.100.3")
	if err != nil || !r3.Hidden {
		t.Fatalf("third report: %+v, %v", r3, err)
	}
	if got := found(3000); len(got) != 5 {
		t.Errorf("auto-hidden listing still visible: %v", got)
	}
	// Reporter IPs are not stored.
	var leaked int
	_ = st.Pool.QueryRow(ctx, `SELECT count(*) FROM reports WHERE encode(reporter_hash, 'escape') LIKE '%198.51.100%' OR note LIKE '%198.51.100%'`).Scan(&leaked)
	if leaked != 0 {
		t.Error("a reporter IP is stored in the reports table")
	}
	open, _ := st.ListOpenReports(ctx, 10)
	if len(open) != 3 || open[0].TargetShopID != shopID || open[0].ProductEn != "Test Rice 5 kg" {
		t.Errorf("open reports = %+v", open)
	}
	for _, bad := range []struct {
		target string
		id     int64
		reason domain.ReportReason
		want   error
	}{{"listing", 999999, domain.ReportFake, moderation.ErrNotFound}, {"planet", target, domain.ReportFake, moderation.ErrInvalidReport}, {"listing", target, "spam", moderation.ErrInvalidReport}} {
		if _, err := mod.Report(ctx, bad.target, bad.id, bad.reason, "", "198.51.100.9"); !errors.Is(err, bad.want) {
			t.Errorf("Report(%s, %d, %s) err = %v, want %v", bad.target, bad.id, bad.reason, err, bad.want)
		}
	}
	// Dismissing the reports makes the listing visible again.
	if err := mod.ResolveReports(ctx, w.admin, moderation.TargetListing, target, false); err != nil {
		t.Fatal(err)
	}
	if got := found(3000); len(got) != 6 {
		t.Errorf("listing not restored after dismissal: %d visible", len(got))
	}
	// Shop reports: upholding suspends the shop.
	for _, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		if _, err := mod.Report(ctx, moderation.TargetShop, fifth, domain.ReportClosed, "", ip); err != nil {
			t.Fatal(err)
		}
	}
	if got := found(3000); len(got) != 5 {
		t.Errorf("auto-hidden shop still visible: %d", len(got))
	}
	if err := mod.ResolveReports(ctx, w.admin, moderation.TargetShop, fifth, true); err != nil {
		t.Fatal(err)
	}
	if shop, _ := st.GetShop(ctx, fifth); shop.Status != string(domain.ShopSuspended) {
		t.Errorf("upheld shop status = %s", shop.Status)
	}

	// CSV bulk upload: valid rows applied, bad rows reported with line numbers.
	// (One comparable shop was just suspended; add another so the anomaly
	// guard has its five comparables again.)
	sixth := w.shop(t, "Comparable F", w.north(2100), domain.ShopVerified)
	w.listing(t, sixth, rice, 41500, time.Hour)
	oil := w.product(t, "Test Oil 1 L", "টেস্ট তেল ১ লিটার", "1 l")
	csv := "product_id,price,in_stock\n" +
		itoa(rice) + ",\"৳ ৪০,৫০০\",yes\n" + // 40,500: far above the median, will be flagged
		itoa(oil) + ",175.50,no\n" +
		"999999,10,yes\n" +
		"abc,10,yes\n"
	bulk, err := w.vendors.BulkUpload(ctx, w.vendor, shopID, strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if bulk.Applied != 2 || bulk.Flagged != 1 || len(bulk.Errors) != 2 {
		t.Fatalf("bulk result = %+v", bulk)
	}
	if bulk.Errors[0] != (vendor.RowError{Line: 5, Code: vendor.RowBadProduct, Detail: "abc"}) || bulk.Errors[1] != (vendor.RowError{Line: 4, Code: vendor.RowUnknownProd, Detail: "999999"}) {
		t.Errorf("bulk errors = %+v", bulk.Errors)
	}
	listings, _ := st.ListListingsByShop(ctx, shopID)
	prices := map[int64]int64{}
	for _, l := range listings {
		prices[l.ProductID] = l.PricePoisha
		if l.ProductID == oil && l.InStock {
			t.Error("in_stock=no not applied")
		}
	}
	if prices[rice] != 4050000 || prices[oil] != 17550 {
		t.Errorf("bulk prices = %v", prices)
	}
	if _, err := w.vendors.BulkUpload(ctx, other, shopID, strings.NewReader(csv)); !errors.Is(err, vendor.ErrForbidden) {
		t.Errorf("bulk upload to someone else's shop: %v", err)
	}
	if _, err := w.vendors.BulkUpload(ctx, w.vendor, shopID, strings.NewReader("nope\n")); !errors.Is(err, vendor.ErrCSVHeader) {
		t.Errorf("bad header err = %v", err)
	}

	// Product proposals go through the admin queue.
	pid, err := w.vendors.ProposeProduct(ctx, w.vendor, vendor.ProductInput{NameEn: "Puffed Rice 500 g", NameBn: "মুড়ি ৫০০ গ্রাম", CategoryID: w.catID, Size: "৫০০ গ্রাম"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Candidates(ctx, search.ParseQuery("puffed rice"), nil, 5); len(got) != 0 {
		t.Error("a proposed product is searchable before approval")
	}
	if queue, _ := st.ListProposedProducts(ctx, 10); len(queue) != 1 || queue[0].ID != pid || queue[0].Quantity != 500 || queue[0].ProposerEmail != "vendor@example.test" {
		t.Errorf("proposal queue = %+v", queue)
	}
	if err := mod.ReviewProduct(ctx, w.admin, pid, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Candidates(ctx, search.ParseQuery("মুড়ি"), nil, 5); len(got) != 1 || got[0].ID != pid {
		t.Errorf("approved product not searchable: %+v", got)
	}
	for _, bad := range []vendor.ProductInput{{NameEn: "", CategoryID: w.catID, Size: "1 kg"}, {NameEn: "X", CategoryID: w.catID, Size: "big"}, {NameEn: "X", CategoryID: 999999, Size: "1 kg"}, {NameEn: "X", Size: "1 kg"}} {
		if _, err := w.vendors.ProposeProduct(ctx, w.vendor, bad); err == nil {
			t.Errorf("invalid proposal accepted: %+v", bad)
		}
	}

	// Deleting a listing.
	if err := w.vendors.DeleteListing(ctx, other, res.ID); !errors.Is(err, vendor.ErrNotFound) {
		t.Errorf("a stranger deleted a listing: %v", err)
	}
	if err := w.vendors.DeleteListing(ctx, w.vendor, res.ID); err != nil {
		t.Fatal(err)
	}

	// Every vendor and admin mutation above left an audit trail.
	audit, err := st.ListAudit(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]int{}
	for _, a := range audit {
		actions[a.Action]++
		if a.Action != "auto_hide" && a.ActorID == nil {
			t.Errorf("audit row %q has no actor", a.Action)
		}
	}
	for _, want := range []string{"shop_created", "shop_verified", "shop_updated", "listing_set", "listing_bulk_set", "listing_deleted", "anomaly_approved", "auto_hide", "reports_dismissed", "reports_upheld", "product_proposed", "product_active"} {
		if actions[want] == 0 {
			t.Errorf("no audit log entry for %q (have %v)", want, actions)
		}
	}
}
