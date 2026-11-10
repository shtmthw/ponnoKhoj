package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func env(overrides map[string]string) LookupFunc {
	base := map[string]string{
		"DATABASE_URL": "postgres://u:supersecretpw@db:5432/ponnokhoj",
		"APP_SECRET":   "0123456789abcdef0123456789abcdef",
	}
	for k, v := range overrides {
		base[k] = v
	}
	return func(name string) (string, bool) {
		v, ok := base[name]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != "dev" || c.HTTPAddr != ":8080" || c.MetricsAddr != ":9090" || c.LogFormat != "text" || c.LogLevel != "info" {
		t.Errorf("basic defaults wrong: %+v", c)
	}
	if c.ShutdownDrainDelay != 5*time.Second || c.ShutdownTimeout != 20*time.Second {
		t.Errorf("shutdown defaults: %v, %v", c.ShutdownDrainDelay, c.ShutdownTimeout)
	}
	// The brief's defaults: 3 km radius, fresh <= 7 days, stale <= 30 days.
	if c.SearchDefaultRadiusM != 3000 || c.SearchMaxRadiusM != 25000 || c.ListingFreshFor != 7*24*time.Hour || c.ListingStaleFor != 30*24*time.Hour {
		t.Errorf("search/freshness defaults wrong: %+v", c)
	}
	if c.EstimateMinEvidence != 3 || c.EstimateEnvelopeLow != 0.5 || c.EstimateEnvelopeHigh != 1.5 || c.LLMMaxSteps != 6 || c.LLMTimeout != 20*time.Second {
		t.Errorf("estimate defaults wrong: %+v", c)
	}
	// Safe by default: secure cookies, nothing auto-verified, every source
	// and the LLM off, tracing off, no trusted proxies.
	if !c.CookieSecure || c.AutoVerifyShops || len(c.SourcesEnabled) != 0 || c.DemoShopEnabled || c.LLMEnabled || c.TracingEnabled || len(c.TrustedProxies) != 0 {
		t.Errorf("unsafe defaults: %+v", c)
	}
	if c.SourceEnabled("demoshop") {
		t.Error("a source is enabled by default")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Parallel()
	c, err := Load(env(map[string]string{
		"PONNOKHOJ_ENV": "prod", "TRUSTED_PROXIES": "10.0.0.0/8, fd00::/8", "SOURCES_ENABLED": " demoshop , other ",
		"LLM_ENABLED": "true", "LLM_BASE_URL": "http://localhost:11434/v1/", "LLM_MODEL": "some-model",
		"MAP_TILE_URL": "https://{s}.tiles.example.org/{z}/{x}/{y}.png", "SHUTDOWN_DRAIN_DELAY": "0s", "LOG_LEVEL": "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.LogFormat != "json" {
		t.Errorf("prod log format = %q, want json", c.LogFormat)
	}
	if len(c.TrustedProxies) != 2 || c.TrustedProxies[0].String() != "10.0.0.0/8" {
		t.Errorf("trusted proxies = %v", c.TrustedProxies)
	}
	if !c.SourceEnabled("demoshop") || !c.SourceEnabled("other") || c.SourceEnabled("nope") {
		t.Errorf("sources = %v", c.SourcesEnabled)
	}
	if c.LLMBaseURL != "http://localhost:11434/v1" {
		t.Errorf("LLM base URL not normalized: %q", c.LLMBaseURL)
	}
	if origin, err := c.MapTileOrigin(); err != nil || origin != "https://*.tiles.example.org" {
		t.Errorf("tile origin = %q, %v", origin, err)
	}
	if c.ShutdownDrainDelay != 0 {
		t.Errorf("drain delay = %v", c.ShutdownDrainDelay)
	}
	c2, _ := Load(env(map[string]string{"LOG_FORMAT": "json"}))
	if c2.LogFormat != "json" {
		t.Error("explicit LOG_FORMAT ignored")
	}
	if origin, _ := c2.MapTileOrigin(); origin != "https://tile.openstreetmap.org" {
		t.Errorf("default tile origin = %q", origin)
	}
}

func TestLoadFailsFastWithEveryProblem(t *testing.T) {
	t.Parallel()
	_, err := Load(func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") || !strings.Contains(err.Error(), "APP_SECRET is required") {
		t.Fatalf("missing required vars: %v", err)
	}
	bad := map[string]string{
		"PONNOKHOJ_ENV":           "staging",
		"DB_MAX_CONNS":            "lots",
		"LOG_LEVEL":               "loud",
		"LOG_FORMAT":              "xml",
		"SHUTDOWN_TIMEOUT":        "soon",
		"APP_SECRET":              "short",
		"TRUSTED_PROXIES":         "10.0.0.1",
		"COOKIE_SECURE":           "maybe",
		"SEARCH_DEFAULT_RADIUS_M": "50000",
		"LISTING_FRESH_FOR":       "1000h",
		"OFFER_CACHE_TTL":         "400h",
		"MAP_TILE_URL":            "https://tiles.example.org/map.png",
		"PUBLIC_BASE_URL":         "ponnokhoj.example",
		"ESTIMATE_ENVELOPE_LOW":   "NaN",
		"ESTIMATE_ENVELOPE_HIGH":  "0.5",
		"LLM_ENABLED":             "true",
		"LLM_BASE_URL":            "ftp://llm",
		"WORKER_CONCURRENCY":      "0",
		"CSV_MAX_ROWS":            "-1",
	}
	_, err = Load(env(bad))
	if err == nil {
		t.Fatal("invalid configuration accepted")
	}
	for name := range bad {
		if name == "LLM_BASE_URL" || name == "LISTING_FRESH_FOR" || name == "OFFER_CACHE_TTL" {
			continue // reported under the name checked below
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %s:\n%v", name, err)
		}
	}
	for _, want := range []string{"LISTING_FRESH_FOR must not exceed", "OFFER_CACHE_TTL must not exceed", "LLM_BASE_URL must be an http(s) URL", "requires LLM_BASE_URL and LLM_MODEL", "at least 32 characters", "not a CIDR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	// The error must never echo secret values.
	if strings.Contains(err.Error(), "supersecretpw") {
		t.Error("configuration error leaks the database password")
	}
}

func TestProdGuards(t *testing.T) {
	t.Parallel()
	_, err := Load(env(map[string]string{"PONNOKHOJ_ENV": "prod", "COOKIE_SECURE": "false", "AUTO_VERIFY_SHOPS": "true", "DEMO_SHOP_ENABLED": "true"}))
	if err == nil {
		t.Fatal("unsafe prod configuration accepted")
	}
	for _, want := range []string{"COOKIE_SECURE must be true in prod", "AUTO_VERIFY_SHOPS must be false in prod", "DEMO_SHOP_ENABLED must be false in prod"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	// The same flags are fine in dev.
	if _, err := Load(env(map[string]string{"COOKIE_SECURE": "false", "AUTO_VERIFY_SHOPS": "yes", "DEMO_SHOP_ENABLED": "1"})); err != nil {
		t.Errorf("dev flags rejected: %v", err)
	}
}

func TestRedacted(t *testing.T) {
	t.Parallel()
	lookup := env(map[string]string{"LLM_API_KEY": "sk-very-secret", "HTTP_ADDR": ":9999"})
	c, err := Load(lookup)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Join(c.Redacted(lookup), "\n")
	for _, secret := range []string{"supersecretpw", "0123456789abcdef0123456789abcdef", "sk-very-secret"} {
		if strings.Contains(out, secret) {
			t.Errorf("redacted output leaks %q", secret)
		}
	}
	for _, want := range []string{"DATABASE_URL=[redacted]", "APP_SECRET=[redacted]", "LLM_API_KEY=[redacted]", "HTTP_ADDR=:9999", "METRICS_ADDR=:9090", "LOG_FORMAT=text", "SEED_ADMIN_PASSWORD="} {
		if !strings.Contains(out, want) {
			t.Errorf("redacted output lacks %q:\n%s", want, out)
		}
	}
}

func TestVarsAreWellFormed(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, v := range Vars {
		if seen[v.Name] {
			t.Errorf("duplicate variable %s", v.Name)
		}
		seen[v.Name] = true
		if v.Description == "" || !regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`).MatchString(v.Name) {
			t.Errorf("variable %q is missing a description or has a bad name", v.Name)
		}
		if v.Required && v.Default != "" {
			t.Errorf("%s is required but has a default", v.Name)
		}
	}
}

// TestReadmeConfigTableMatchesVars keeps the README's configuration
// reference from drifting: every variable must appear in the table with its
// default, and the table must not document variables that do not exist.
func TestReadmeConfigTableMatchesVars(t *testing.T) {
	t.Parallel()
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PONNOKHOJ_UPDATE_README") == "1" {
		const start, end = "<!-- config-table:start -->", "<!-- config-table:end -->"
		i, j := strings.Index(string(readme), start), strings.Index(string(readme), end)
		if i < 0 || j < i {
			t.Fatal("README has no config-table markers")
		}
		readme = []byte(string(readme[:i+len(start)]) + "\n" + MarkdownTable() + string(readme[j:]))
		if err := os.WriteFile("../../README.md", readme, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rowRe := regexp.MustCompile("(?m)^\\| `([A-Z][A-Z0-9_]+)` \\| (.*?) \\| (.*) \\|$")
	rows := map[string][]string{}
	for _, m := range rowRe.FindAllStringSubmatch(string(readme), -1) {
		rows[m[1]] = m[2:]
	}
	for _, v := range Vars {
		row, ok := rows[v.Name]
		if !ok {
			t.Errorf("README configuration table has no row for %s", v.Name)
			continue
		}
		wantDefault := "`" + v.Default + "`"
		switch {
		case v.Required:
			wantDefault = "**required**"
		case v.Default == "":
			wantDefault = "—"
		}
		if row[0] != wantDefault {
			t.Errorf("README default for %s is %q, want %q", v.Name, row[0], wantDefault)
		}
		if row[1] != v.Description {
			t.Errorf("README description for %s differs from config.Vars:\n  readme: %s\n  code:   %s", v.Name, row[1], v.Description)
		}
		delete(rows, v.Name)
	}
	for name := range rows {
		t.Errorf("README documents %s, which the config package does not read", name)
	}
}
