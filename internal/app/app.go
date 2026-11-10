// Package app wires ponnoKhoj's services together. The CLI's serve and
// worker commands and the integration tests all build the application here,
// so tests exercise exactly what runs in production.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/api"
	"github.com/shtmthw/ponnoKhoj/internal/auth"
	"github.com/shtmthw/ponnoKhoj/internal/config"
	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/estimate"
	"github.com/shtmthw/ponnoKhoj/internal/httpx"
	"github.com/shtmthw/ponnoKhoj/internal/jobs"
	"github.com/shtmthw/ponnoKhoj/internal/llm"
	"github.com/shtmthw/ponnoKhoj/internal/migrations"
	"github.com/shtmthw/ponnoKhoj/internal/moderation"
	"github.com/shtmthw/ponnoKhoj/internal/observability"
	"github.com/shtmthw/ponnoKhoj/internal/pricefinder"
	"github.com/shtmthw/ponnoKhoj/internal/sources"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
	"github.com/shtmthw/ponnoKhoj/internal/vendor"
	"github.com/shtmthw/ponnoKhoj/internal/version"
	"github.com/shtmthw/ponnoKhoj/internal/web"
)

// Runtime is what both long-running commands (and the integration tests)
// set up first: configuration, logging, metrics, the database and the
// online source registry.
type Runtime struct {
	Cfg      config.Config
	Log      *slog.Logger
	Metrics  *observability.Metrics
	Store    *store.Store
	Registry *sources.Registry
	shutdown []func(context.Context) error
}

// NewRuntime builds the runtime from an already validated configuration.
func NewRuntime(ctx context.Context, cfg config.Config, stderr io.Writer, service string) (*Runtime, error) {
	log := observability.NewLogger(stderr, cfg.LogFormat, cfg.LogLevel).With("service", service)
	slog.SetDefault(log)
	log.Info("starting", "version", version.Version, "commit", version.Commit, "env", cfg.Env)

	traceShutdown, err := observability.SetupTracing(ctx, cfg.TracingEnabled, service)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL, int32(cfg.DBMaxConns)) //nolint:gosec // validated to [2, 200]
	if err != nil {
		return nil, err
	}
	registry, err := sources.NewRegistry(sources.RegistryConfig{
		Enabled: cfg.SourceEnabled, ContactURL: cfg.PublicBaseURL, DemoShopBaseURL: cfg.DemoShopBaseURL,
	}, cfg.SourcesEnabled)
	if err != nil {
		st.Close()
		return nil, err
	}
	return &Runtime{
		Cfg: cfg, Log: log, Metrics: observability.NewMetrics(), Store: st, Registry: registry,
		shutdown: []func(context.Context) error{traceShutdown},
	}, nil
}

// Close flushes tracing and closes the pool: the last shutdown step.
// ctx may already be cancelled (shutdown); the steps get their own deadline.
func (rt *Runtime) Close(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, fn := range rt.shutdown {
		if err := fn(ctx); err != nil {
			rt.Log.Warn("shutdown step failed", "error", err)
		}
	}
	rt.Store.Close()
	rt.Log.Info("shutdown: database pool closed")
}

// statsFlushInterval is how often aggregated search counts are written.
const statsFlushInterval = 30 * time.Second

// flushSearchStats writes the finder's search counts every
// statsFlushInterval and once more during shutdown, before the pool closes.
func (rt *Runtime) flushSearchStats(finder *pricefinder.Service) {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(statsFlushInterval)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := finder.FlushStats(ctx); err != nil {
					rt.Log.Warn("could not record search stats", "error", err)
				}
				cancel()
			case <-stop:
				return
			}
		}
	}()
	rt.shutdown = append(rt.shutdown, func(ctx context.Context) error {
		close(stop)
		<-done
		return finder.FlushStats(ctx)
	})
}

// SyncSources records the source catalog and feature flags in the database.
func (rt *Runtime) SyncSources(ctx context.Context) error {
	return rt.Registry.Sync(ctx, func(ctx context.Context, row sources.SourceRow) error {
		return rt.Store.UpsertSource(ctx, db.UpsertSourceParams{
			ID: row.ID, Name: row.Name, BaseUrl: row.BaseURL, AccessMethod: row.AccessMethod,
			ComplianceStatus: row.Compliance, Enabled: row.Enabled,
		})
	})
}

// ReadyCheck passes when the database answers and its schema is at least
// the version this binary expects. "At least" matters: migrations follow
// expand/contract, so during a rollout the previous release must stay ready
// against the already-migrated schema.
func (rt *Runtime) ReadyCheck(ctx context.Context) error {
	v, err := rt.Store.SchemaVersion(ctx)
	if err != nil {
		return errors.New("database unreachable")
	}
	if want := migrations.ExpectedVersion(); v < want {
		return fmt.Errorf("schema version %d is older than expected %d", v, want)
	}
	return nil
}

// EstimateParams maps the configuration onto the estimator parameters.
func (rt *Runtime) EstimateParams() estimate.Params {
	p := estimate.DefaultParams()
	p.MinEvidence = rt.Cfg.EstimateMinEvidence
	p.FreshFor = rt.Cfg.ListingFreshFor
	p.ListingMaxAge = rt.Cfg.ListingStaleFor
	p.OfferMaxAge = rt.Cfg.OfferMaxAge
	p.EnvelopeLow, p.EnvelopeHigh = rt.Cfg.EstimateEnvelopeLow, rt.Cfg.EstimateEnvelopeHigh
	return p
}

// EstimateService builds the estimate service. Only the worker passes
// withLLM: the web process merely looks estimates up. llmClient overrides the
// HTTP client (tests pass a fake); nil uses the configured endpoint.
func (rt *Runtime) EstimateService(withLLM bool, llmClient llm.Client) *estimate.Service {
	var agent *estimate.Agent
	if withLLM && rt.Cfg.LLMEnabled {
		if llmClient == nil {
			llmClient = llm.NewHTTPClient(rt.Cfg.LLMBaseURL, rt.Cfg.LLMAPIKey)
		}
		agent = estimate.NewAgent(llmClient, estimate.AgentConfig{
			Model: rt.Cfg.LLMModel, MaxSteps: rt.Cfg.LLMMaxSteps, Timeout: rt.Cfg.LLMTimeout, MaxTokens: rt.Cfg.LLMMaxTokens,
		})
	}
	return estimate.NewService(rt.Store, rt.EstimateParams(), rt.Cfg.EstimateTTL, agent, rt.Metrics, rt.Log)
}

// Handlers builds the main HTTP handler (web UI, JSON API, health endpoints,
// full middleware chain) and the metrics handler.
func (rt *Runtime) Handlers(ready *httpx.Readiness) (main, metrics http.Handler, err error) {
	cfg := rt.Cfg
	authSvc, err := auth.NewService(rt.Store, auth.Config{SessionTTL: cfg.SessionTTL, CookieSecure: cfg.CookieSecure})
	if err != nil {
		return nil, nil, err
	}
	enqueuer, err := jobs.NewEnqueuer(rt.Store.Pool, rt.Log)
	if err != nil {
		return nil, nil, err
	}
	freshness := domain.FreshnessPolicy{FreshFor: cfg.ListingFreshFor, StaleFor: cfg.ListingStaleFor}
	finder := pricefinder.NewService(rt.Store, rt.EstimateService(false, nil), enqueuer, pricefinder.Config{
		DefaultRadiusM: cfg.SearchDefaultRadiusM, MaxRadiusM: cfg.SearchMaxRadiusM, Freshness: freshness,
		OfferCacheTTL: cfg.OfferCacheTTL, OfferMaxAge: cfg.OfferMaxAge, SourcesEnabled: len(rt.Registry.Enabled()), SearchSourcesEnabled: rt.Registry.SearchersEnabled(),
	}, rt.Metrics, rt.Log)
	rt.flushSearchStats(finder)
	vendorSvc := vendor.NewService(rt.Store, vendor.Config{AutoVerifyShops: cfg.AutoVerifyShops, Freshness: freshness, CSVMaxRows: cfg.CSVMaxRows})
	moderationSvc := moderation.NewService(rt.Store, cfg.ReportHideThreshold, cfg.AppSecret)

	ipResolver := httpx.NewClientIPResolver(cfg.TrustedProxies)
	searchLimit := httpx.NewRateLimiter(cfg.RateLimitSearchPerMin)
	estimateLimit := httpx.NewRateLimiter(cfg.RateLimitEstimatePerMin)

	webSrv, err := web.New(web.Deps{
		Cfg: cfg, Store: rt.Store, Auth: authSvc, Finder: finder, Vendor: vendorSvc, Moderation: moderationSvc,
		Log: rt.Log, ClientIP: ipResolver.ClientIP,
		Limits: web.Limits{
			Search: searchLimit, Estimate: estimateLimit,
			// The estimate panel polls every 2 s while pending; give it its
			// own budget so polling cannot starve searching.
			Poll:   httpx.NewRateLimiter(max(cfg.RateLimitSearchPerMin, 60)),
			Auth:   httpx.NewRateLimiter(cfg.RateLimitAuthPerMin),
			Report: httpx.NewRateLimiter(cfg.RateLimitReportPerMin),
		},
	})
	if err != nil {
		return nil, nil, err
	}
	apiSrv := api.New(api.Deps{
		Cfg: cfg, Store: rt.Store, Finder: finder, Log: rt.Log, ClientIP: ipResolver.ClientIP,
		SearchLimit: searchLimit, EstimateLimit: estimateLimit,
	})

	router := httpx.NewRouter()
	router.Handle("GET /livez", httpx.LivezHandler())
	router.Handle("GET /readyz", httpx.ReadyzHandler(ready, rt.ReadyCheck))
	apiSrv.Register(router)
	webSrv.Register(router)

	tileOrigin, err := cfg.MapTileOrigin()
	if err != nil {
		return nil, nil, err
	}
	handler := httpx.Chain(router,
		httpx.Observe(rt.Log, rt.Metrics, observability.Tracer(), ipResolver.ClientIP),
		httpx.Recover(rt.Log),
		httpx.SecurityHeaders(httpx.SecurityConfig{TileOrigin: tileOrigin, HSTS: cfg.Env == "prod"}),
		httpx.CrossOrigin(),
		// Global body cap: the CSV upload limit plus form overhead. Ordinary
		// forms apply a much smaller cap of their own.
		httpx.BodyLimit(cfg.CSVMaxBytes+64<<10),
		httpx.Timeout(15*time.Second),
	)
	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", rt.Metrics.Handler())

	return handler, metricsMux, nil
}

// WorkerClient builds the River client that works the queues. llmClient
// overrides the configured LLM endpoint (tests); nil uses the configuration.
func (rt *Runtime) WorkerClient(llmClient llm.Client) (*jobs.Worker, error) {
	return jobs.NewWorker(jobs.WorkerDeps{
		Store:     rt.Store,
		Refresher: jobs.NewRefresher(rt.Store, rt.Registry.Enabled(), rt.Metrics, rt.Log),
		Estimates: rt.EstimateService(true, llmClient),
		Metrics:   rt.Metrics, Log: rt.Log,
		OfferCacheTTL: rt.Cfg.OfferCacheTTL, OfferMaxAge: rt.Cfg.OfferMaxAge, Concurrency: rt.Cfg.WorkerConcurrency,
	})
}
