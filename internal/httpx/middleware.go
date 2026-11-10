package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares so that the first one is outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type ctxKey int

const (
	ctxRequestInfo ctxKey = iota
)

// requestInfo is shared by pointer between the outer observing middleware and
// the route wrapper deep inside the mux, so the outer layer learns which
// route pattern matched.
type requestInfo struct {
	id      string
	pattern string
}

// RequestID returns the request's ID, or "" outside a request.
func RequestID(ctx context.Context) string {
	if info, ok := ctx.Value(ctxRequestInfo).(*requestInfo); ok {
		return info.id
	}
	return ""
}

// Router is an http.ServeMux that records the matched route pattern for
// logging and metrics (bounded label cardinality).
type Router struct {
	mux *http.ServeMux
}

// NewRouter returns an empty router.
func NewRouter() *Router { return &Router{mux: http.NewServeMux()} }

// Handle registers a handler for a method/path pattern such as
// "GET /shops/{id}", wrapped by the given per-route middlewares.
func (rt *Router) Handle(pattern string, h http.Handler, mws ...Middleware) {
	h = Chain(h, mws...)
	rt.mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info, ok := r.Context().Value(ctxRequestInfo).(*requestInfo); ok {
			info.pattern = pattern
		}
		h.ServeHTTP(w, r)
	}))
}

// HandleFunc is Handle for a function.
func (rt *Router) HandleFunc(pattern string, h http.HandlerFunc, mws ...Middleware) {
	rt.Handle(pattern, h, mws...)
}

// ServeHTTP implements http.Handler.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) { rt.mux.ServeHTTP(w, r) }

// statusRecorder captures the response status for logs and metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap supports http.ResponseController.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Observer records a finished request (metrics).
type Observer interface {
	ObserveHTTP(route, method string, status int, elapsed time.Duration)
}

// Observe assigns a request ID, logs the request, records metrics and opens a
// tracing span. It logs the URL path only: query strings can carry shopper
// coordinates and must never reach the logs.
func Observe(log *slog.Logger, obs Observer, tracer trace.Tracer, clientIP func(*http.Request) string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			info := &requestInfo{id: requestIDFrom(r)}
			ctx := context.WithValue(r.Context(), ctxRequestInfo, info)
			ctx, span := tracer.Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer))
			defer span.End()

			w.Header().Set("X-Request-ID", info.id)
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r.WithContext(ctx))

			route := info.pattern
			if route == "" {
				route = "unmatched"
			}
			elapsed := time.Since(start)
			span.SetName(route)
			span.SetAttributes(attribute.Int("http.response.status_code", rec.status), attribute.String("http.route", route))
			if obs != nil {
				obs.ObserveHTTP(route, r.Method, rec.status, elapsed)
			}
			level := slog.LevelInfo
			switch {
			case rec.status >= 500:
				level = slog.LevelError
			case route == "GET /livez" || route == "GET /readyz":
				level = slog.LevelDebug // probes would drown everything else
			}
			log.LogAttrs(ctx, level, "http request",
				slog.String("request_id", info.id),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("route", route),
				slog.Int("status", rec.status),
				slog.Duration("duration", elapsed),
				slog.String("client_ip", clientIP(r)),
			)
		})
	}
}

// requestIDFrom reuses a sane inbound X-Request-ID (set by the gateway) or
// generates one.
func requestIDFrom(r *http.Request) string {
	if id := r.Header.Get("X-Request-ID"); id != "" && len(id) <= 64 && isToken(id) {
		return id
	}
	var b [12]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails on supported platforms
	return hex.EncodeToString(b[:])
}

func isToken(s string) bool {
	for _, c := range s {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}

// Recover turns a handler panic into a 500 and logs the stack. It re-panics
// http.ErrAbortHandler, which net/http uses to abort a response on purpose.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer recoverPanic(log, w, r)
			next.ServeHTTP(w, r)
		})
	}
}

// recoverPanic is deferred by Recover; recover() works because it is called
// directly by the deferred function.
func recoverPanic(log *slog.Logger, w http.ResponseWriter, r *http.Request) {
	v := recover()
	if v == nil {
		return
	}
	if err, ok := v.(error); ok && err == http.ErrAbortHandler { //nolint:errorlint // net/http documents identity comparison with this sentinel
		panic(v)
	}
	log.ErrorContext(r.Context(), "panic in handler",
		"request_id", RequestID(r.Context()), "panic", v, "stack", string(debug.Stack()))
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

// Timeout bounds how long a handler may run.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, d, "request timed out")
	}
}

// BodyLimit caps the request body for every request. Routes that accept
// uploads apply their own, larger, explicit limit on top of the reader.
func BodyLimit(maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityConfig configures the security headers.
type SecurityConfig struct {
	// TileOrigin is the map tile server origin allowed in img-src.
	TileOrigin string
	// HSTS enables Strict-Transport-Security (prod, behind TLS).
	HSTS bool
}

// SecurityHeaders sets the Content-Security-Policy and related headers. The
// CSP allows only self-hosted scripts and styles: no inline script, no eval.
func SecurityHeaders(cfg SecurityConfig) Middleware {
	img := "img-src 'self' data:"
	if cfg.TileOrigin != "" {
		img += " " + cfg.TileOrigin
	}
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		img,
		"connect-src 'self'",
		"font-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Permissions-Policy", "geolocation=(self), camera=(), microphone=()")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			if cfg.HSTS {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CrossOrigin rejects cross-origin state-changing requests using net/http's
// built-in CrossOriginProtection (Sec-Fetch-Site / Origin checks). Safe
// methods and non-browser clients are unaffected.
func CrossOrigin() Middleware {
	cop := http.NewCrossOriginProtection()
	return cop.Handler
}

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the client went away; nothing to do
}

// ErrorBody is the JSON error envelope of the API.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes one API error.
type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteError writes the JSON error envelope.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message, RequestID: RequestID(r.Context())}})
}
