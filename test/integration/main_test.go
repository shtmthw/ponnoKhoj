//go:build integration

// Package integration tests ponnoKhoj against a real PostgreSQL with PostGIS
// and pg_trgm, started with testcontainers. Run with `make test-integration`.
package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/shtmthw/ponnoKhoj/internal/app"
	"github.com/shtmthw/ponnoKhoj/internal/auth"
	"github.com/shtmthw/ponnoKhoj/internal/config"
	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/httpx"
	"github.com/shtmthw/ponnoKhoj/internal/llm"
	"github.com/shtmthw/ponnoKhoj/internal/migrations"
	"github.com/shtmthw/ponnoKhoj/internal/seed"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
	"github.com/shtmthw/ponnoKhoj/internal/vendor"
)

// postgisImage is the PostGIS image the tests run against, pinned by digest
// (postgis/postgis:17-3.6). Keep in step with POSTGIS_IMAGE in deploy/versions.env.
const postgisImage = "postgis/postgis:17-3.6@sha256:1ac10b2551ba84ae05db8e7da477afcd0a3fede99bcb7705da2ee157390cb095"

const templateDB = "pk_template"

var (
	adminURL string // connection string of the server's maintenance database
	dbSeq    atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	ctx := context.Background()
	// TEST_DATABASE_URL lets the suite run against an existing PostGIS
	// server (it must allow CREATE DATABASE); otherwise a container starts.
	adminURL = os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		container, err := postgres.Run(ctx, postgisImage,
			postgres.WithDatabase("postgres"), postgres.WithUsername("postgres"), postgres.WithPassword("integration-test-only"),
			postgres.BasicWaitStrategies())
		if err != nil {
			fmt.Fprintln(os.Stderr, "start postgis container:", err)
			return 1
		}
		defer func() { _ = testcontainers.TerminateContainer(container) }()
		adminURL, err = container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			fmt.Fprintln(os.Stderr, "container connection string:", err)
			return 1
		}
	}
	// Build a migrated, reference-seeded template once; each test clones it.
	if err := exec(ctx, adminURL, "DROP DATABASE IF EXISTS "+templateDB+" WITH (FORCE)", "CREATE DATABASE "+templateDB); err != nil {
		fmt.Fprintln(os.Stderr, "create template database:", err)
		return 1
	}
	tmplURL := urlFor(templateDB)
	if _, err := migrations.Up(ctx, tmplURL); err != nil {
		fmt.Fprintln(os.Stderr, "migrate template:", err)
		return 1
	}
	st, err := store.Open(ctx, tmplURL, 4)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open template:", err)
		return 1
	}
	if _, err = seed.Areas(ctx, st); err == nil {
		_, err = seed.Categories(ctx, st)
	}
	st.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed template:", err)
		return 1
	}
	return m.Run()
}

func exec(ctx context.Context, connURL string, stmts ...string) error {
	conn, err := pgx.Connect(ctx, connURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}

func urlFor(database string) string {
	u, err := url.Parse(adminURL)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + database
	return u.String()
}

// newDB creates a fresh database for one test. With template=true it is a
// clone of the migrated, reference-seeded template; otherwise it is empty.
func newDB(t *testing.T, template bool) string {
	t.Helper()
	name := fmt.Sprintf("pk_test_%d_%d", os.Getpid(), dbSeq.Add(1))
	create := "CREATE DATABASE " + name
	if template {
		create += " TEMPLATE " + templateDB
	}
	if err := exec(context.Background(), adminURL, create); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if err := exec(context.Background(), adminURL, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("drop database %s: %v", name, err)
		}
	})
	return urlFor(name)
}

func openStore(t *testing.T, dbURL string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), dbURL, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testConfig builds a valid configuration for a test database.
func testConfig(t *testing.T, dbURL string, overrides map[string]string) config.Config {
	t.Helper()
	env := map[string]string{
		"DATABASE_URL": dbURL, "APP_SECRET": "integration-test-secret-0123456789abcdef",
		"COOKIE_SECURE": "false", "SHUTDOWN_DRAIN_DELAY": "0s", "LOG_LEVEL": "error",
		"RATE_LIMIT_SEARCH_PER_MIN": "100000", "RATE_LIMIT_ESTIMATE_PER_MIN": "100000",
		"RATE_LIMIT_AUTH_PER_MIN": "100000", "RATE_LIMIT_REPORT_PER_MIN": "100000",
		"WORKER_CONCURRENCY": "4",
	}
	for k, v := range overrides {
		env[k] = v
	}
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// testApp is the full application (web + API + worker) on a test database.
type testApp struct {
	rt     *app.Runtime
	server *httptest.Server
	ready  *httpx.Readiness
}

// startApp boots the real HTTP handler chain behind an httptest server and,
// when withWorker is set, the River worker. The demo shop's base URL is
// pointed at the test server itself so the online pipeline runs end to end.
func startApp(t *testing.T, dbURL string, overrides map[string]string, withWorker bool, llmClient llm.Client) *testApp {
	t.Helper()
	ctx := context.Background()
	a := &testApp{ready: &httpx.Readiness{}}
	// The handler is created after the server so the server URL is known.
	var handler http.Handler
	a.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(a.server.Close)

	env := map[string]string{"PUBLIC_BASE_URL": a.server.URL, "DEMO_SHOP_BASE_URL": a.server.URL + "/demo-shop"}
	for k, v := range overrides {
		env[k] = v
	}
	rt, err := app.NewRuntime(ctx, testConfig(t, dbURL, env), io.Discard, "integration-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close(context.Background()) })
	a.rt = rt
	if err := rt.SyncSources(ctx); err != nil {
		t.Fatal(err)
	}
	handler, _, err = rt.Handlers(a.ready)
	if err != nil {
		t.Fatal(err)
	}
	if withWorker {
		worker, err := rt.WorkerClient(llmClient)
		if err != nil {
			t.Fatal(err)
		}
		if err := worker.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { worker.Stop(10 * time.Second) })
	}
	return a
}

// browser is an HTTP client with a cookie jar that does not follow
// redirects, so tests can assert on them.
type browser struct {
	t      *testing.T
	client *http.Client
	base   string
}

func newBrowser(t *testing.T, base string) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, base: base, client: &http.Client{
		Jar: jar, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (b *browser) do(req *http.Request) (int, string, http.Header) {
	b.t.Helper()
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", "en")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return resp.StatusCode, string(body), resp.Header
}

func (b *browser) get(path string) (int, string, http.Header) {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodGet, b.base+path, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	return b.do(req)
}

func (b *browser) post(path string, form url.Values) (int, string, http.Header) {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodPost, b.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// What a browser sends for a same-origin form post.
	req.Header.Set("Origin", b.base)
	return b.do(req)
}

// waitFor polls until cond is true or the deadline passes.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// world is a small hand-built data set with known geometry around a centre.
type world struct {
	st      *store.Store
	auth    *auth.Service
	vendors *vendor.Service
	admin   auth.User
	vendor  auth.User
	areaID  int64
	center  geo.Point
	catID   int64
}

const testPassword = "integration-password"

func newWorld(t *testing.T, st *store.Store) *world {
	t.Helper()
	ctx := context.Background()
	authSvc, err := auth.NewService(st, auth.Config{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := authSvc.CreateUser(ctx, "admin@example.test", testPassword, "Admin", domain.RoleAdmin, false)
	if err != nil {
		t.Fatal(err)
	}
	v, err := authSvc.SignUp(ctx, "vendor@example.test", testPassword, "Vendor")
	if err != nil {
		t.Fatal(err)
	}
	areaID, err := st.GetAreaByName(ctx, db.GetAreaByNameParams{Kind: "district", NameEn: "Chattogram"})
	if err != nil {
		t.Fatal(err)
	}
	area, err := st.GetArea(ctx, areaID)
	if err != nil || !area.HasCentroid {
		t.Fatalf("Chattogram centroid: %v", err)
	}
	cats, err := st.ListCategories(ctx)
	if err != nil || len(cats) == 0 {
		t.Fatalf("categories: %v", err)
	}
	var catID int64
	for _, c := range cats {
		if c.Slug == "rice-grains" {
			catID = c.ID
		}
	}
	return &world{
		st: st, auth: authSvc, admin: admin, vendor: v, areaID: areaID, catID: catID,
		center:  geo.Point{Lat: area.Lat, Lng: area.Lng},
		vendors: vendor.NewService(st, vendor.Config{Freshness: domain.DefaultFreshnessPolicy(), CSVMaxRows: 100}),
	}
}

// north returns the point a given number of meters north of the centre.
func (w *world) north(meters float64) geo.Point {
	return geo.Point{Lat: w.center.Lat + meters/111_195, Lng: w.center.Lng}
}

// shop creates a shop owned by the world's vendor at a point.
func (w *world) shop(t *testing.T, name string, at geo.Point, status domain.ShopStatus) int64 {
	t.Helper()
	id, err := w.st.CreateShop(context.Background(), db.CreateShopParams{
		OwnerID: w.vendor.ID, Name: name, Address: "Test address", AreaID: w.areaID, Lat: at.Lat, Lng: at.Lng, Status: string(status),
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// product creates an active catalog product.
func (w *world) product(t *testing.T, nameEn, nameBn, size string, aliases ...string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := insertProduct(ctx, w.st, w.catID, nameEn, nameBn, size)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range aliases {
		if err := insertAlias(ctx, w.st, id, a); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// listing sets a price directly and backdates it by age.
func (w *world) listing(t *testing.T, shopID, productID, pricePoisha int64, age time.Duration) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := w.st.UpsertListing(ctx, db.UpsertListingParams{ShopID: shopID, ProductID: productID, PricePoisha: pricePoisha, Unit: "g", InStock: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.SetListingUpdatedAt(ctx, db.SetListingUpdatedAtParams{ID: id, UpdatedAt: time.Now().Add(-age)}); err != nil {
		t.Fatal(err)
	}
	return id
}
