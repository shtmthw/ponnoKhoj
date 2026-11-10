package main

import (
	"context"
	"io"
	"os"

	"github.com/shtmthw/ponnoKhoj/internal/app"
	"github.com/shtmthw/ponnoKhoj/internal/config"
	"github.com/shtmthw/ponnoKhoj/internal/httpx"
)

func newRuntime(ctx context.Context, stderr io.Writer, service string) (*app.Runtime, error) {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	return app.NewRuntime(ctx, cfg, stderr, service)
}

func cmdServe(ctx context.Context, stderr io.Writer) error {
	rt, err := newRuntime(ctx, stderr, "ponnokhoj-web")
	if err != nil {
		return err
	}
	// Last shutdown step: flush tracing and close the database pool, after
	// the HTTP server has drained.
	defer rt.Close(ctx)

	// Best effort: the worker and seeder also sync, and before migrations
	// have run the table does not exist yet (readiness reports that).
	if err := rt.SyncSources(ctx); err != nil {
		rt.Log.Warn("could not sync online sources (has the schema been migrated?)", "error", err)
	}
	ready := &httpx.Readiness{}
	handler, metrics, err := rt.Handlers(ready) //nolint:contextcheck // Handlers starts the detached search-stats flusher; Runtime.Close stops it
	if err != nil {
		return err
	}
	srv, err := httpx.NewServer(ctx, rt.Log, httpx.ServerConfig{
		Addr: rt.Cfg.HTTPAddr, MetricsAddr: rt.Cfg.MetricsAddr,
		DrainDelay: rt.Cfg.ShutdownDrainDelay, ShutdownTimeout: rt.Cfg.ShutdownTimeout,
	}, handler, metrics, ready)
	if err != nil {
		return err
	}
	return srv.Run(ctx)
}
