package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/auth"
	"github.com/shtmthw/ponnoKhoj/internal/config"
	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/httpx"
	"github.com/shtmthw/ponnoKhoj/internal/moderation"
	"github.com/shtmthw/ponnoKhoj/internal/pricefinder"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
	"github.com/shtmthw/ponnoKhoj/internal/vendor"
)

//go:embed templates/*.html
var templateFS embed.FS

// Limits are the per-IP rate limiters of the HTML surface.
type Limits struct {
	Search   *httpx.RateLimiter
	Estimate *httpx.RateLimiter
	Poll     *httpx.RateLimiter
	Auth     *httpx.RateLimiter
	Report   *httpx.RateLimiter
}

// Deps is everything the web UI needs.
type Deps struct {
	Cfg        config.Config
	Store      *store.Store
	Auth       *auth.Service
	Finder     *pricefinder.Service
	Vendor     *vendor.Service
	Moderation *moderation.Service
	Log        *slog.Logger
	ClientIP   func(*http.Request) string
	Limits     Limits
}

// Server renders the HTML pages.
type Server struct {
	d       Deps
	catalog *Catalog
	assets  *Assets
	pages   map[string]*template.Template

	refMu      sync.Mutex
	refLoaded  time.Time
	districts  []db.ListDistrictsRow
	categories []db.Category
}

// Page is the data every template receives.
type Page struct {
	L               *Localizer
	Title           string
	User            *auth.User
	Path            string
	Flash           string
	FlashKind       string
	Assets          *Assets
	TileURL         string
	TileAttribution string
	DemoMode        bool
	Data            any
}

// templateFuncs are the helpers available in every template.
var templateFuncs = template.FuncMap{
	// dict builds a map for passing several values to a sub-template.
	"dict": func(kv ...any) (map[string]any, error) {
		if len(kv)%2 != 0 {
			return nil, errors.New("dict: odd number of arguments")
		}
		m := make(map[string]any, len(kv)/2)
		for i := 0; i < len(kv); i += 2 {
			key, ok := kv[i].(string)
			if !ok {
				return nil, errors.New("dict: keys must be strings")
			}
			m[key] = kv[i+1]
		}
		return m, nil
	},
	"money": func(poisha int64) domain.Money { return domain.Money(poisha) },
	// rangePos is where the point estimate sits between low and high, in
	// percent, for the estimate range bar.
	"rangePos": func(low, point, high domain.Money) int {
		if high <= low {
			return 50
		}
		return int((point - low) * 100 / (high - low))
	},
	// initial is the first letter of a name, for the small source badge.
	"initial": func(s string) string {
		for _, r := range strings.TrimSpace(s) {
			return strings.ToUpper(string(r))
		}
		return "?"
	},
	"deref": func(f *float64) float64 {
		if f == nil {
			return 0
		}
		return *f
	},
	"dereft": func(t *time.Time) time.Time {
		if t == nil {
			return time.Time{}
		}
		return *t
	},
}

// pageFiles lists the page templates; each is parsed together with the
// layout and the shared partials.
var pageFiles = []string{
	"search.html", "shop.html", "auth.html", "error.html", "admin.html",
	"vendor_dashboard.html", "vendor_shop_form.html", "vendor_shop.html", "vendor_product_new.html",
}

// ParseTemplates parses every page. It is exported for the golden tests.
func ParseTemplates() (map[string]*template.Template, error) {
	pages := map[string]*template.Template{}
	for _, name := range pageFiles {
		t, err := template.New(name).Funcs(templateFuncs).ParseFS(templateFS, "templates/layout.html", "templates/estimate.html", "templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", name, err)
		}
		pages[name] = t
	}
	return pages, nil
}

// New builds the web UI.
func New(d Deps) (*Server, error) {
	catalog, err := LoadCatalog()
	if err != nil {
		return nil, err
	}
	assets, err := NewAssets()
	if err != nil {
		return nil, err
	}
	pages, err := ParseTemplates()
	if err != nil {
		return nil, err
	}
	return &Server{d: d, catalog: catalog, assets: assets, pages: pages}, nil
}

// Register adds the HTML routes.
func (s *Server) Register(rt *httpx.Router) {
	limit := func(l *httpx.RateLimiter) httpx.Middleware {
		return l.Middleware(s.d.ClientIP, func(w http.ResponseWriter, r *http.Request) {
			s.renderError(w, r, http.StatusTooManyRequests, "error.rate_limited")
		})
	}
	limitSearches := func(l *httpx.RateLimiter) httpx.Middleware { return searchesOnly(limit(l)) }
	user, admin := s.requireUser, s.requireAdmin

	rt.Handle("GET /static/", s.assets)
	rt.HandleFunc("GET /robots.txt", s.handleRobots)
	rt.HandleFunc("GET /favicon.ico", s.handleFavicon)
	rt.HandleFunc("GET /lang", s.handleLang)

	rt.HandleFunc("GET /{$}", s.handleSearch, limitSearches(s.d.Limits.Search))
	rt.HandleFunc("GET /search", s.handleSearch, limitSearches(s.d.Limits.Search))
	rt.HandleFunc("GET /estimate", s.handleEstimatePoll, limit(s.d.Limits.Poll))
	rt.HandleFunc("GET /areas/upazilas", s.handleUpazilaOptions, limit(s.d.Limits.Poll))
	rt.HandleFunc("POST /estimate", s.handleEstimateStart, limit(s.d.Limits.Estimate))
	rt.HandleFunc("GET /shops/{id}", s.handleShop)
	rt.HandleFunc("POST /report", s.handleReport, limit(s.d.Limits.Report))

	rt.HandleFunc("GET /vendor/signup", s.handleAuthForm)
	rt.HandleFunc("POST /vendor/signup", s.handleSignUp, limit(s.d.Limits.Auth))
	rt.HandleFunc("GET /vendor/login", s.handleAuthForm)
	rt.HandleFunc("POST /vendor/login", s.handleLogin, limit(s.d.Limits.Auth))
	rt.HandleFunc("POST /vendor/logout", s.handleLogout)

	rt.HandleFunc("GET /vendor", s.handleDashboard, user)
	rt.HandleFunc("GET /vendor/catalog.csv", s.handleCatalogCSV, user)
	rt.HandleFunc("GET /vendor/shops/new", s.handleShopForm, user)
	rt.HandleFunc("POST /vendor/shops/new", s.handleShopSave, user)
	rt.HandleFunc("GET /vendor/shops/{id}", s.handleVendorShop, user)
	rt.HandleFunc("GET /vendor/shops/{id}/edit", s.handleShopForm, user)
	rt.HandleFunc("POST /vendor/shops/{id}/edit", s.handleShopSave, user)
	rt.HandleFunc("GET /vendor/shops/{id}/products", s.handleProductOptions, user)
	rt.HandleFunc("POST /vendor/shops/{id}/listings", s.handleListingSet, user)
	rt.HandleFunc("POST /vendor/shops/{id}/bulk", s.handleBulkUpload, user)
	rt.HandleFunc("POST /vendor/listings/{id}/delete", s.handleListingDelete, user)
	rt.HandleFunc("GET /vendor/products/new", s.handleProposeForm, user)
	rt.HandleFunc("POST /vendor/products/new", s.handlePropose, user)

	rt.HandleFunc("GET /admin", s.handleAdmin, admin)
	rt.HandleFunc("POST /admin/shops/{id}/status", s.handleAdminShopStatus, admin)
	rt.HandleFunc("POST /admin/reports/resolve", s.handleAdminResolve, admin)
	rt.HandleFunc("POST /admin/listings/{id}/anomaly", s.handleAdminAnomaly, admin)
	rt.HandleFunc("POST /admin/products/{id}/review", s.handleAdminProduct, admin)

	if s.d.Cfg.DemoShopEnabled {
		demo := newDemoShop(s.d.Cfg.PublicBaseURL)
		rt.HandleFunc("GET /demo-shop/search", demo.handleSearch)
		rt.HandleFunc("GET /demo-shop/p/{slug}", demo.handleProduct)
	}
	rt.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.renderError(w, r, http.StatusNotFound, "error.not_found")
	})
}

type ctxKey int

const ctxUser ctxKey = iota

// currentUser resolves the session cookie to a user (nil when signed out).
// The lookup result is cached on the request context.
func (s *Server) currentUser(r *http.Request) *auth.User {
	if u, ok := r.Context().Value(ctxUser).(*auth.User); ok {
		return u
	}
	token := s.d.Auth.TokenFromRequest(r)
	if token == "" {
		return nil
	}
	u, err := s.d.Auth.Authenticate(r.Context(), token)
	if err != nil {
		if !errors.Is(err, auth.ErrNoSession) {
			s.d.Log.ErrorContext(r.Context(), "session lookup failed", "error", err)
		}
		return nil
	}
	return &u
}

func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := s.currentUser(r)
		if u == nil {
			target := "/vendor/login"
			if r.Method == http.MethodGet {
				target += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		// Signed-in pages are personal: never cache them.
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUser, u)))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return s.requireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := s.currentUser(r); u == nil || !u.IsAdmin() {
			s.renderError(w, r, http.StatusForbidden, "error.forbidden")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (s *Server) localizer(r *http.Request) *Localizer { return s.catalog.For(languageOf(r)) }

// page assembles the layout data for a request.
func (s *Server) page(r *http.Request, title string, data any) *Page {
	l := s.localizer(r)
	p := &Page{
		L: l, Title: title, User: s.currentUser(r), Path: r.URL.RequestURI(), Assets: s.assets,
		TileURL: s.d.Cfg.MapTileURL, TileAttribution: s.d.Cfg.MapTileAttribution,
		DemoMode: s.d.Cfg.DemoShopEnabled, Data: data,
	}
	// Flash messages travel as a catalog key; only flash.* keys are shown,
	// so the parameter cannot inject arbitrary text.
	if key := r.URL.Query().Get("flash"); strings.HasPrefix(key, "flash.") {
		if msg := l.T(key); msg != key {
			p.Flash, p.FlashKind = msg, "ok"
			if strings.HasPrefix(key, "flash.err.") {
				p.FlashKind = "error"
			}
		}
	}
	return p
}

// render executes a page into a buffer first, so a template error becomes a
// clean 500 instead of half a page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, file string, p *Page) {
	s.renderNamed(w, r, status, file, "layout", p)
}

func (s *Server) renderNamed(w http.ResponseWriter, r *http.Request, status int, file, name string, data any) {
	var buf bytes.Buffer
	if err := s.pages[file].ExecuteTemplate(&buf, name, data); err != nil {
		s.d.Log.ErrorContext(r.Context(), "template failed", "template", file, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

type errorVM struct{ Message string }

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, key string) {
	l := s.localizer(r)
	p := s.page(r, l.T("error.title", l.Num(status)), errorVM{Message: l.T(key)})
	s.render(w, r, status, "error.html", p)
}

// serverError logs the cause and shows a generic error page.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.d.Log.ErrorContext(r.Context(), "request failed", "request_id", httpx.RequestID(r.Context()), "error", err)
	s.renderError(w, r, http.StatusInternalServerError, "error.internal")
}

// parseForm parses a small urlencoded form (capped at 64 KiB).
func parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	return r.ParseForm() == nil
}

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

func formID(r *http.Request, name string) int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue(name)), 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

// safeNext accepts only same-site paths as redirect targets.
func safeNext(next, fallback string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") && !strings.Contains(next, "\\") && !strings.ContainsAny(next, "\r\n") {
		return next
	}
	return fallback
}

func redirectFlash(w http.ResponseWriter, r *http.Request, target, key string) {
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	http.Redirect(w, r, target+sep+"flash="+url.QueryEscape(key), http.StatusSeeOther) //nolint:gosec // G710: target is a same-site path built by our handlers, never request input
}

// reference returns the districts and categories, cached for a few minutes:
// they change only when an operator re-seeds.
func (s *Server) reference(ctx context.Context) ([]db.ListDistrictsRow, []db.Category, error) {
	s.refMu.Lock()
	defer s.refMu.Unlock()
	if time.Since(s.refLoaded) < 5*time.Minute && len(s.districts) > 0 {
		return s.districts, s.categories, nil
	}
	districts, err := s.d.Store.ListDistricts(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("web: list districts: %w", err)
	}
	categories, err := s.d.Store.ListCategories(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("web: list categories: %w", err)
	}
	s.districts, s.categories, s.refLoaded = districts, categories, time.Now()
	return districts, categories, nil
}

func (s *Server) handleRobots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	// The demo shop is fictional: only ponnoKhoj's own bot may read it.
	_, _ = w.Write([]byte("User-agent: ponnoKhojBot\nAllow: /demo-shop/\nDisallow: /\n\n" +
		"User-agent: *\nDisallow: /vendor/\nDisallow: /admin\nDisallow: /api/\nDisallow: /demo-shop/\nDisallow: /estimate\n"))
}

// searchesOnly applies a rate-limit middleware to actual searches (a query
// or a product) only. The home page and an empty search form cost nothing to
// render, so a rate-limited visitor can always get back to the home page.
func searchesOnly(limited httpx.Middleware) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		guarded := limited(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if q := r.URL.Query(); q.Get("q") == "" && q.Get("product") == "" {
				next.ServeHTTP(w, r)
				return
			}
			guarded.ServeHTTP(w, r)
		})
	}
}

// handleFavicon points browsers that ask for /favicon.ico (pages without
// the layout's <link rel="icon">, such as the JSON API) at the SVG icon.
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.Redirect(w, r, s.assets.Path("favicon.svg"), http.StatusMovedPermanently)
}

func (s *Server) handleLang(w http.ResponseWriter, r *http.Request) {
	if lang := r.URL.Query().Get("set"); lang == LangBn || lang == LangEn {
		http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: language preference only; Secure follows COOKIE_SECURE (forced on in prod)
			Name: langCookie, Value: lang, Path: "/", MaxAge: 365 * 24 * 3600,
			HttpOnly: true, Secure: s.d.Cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
		})
	}
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next"), "/"), http.StatusSeeOther) //nolint:gosec // G710: safeNext accepts only same-site paths (tested in TestSafeNext)
}
