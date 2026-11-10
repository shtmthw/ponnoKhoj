// Package config parses and validates ponnoKhoj's configuration, which comes
// from environment variables only. Everything is validated at startup so a
// bad value fails fast with a clear message.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config is the effective configuration.
type Config struct {
	Env           string
	HTTPAddr      string
	MetricsAddr   string
	PublicBaseURL string

	DatabaseURL string
	DBMaxConns  int

	LogLevel  string
	LogFormat string

	ShutdownDrainDelay time.Duration
	ShutdownTimeout    time.Duration

	AppSecret      string
	TrustedProxies []netip.Prefix
	CookieSecure   bool
	SessionTTL     time.Duration

	AutoVerifyShops     bool
	ReportHideThreshold int

	SearchDefaultRadiusM int
	SearchMaxRadiusM     int
	ListingFreshFor      time.Duration
	ListingStaleFor      time.Duration

	RateLimitSearchPerMin   int
	RateLimitEstimatePerMin int
	RateLimitAuthPerMin     int
	RateLimitReportPerMin   int

	CSVMaxBytes int64
	CSVMaxRows  int

	MapTileURL         string
	MapTileAttribution string

	SourcesEnabled  []string
	DemoShopEnabled bool
	DemoShopBaseURL string
	OfferCacheTTL   time.Duration
	OfferMaxAge     time.Duration

	EstimateTTL          time.Duration
	EstimateMinEvidence  int
	EstimateEnvelopeLow  float64
	EstimateEnvelopeHigh float64

	LLMEnabled   bool
	LLMBaseURL   string
	LLMModel     string
	LLMAPIKey    string
	LLMMaxSteps  int
	LLMTimeout   time.Duration
	LLMMaxTokens int

	TracingEnabled    bool
	WorkerConcurrency int
}

// Var documents one environment variable. The README's configuration table is
// checked against Vars by a test, so the two cannot drift.
type Var struct {
	Name        string
	Default     string // "" means no default
	Required    bool
	Secret      bool
	Description string
}

// Vars lists every environment variable ponnoKhoj reads, in display order.
var Vars = []Var{
	{Name: "PONNOKHOJ_ENV", Default: "dev", Description: "`dev` or `prod`. `prod` turns on HSTS and defaults logs to JSON."},
	{Name: "HTTP_ADDR", Default: ":8080", Description: "Listen address for the web UI and JSON API."},
	{Name: "METRICS_ADDR", Default: ":9090", Description: "Listen address for Prometheus `/metrics` (never routed through the Gateway)."},
	{Name: "PUBLIC_BASE_URL", Default: "http://localhost:8080", Description: "Public origin of the site; used in the fetch client's User-Agent contact URL."},
	{Name: "DATABASE_URL", Required: true, Secret: true, Description: "PostgreSQL connection string (PostGIS and pg_trgm required)."},
	{Name: "DB_MAX_CONNS", Default: "10", Description: "Maximum connections in the pool."},
	{Name: "LOG_LEVEL", Default: "info", Description: "`debug`, `info`, `warn` or `error`."},
	{Name: "LOG_FORMAT", Default: "", Description: "`json` or `text`. Defaults to `json` in prod and `text` in dev."},
	{Name: "SHUTDOWN_DRAIN_DELAY", Default: "5s", Description: "After SIGTERM, how long readiness fails before the server stops accepting requests."},
	{Name: "SHUTDOWN_TIMEOUT", Default: "20s", Description: "Deadline for in-flight requests and jobs to finish after the drain delay."},
	{Name: "APP_SECRET", Required: true, Secret: true, Description: "At least 32 characters. Keys the hash that anonymizes report submitters."},
	{Name: "TRUSTED_PROXIES", Default: "", Description: "Comma-separated CIDRs whose `X-Forwarded-For` is honored. Empty trusts nobody."},
	{Name: "COOKIE_SECURE", Default: "true", Description: "Set the `Secure` flag on the session cookie. Only disable for plain-HTTP local development."},
	{Name: "SESSION_TTL", Default: "168h", Description: "Lifetime of a vendor/admin session."},
	{Name: "AUTO_VERIFY_SHOPS", Default: "false", Description: "Mark new shops verified without admin review (development only)."},
	{Name: "REPORT_HIDE_THRESHOLD", Default: "3", Description: "Distinct open reports that auto-hide a listing or shop pending review."},
	{Name: "SEARCH_DEFAULT_RADIUS_M", Default: "3000", Description: "Local search radius when none is given, in meters."},
	{Name: "SEARCH_MAX_RADIUS_M", Default: "25000", Description: "Largest accepted search radius, in meters."},
	{Name: "LISTING_FRESH_FOR", Default: "168h", Description: "Listings updated within this are fresh."},
	{Name: "LISTING_STALE_FOR", Default: "720h", Description: "Listings older than fresh but within this are stale; older ones are hidden."},
	{Name: "RATE_LIMIT_SEARCH_PER_MIN", Default: "60", Description: "Search requests per client IP per minute."},
	{Name: "RATE_LIMIT_ESTIMATE_PER_MIN", Default: "10", Description: "Estimate requests per client IP per minute."},
	{Name: "RATE_LIMIT_AUTH_PER_MIN", Default: "10", Description: "Login and sign-up attempts per client IP per minute."},
	{Name: "RATE_LIMIT_REPORT_PER_MIN", Default: "5", Description: "Report submissions per client IP per minute."},
	{Name: "CSV_MAX_BYTES", Default: "1048576", Description: "Largest accepted bulk-upload CSV, in bytes."},
	{Name: "CSV_MAX_ROWS", Default: "2000", Description: "Most data rows accepted in one bulk upload."},
	{Name: "MAP_TILE_URL", Default: "https://tile.openstreetmap.org/{z}/{x}/{y}.png", Description: "Leaflet tile URL template. The default is OpenStreetMap's server; follow its tile usage policy."},
	{Name: "MAP_TILE_ATTRIBUTION", Default: "© OpenStreetMap contributors", Description: "Attribution shown on every map."},
	{Name: "SOURCES_ENABLED", Default: "", Description: "Comma-separated online source IDs to enable. Every source is off unless listed (see docs/sources.md)."},
	{Name: "DEMO_SHOP_ENABLED", Default: "false", Description: "Serve the built-in fictional online shop under `/demo-shop/` (demo data only)."},
	{Name: "DEMO_SHOP_BASE_URL", Default: "http://localhost:8080/demo-shop", Description: "Where the worker reaches the demo shop when the `demoshop` source is enabled."},
	{Name: "OFFER_CACHE_TTL", Default: "6h", Description: "Online offers older than this trigger a background refresh (stale-while-revalidate)."},
	{Name: "OFFER_MAX_AGE", Default: "168h", Description: "Online offers older than this are no longer shown."},
	{Name: "ESTIMATE_TTL", Default: "24h", Description: "How long an estimate is cached per product, ~1 km cell and radius bucket."},
	{Name: "ESTIMATE_MIN_EVIDENCE", Default: "3", Description: "Independent evidence items required before any estimate is produced."},
	{Name: "ESTIMATE_ENVELOPE_LOW", Default: "0.5", Description: "An estimate's low bound may not fall below this multiple of the cheapest evidence."},
	{Name: "ESTIMATE_ENVELOPE_HIGH", Default: "1.5", Description: "An estimate's high bound may not exceed this multiple of the dearest evidence."},
	{Name: "LLM_ENABLED", Default: "false", Description: "Enable LLM-assisted refinement of estimates. Off means statistical estimates only."},
	{Name: "LLM_BASE_URL", Default: "", Description: "OpenAI-compatible base URL, e.g. `http://localhost:11434/v1` for Ollama. Required when LLM_ENABLED."},
	{Name: "LLM_MODEL", Default: "", Description: "Model name sent to the LLM endpoint. Required when LLM_ENABLED."},
	{Name: "LLM_API_KEY", Default: "", Secret: true, Description: "Bearer token for the LLM endpoint (may be empty for local Ollama)."},
	{Name: "LLM_MAX_STEPS", Default: "6", Description: "Maximum tool-calling steps per estimate."},
	{Name: "LLM_TIMEOUT", Default: "20s", Description: "Total time budget for the LLM on one estimate."},
	{Name: "LLM_MAX_TOKENS", Default: "1024", Description: "Completion token cap per LLM call."},
	{Name: "OTEL_TRACING_ENABLED", Default: "false", Description: "Export OpenTelemetry traces over OTLP/HTTP (configure the exporter with the standard `OTEL_EXPORTER_OTLP_*` variables)."},
	{Name: "WORKER_CONCURRENCY", Default: "5", Description: "Jobs the worker runs at once."},
	{Name: "SEED_ADMIN_PASSWORD", Default: "", Secret: true, Description: "Password for the demo admin account. Read only by `ponnokhoj seed --demo`."},
	{Name: "SEED_VENDOR_PASSWORD", Default: "", Secret: true, Description: "Password for the demo vendor accounts. Read only by `ponnokhoj seed --demo`."},
}

// LookupFunc matches os.LookupEnv.
type LookupFunc func(string) (string, bool)

type parser struct {
	lookup LookupFunc
	errs   []string
}

func (p *parser) raw(name string) string {
	v, ok := p.lookup(name)
	if ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	for _, d := range Vars {
		if d.Name == name {
			if d.Required {
				p.errs = append(p.errs, name+" is required")
			}
			return d.Default
		}
	}
	panic("config: undocumented variable " + name) // programmer error, caught by tests
}

func (p *parser) fail(name, msg string) { p.errs = append(p.errs, name+" "+msg) }

func (p *parser) str(name string) string { return p.raw(name) }

func (p *parser) oneOf(name string, allowed ...string) string {
	v := p.raw(name)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	p.fail(name, "must be one of "+strings.Join(allowed, ", "))
	return allowed[0]
}

func (p *parser) integer(name string, minV, maxV int) int {
	v := p.raw(name)
	n, err := strconv.Atoi(v)
	if err != nil || n < minV || n > maxV {
		p.fail(name, fmt.Sprintf("must be an integer between %d and %d", minV, maxV))
		return minV
	}
	return n
}

func (p *parser) float(name string, minV, maxV float64) float64 {
	v := p.raw(name)
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f != f || f < minV || f > maxV {
		p.fail(name, fmt.Sprintf("must be a number between %g and %g", minV, maxV))
		return minV
	}
	return f
}

func (p *parser) boolean(name string) bool {
	switch strings.ToLower(p.raw(name)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	p.fail(name, "must be true or false")
	return false
}

func (p *parser) duration(name string, minV, maxV time.Duration) time.Duration {
	d, err := time.ParseDuration(p.raw(name))
	if err != nil || d < minV || d > maxV {
		p.fail(name, fmt.Sprintf("must be a duration between %s and %s", minV, maxV))
		return minV
	}
	return d
}

func (p *parser) list(name string) []string {
	var out []string
	for part := range strings.SplitSeq(p.raw(name), ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (p *parser) httpURL(name string, allowEmpty bool) string {
	v := p.raw(name)
	if v == "" && allowEmpty {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		p.fail(name, "must be an http(s) URL")
		return ""
	}
	return strings.TrimRight(v, "/")
}

// Load parses and validates the configuration. All problems are reported
// together in one error.
func Load(lookup LookupFunc) (Config, error) {
	p := &parser{lookup: lookup}
	c := Config{
		Env:           p.oneOf("PONNOKHOJ_ENV", "dev", "prod"),
		HTTPAddr:      p.str("HTTP_ADDR"),
		MetricsAddr:   p.str("METRICS_ADDR"),
		PublicBaseURL: p.httpURL("PUBLIC_BASE_URL", false),
		DatabaseURL:   p.str("DATABASE_URL"),
		DBMaxConns:    p.integer("DB_MAX_CONNS", 2, 200),
		LogLevel:      p.oneOf("LOG_LEVEL", "info", "debug", "warn", "error"),

		ShutdownDrainDelay: p.duration("SHUTDOWN_DRAIN_DELAY", 0, 2*time.Minute),
		ShutdownTimeout:    p.duration("SHUTDOWN_TIMEOUT", time.Second, 5*time.Minute),

		AppSecret:    p.str("APP_SECRET"),
		CookieSecure: p.boolean("COOKIE_SECURE"),
		SessionTTL:   p.duration("SESSION_TTL", 5*time.Minute, 90*24*time.Hour),

		AutoVerifyShops:     p.boolean("AUTO_VERIFY_SHOPS"),
		ReportHideThreshold: p.integer("REPORT_HIDE_THRESHOLD", 1, 1000),

		SearchDefaultRadiusM: p.integer("SEARCH_DEFAULT_RADIUS_M", 100, 100_000),
		SearchMaxRadiusM:     p.integer("SEARCH_MAX_RADIUS_M", 100, 100_000),
		ListingFreshFor:      p.duration("LISTING_FRESH_FOR", time.Hour, 365*24*time.Hour),
		ListingStaleFor:      p.duration("LISTING_STALE_FOR", time.Hour, 2*365*24*time.Hour),

		RateLimitSearchPerMin:   p.integer("RATE_LIMIT_SEARCH_PER_MIN", 1, 100_000),
		RateLimitEstimatePerMin: p.integer("RATE_LIMIT_ESTIMATE_PER_MIN", 1, 100_000),
		RateLimitAuthPerMin:     p.integer("RATE_LIMIT_AUTH_PER_MIN", 1, 100_000),
		RateLimitReportPerMin:   p.integer("RATE_LIMIT_REPORT_PER_MIN", 1, 100_000),

		CSVMaxBytes: int64(p.integer("CSV_MAX_BYTES", 1024, 50<<20)),
		CSVMaxRows:  p.integer("CSV_MAX_ROWS", 1, 100_000),

		MapTileURL:         p.str("MAP_TILE_URL"),
		MapTileAttribution: p.str("MAP_TILE_ATTRIBUTION"),

		SourcesEnabled:  p.list("SOURCES_ENABLED"),
		DemoShopEnabled: p.boolean("DEMO_SHOP_ENABLED"),
		DemoShopBaseURL: p.httpURL("DEMO_SHOP_BASE_URL", false),
		OfferCacheTTL:   p.duration("OFFER_CACHE_TTL", time.Minute, 30*24*time.Hour),
		OfferMaxAge:     p.duration("OFFER_MAX_AGE", time.Hour, 365*24*time.Hour),

		EstimateTTL:          p.duration("ESTIMATE_TTL", time.Minute, 30*24*time.Hour),
		EstimateMinEvidence:  p.integer("ESTIMATE_MIN_EVIDENCE", 1, 100),
		EstimateEnvelopeLow:  p.float("ESTIMATE_ENVELOPE_LOW", 0.01, 1),
		EstimateEnvelopeHigh: p.float("ESTIMATE_ENVELOPE_HIGH", 1, 100),

		LLMEnabled:   p.boolean("LLM_ENABLED"),
		LLMBaseURL:   p.httpURL("LLM_BASE_URL", true),
		LLMModel:     p.str("LLM_MODEL"),
		LLMAPIKey:    p.str("LLM_API_KEY"),
		LLMMaxSteps:  p.integer("LLM_MAX_STEPS", 1, 20),
		LLMTimeout:   p.duration("LLM_TIMEOUT", time.Second, 5*time.Minute),
		LLMMaxTokens: p.integer("LLM_MAX_TOKENS", 64, 32_768),

		TracingEnabled:    p.boolean("OTEL_TRACING_ENABLED"),
		WorkerConcurrency: p.integer("WORKER_CONCURRENCY", 1, 200),
	}

	// LOG_FORMAT's default depends on the environment.
	if v, ok := lookup("LOG_FORMAT"); ok && strings.TrimSpace(v) != "" {
		c.LogFormat = p.oneOf("LOG_FORMAT", "json", "text")
	} else if c.Env == "prod" {
		c.LogFormat = "json"
	} else {
		c.LogFormat = "text"
	}

	for _, cidr := range p.list("TRUSTED_PROXIES") {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			p.fail("TRUSTED_PROXIES", fmt.Sprintf("contains %q, which is not a CIDR", cidr))
			continue
		}
		c.TrustedProxies = append(c.TrustedProxies, prefix.Masked())
	}

	if c.AppSecret != "" && len(c.AppSecret) < 32 {
		p.fail("APP_SECRET", "must be at least 32 characters")
	}
	if c.SearchDefaultRadiusM > c.SearchMaxRadiusM {
		p.fail("SEARCH_DEFAULT_RADIUS_M", "must not exceed SEARCH_MAX_RADIUS_M")
	}
	if c.ListingFreshFor > c.ListingStaleFor {
		p.fail("LISTING_FRESH_FOR", "must not exceed LISTING_STALE_FOR")
	}
	if c.OfferCacheTTL > c.OfferMaxAge {
		p.fail("OFFER_CACHE_TTL", "must not exceed OFFER_MAX_AGE")
	}
	if !strings.Contains(c.MapTileURL, "{z}") || !strings.Contains(c.MapTileURL, "{x}") || !strings.Contains(c.MapTileURL, "{y}") {
		p.fail("MAP_TILE_URL", "must contain {z}, {x} and {y}")
	} else if _, err := c.MapTileOrigin(); err != nil {
		p.fail("MAP_TILE_URL", "must be an http(s) URL template")
	}
	if c.LLMEnabled && (c.LLMBaseURL == "" || c.LLMModel == "") {
		p.fail("LLM_ENABLED", "requires LLM_BASE_URL and LLM_MODEL")
	}
	if c.Env == "prod" {
		if !c.CookieSecure {
			p.fail("COOKIE_SECURE", "must be true in prod")
		}
		if c.AutoVerifyShops {
			p.fail("AUTO_VERIFY_SHOPS", "must be false in prod")
		}
		if c.DemoShopEnabled {
			p.fail("DEMO_SHOP_ENABLED", "must be false in prod")
		}
	}

	if len(p.errs) > 0 {
		sort.Strings(p.errs)
		return Config{}, errors.New("invalid configuration:\n  - " + strings.Join(p.errs, "\n  - "))
	}
	return c, nil
}

// MapTileOrigin returns the origin pattern of the tile server for the
// Content-Security-Policy img-src directive. A "{s}" subdomain placeholder
// becomes a wildcard.
func (c Config) MapTileOrigin() (string, error) {
	raw := strings.NewReplacer("{s}", "sub", "{z}", "0", "{x}", "0", "{y}", "0", "{r}", "").Replace(c.MapTileURL)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("config: tile URL is not http(s)")
	}
	host := u.Host
	if strings.Contains(c.MapTileURL, "{s}.") {
		host = "*." + strings.TrimPrefix(host, "sub.")
	}
	return u.Scheme + "://" + host, nil
}

// SourceEnabled reports whether the feature flag for a source is on.
func (c Config) SourceEnabled(id string) bool {
	for _, s := range c.SourcesEnabled {
		if s == id {
			return true
		}
	}
	return false
}

// Redacted returns name=value pairs of the effective configuration with
// secrets replaced, for `ponnokhoj config check`.
func (c Config) Redacted(lookup LookupFunc) []string {
	out := make([]string, 0, len(Vars))
	for _, v := range Vars {
		val, ok := lookup(v.Name)
		val = strings.TrimSpace(val)
		switch {
		case v.Secret && ok && val != "":
			val = "[redacted]"
		case !ok || val == "":
			val = v.Default
			if v.Name == "LOG_FORMAT" {
				val = c.LogFormat
			}
		}
		out = append(out, v.Name+"="+val)
	}
	return out
}

// MarkdownTable renders Vars as the README's configuration reference. The
// README test compares the two, so the table cannot drift from the code
// (regenerate: PONNOKHOJ_UPDATE_README=1 go test ./internal/config -run Readme).
func MarkdownTable() string {
	var b strings.Builder
	b.WriteString("| Variable | Default | Description |\n|---|---|---|\n")
	for _, v := range Vars {
		def := "`" + v.Default + "`"
		switch {
		case v.Required:
			def = "**required**"
		case v.Default == "":
			def = "—"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", v.Name, def, v.Description)
	}
	return b.String()
}
