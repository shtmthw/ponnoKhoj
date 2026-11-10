package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/httpx"
)

func cmdWorker(ctx context.Context, stderr io.Writer) error {
	rt, err := newRuntime(ctx, stderr, "ponnokhoj-worker")
	if err != nil {
		return err
	}
	defer rt.Close(ctx)
	cfg := rt.Cfg

	if err := rt.SyncSources(ctx); err != nil {
		return fmt.Errorf("sync online sources (has the schema been migrated?): %w", err)
	}
	rt.Log.Info("online sources", "enabled", len(rt.Registry.Enabled()), "llm_enabled", cfg.LLMEnabled)

	worker, err := rt.WorkerClient(nil)
	if err != nil {
		return err
	}

	// The worker serves no application traffic. Health and metrics share the
	// metrics port, which is cluster-internal.
	ready := &httpx.Readiness{}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", rt.Metrics.Handler())
	mux.Handle("GET /livez", httpx.LivezHandler())
	mux.Handle("GET /readyz", httpx.ReadyzHandler(ready, rt.ReadyCheck))
	health := &http.Server{
		Addr: cfg.MetricsAddr, Handler: mux,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
	}
	healthErr := make(chan error, 1)
	go func() { healthErr <- health.ListenAndServe() }()

	// River must not be tied to the signal context: on SIGTERM it has to
	// keep running long enough to finish in-flight jobs.
	if err := worker.Start(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	rt.Log.Info("worker started", "concurrency", cfg.WorkerConcurrency, "health_addr", cfg.MetricsAddr)

	select {
	case <-ctx.Done():
	case err := <-healthErr:
		rt.Log.Error("health listener failed", "error", err)
	}

	// Graceful stop: readiness fails, River stops fetching and waits for
	// in-flight jobs (see jobs.Worker.Stop); then the pool closes (rt.Close).
	ready.BeginShutdown()
	rt.Log.Info("shutdown: worker stops fetching jobs, waiting for in-flight jobs", "timeout", cfg.ShutdownTimeout.String())
	worker.Stop(cfg.ShutdownTimeout)
	closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer closeCancel()
	_ = health.Shutdown(closeCtx)
	rt.Log.Info("shutdown: worker stopped")
	return nil
}
