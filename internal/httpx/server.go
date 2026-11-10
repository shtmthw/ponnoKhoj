// Package httpx contains the HTTP server lifecycle, middleware, client-IP
// resolution, rate limiting and response helpers shared by the web UI and the
// JSON API.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// Readiness tracks whether the process should receive traffic.
type Readiness struct {
	shuttingDown atomic.Bool
}

// BeginShutdown makes readiness fail from now on.
func (r *Readiness) BeginShutdown() { r.shuttingDown.Store(true) }

// ShuttingDown reports whether shutdown has begun.
func (r *Readiness) ShuttingDown() bool { return r.shuttingDown.Load() }

// LivezHandler reports that the process is alive. It deliberately does not
// touch the database: a database outage must not cause restart storms.
func LivezHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("ok\n"))
	})
}

// ReadyzHandler reports whether the process can serve: it is not shutting
// down and check (database reachable, schema at the expected version)
// succeeds within a short timeout.
func ReadyzHandler(ready *Readiness, check func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if ready.ShuttingDown() {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := check(ctx); err != nil {
			// The reason goes to the client as a short, non-sensitive string.
			http.Error(w, "not ready: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
}

// ServerConfig configures the listeners and the shutdown sequence.
type ServerConfig struct {
	Addr            string
	MetricsAddr     string
	DrainDelay      time.Duration
	ShutdownTimeout time.Duration
}

// Server runs the main and metrics listeners with graceful shutdown.
type Server struct {
	log       *slog.Logger
	cfg       ServerConfig
	ready     *Readiness
	main      *http.Server
	metrics   *http.Server
	mainLn    net.Listener
	metricsLn net.Listener
}

// NewServer binds both listeners immediately so configuration errors (port in
// use, bad address) surface at startup.
func NewServer(ctx context.Context, log *slog.Logger, cfg ServerConfig, handler, metricsHandler http.Handler, ready *Readiness) (*Server, error) {
	var lc net.ListenConfig
	mainLn, err := lc.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("httpx: listen %s: %w", cfg.Addr, err)
	}
	metricsLn, err := lc.Listen(ctx, "tcp", cfg.MetricsAddr)
	if err != nil {
		_ = mainLn.Close()
		return nil, fmt.Errorf("httpx: listen %s: %w", cfg.MetricsAddr, err)
	}
	newServer := func(h http.Handler) *http.Server {
		return &http.Server{
			Handler:           h,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       90 * time.Second,
			MaxHeaderBytes:    64 << 10,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		}
	}
	return &Server{
		log: log, cfg: cfg, ready: ready,
		main: newServer(handler), metrics: newServer(metricsHandler),
		mainLn: mainLn, metricsLn: metricsLn,
	}, nil
}

// Addr is the bound address of the main listener.
func (s *Server) Addr() string { return s.mainLn.Addr().String() }

// MetricsAddr is the bound address of the metrics listener.
func (s *Server) MetricsAddr() string { return s.metricsLn.Addr().String() }

// Run serves until ctx is cancelled (SIGTERM), then shuts down gracefully:
//
//  1. readiness starts failing immediately;
//  2. wait DrainDelay so the endpoint is removed from load balancers (done
//     in-process because the distroless image has no sleep binary);
//  3. http.Server.Shutdown with the ShutdownTimeout deadline, which stops
//     accepting connections and waits for in-flight requests.
//
// The caller closes the database pool after Run returns.
func (s *Server) Run(ctx context.Context) error {
	errc := make(chan error, 2)
	go func() { errc <- s.main.Serve(s.mainLn) }()
	go func() { errc <- s.metrics.Serve(s.metricsLn) }()
	s.log.Info("http server listening", "addr", s.Addr(), "metrics_addr", s.MetricsAddr())

	select {
	case err := <-errc:
		// A listener died on its own: stop the other one and report it.
		s.ready.BeginShutdown()
		_ = s.main.Close()
		_ = s.metrics.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("httpx: serve: %w", err)
	case <-ctx.Done():
	}

	s.ready.BeginShutdown()
	s.log.Info("shutdown: readiness failing, draining", "drain_delay", s.cfg.DrainDelay.String())
	time.Sleep(s.cfg.DrainDelay)

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownTimeout)
	defer cancel()
	err := s.main.Shutdown(shutdownCtx)
	if err != nil {
		s.log.Error("shutdown: in-flight requests did not finish in time", "error", err)
		_ = s.main.Close()
	}
	if mErr := s.metrics.Shutdown(shutdownCtx); mErr != nil {
		_ = s.metrics.Close()
	}
	s.log.Info("shutdown: http server stopped")
	if err != nil {
		return fmt.Errorf("httpx: shutdown: %w", err)
	}
	return nil
}
