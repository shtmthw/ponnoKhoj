package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds every Prometheus collector the application exports.
type Metrics struct {
	registry *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec

	Searches *prometheus.CounterVec // mode, outcome

	SourceRequests      *prometheus.CounterVec   // source, outcome
	SourceLatency       *prometheus.HistogramVec // source
	SourceErrors        *prometheus.CounterVec   // source, class
	SourceParseFailures *prometheus.CounterVec   // source
	SourceOffers        *prometheus.CounterVec   // source

	Estimates     *prometheus.CounterVec // status, method
	LLMCalls      *prometheus.CounterVec // outcome
	LLMTokens     *prometheus.CounterVec // kind (prompt|completion)
	LLMLatency    prometheus.Histogram
	JobsCompleted *prometheus.CounterVec // kind, outcome
}

// NewMetrics registers all collectors on a fresh registry.
func NewMetrics() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ponnokhoj_http_requests_total", Help: "HTTP requests by route pattern, method and status code.",
		}, []string{"route", "method", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "ponnokhoj_http_request_duration_seconds", Help: "HTTP request latency by route pattern.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 1, 2.5, 5, 10},
		}, []string{"route", "method"}),
		Searches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ponnokhoj_searches_total", Help: "Searches by mode and outcome.",
		}, []string{"mode", "outcome"}),
		SourceRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ponnokhoj_source_requests_total", Help: "Outbound requests to online sources by outcome.",
		}, []string{"source", "outcome"}),
		SourceLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "ponnokhoj_source_request_duration_seconds", Help: "Latency of online source searches.",
			Buckets: prometheus.DefBuckets,
		}, []string{"source"}),
		SourceErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ponnokhoj_source_errors_total", Help: "Online source errors by class.",
		}, []string{"source", "class"}),
		SourceParseFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ponnokhoj_source_parse_failures_total", Help: "Offers dropped because a price or page could not be parsed.",
		}, []string{"source"}),
		SourceOffers: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ponnokhoj_source_offers_total", Help: "Offers found and matched to a product.",
		}, []string{"source"}),
		Estimates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ponnokhoj_estimates_total", Help: "Estimate runs by resulting status and method.",
		}, []string{"status", "method"}),
		LLMCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ponnokhoj_llm_calls_total", Help: "LLM chat completion calls by outcome.",
		}, []string{"outcome"}),
		LLMTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ponnokhoj_llm_tokens_total", Help: "LLM tokens used, by kind.",
		}, []string{"kind"}),
		LLMLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "ponnokhoj_llm_call_duration_seconds", Help: "Latency of LLM chat completion calls.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 40},
		}),
		JobsCompleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ponnokhoj_jobs_total", Help: "Background jobs by kind and outcome.",
		}, []string{"kind", "outcome"}),
	}
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpRequests, m.httpDuration, m.Searches,
		m.SourceRequests, m.SourceLatency, m.SourceErrors, m.SourceParseFailures, m.SourceOffers,
		m.Estimates, m.LLMCalls, m.LLMTokens, m.LLMLatency, m.JobsCompleted,
	)
	return m
}

// Handler serves the metrics in Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

// ObserveHTTP records one finished request. route must be a registered route
// pattern (bounded cardinality), never a raw URL path.
func (m *Metrics) ObserveHTTP(route, method string, status int, elapsed time.Duration) {
	m.httpRequests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(route, method).Observe(elapsed.Seconds())
}

// Gatherer exposes the registry for tests.
func (m *Metrics) Gatherer() prometheus.Gatherer { return m.registry }
