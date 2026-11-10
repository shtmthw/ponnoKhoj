// Package jobs defines the River background jobs: refreshing online offers,
// computing estimates, and periodic maintenance. River stores jobs in
// Postgres, so there is no extra broker to run.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/shtmthw/ponnoKhoj/internal/estimate"
	"github.com/shtmthw/ponnoKhoj/internal/observability"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// activeStates makes a job unique among jobs that have not finished yet. A
// completed job must not block the next refresh of the same product.
var activeStates = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
	rivertype.JobStateRetryable, rivertype.JobStateScheduled,
}

// RefreshOffersArgs refreshes one product's online offers from every
// enabled source.
type RefreshOffersArgs struct {
	ProductID int64 `json:"product_id"`
}

// Kind implements river.JobArgs.
func (RefreshOffersArgs) Kind() string { return "refresh_offers" }

// InsertOpts deduplicates identical in-flight refreshes.
func (RefreshOffersArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeStates}}
}

// EstimateArgs computes the estimate for (product, cell, radius bucket).
type EstimateArgs struct {
	ProductID int64  `json:"product_id"`
	Cell      string `json:"cell"`
	RadiusM   int    `json:"radius_m"`
}

// Kind implements river.JobArgs.
func (EstimateArgs) Kind() string { return "estimate" }

// InsertOpts deduplicates identical in-flight estimate requests.
func (EstimateArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeStates}}
}

// MaintenanceArgs runs housekeeping: expired sessions and estimates are
// deleted and stale online offers are queued for refresh.
type MaintenanceArgs struct{}

// Kind implements river.JobArgs.
func (MaintenanceArgs) Kind() string { return "maintenance" }

// InsertOpts keeps a single maintenance run queued at a time.
func (MaintenanceArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByState: activeStates}}
}

// Enqueuer inserts jobs. The web process uses an insert-only River client.
type Enqueuer struct {
	client *river.Client[pgx.Tx]
}

// NewEnqueuer builds an insert-only client: it works no queues.
func NewEnqueuer(pool *pgxpool.Pool, log *slog.Logger) (*Enqueuer, error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: log})
	if err != nil {
		return nil, fmt.Errorf("jobs: insert-only client: %w", err)
	}
	return &Enqueuer{client: client}, nil
}

// EnqueueRefreshOffers queues an offers refresh (a no-op when one is
// already in flight for the product).
func (e *Enqueuer) EnqueueRefreshOffers(ctx context.Context, productID int64) error {
	if _, err := e.client.Insert(ctx, RefreshOffersArgs{ProductID: productID}, nil); err != nil {
		return fmt.Errorf("jobs: enqueue refresh_offers: %w", err)
	}
	return nil
}

// EnqueueEstimate queues an estimate (a no-op when an identical one is
// already in flight).
func (e *Enqueuer) EnqueueEstimate(ctx context.Context, productID int64, cell string, radiusM int) error {
	if _, err := e.client.Insert(ctx, EstimateArgs{ProductID: productID, Cell: cell, RadiusM: radiusM}, nil); err != nil {
		return fmt.Errorf("jobs: enqueue estimate: %w", err)
	}
	return nil
}

// WorkerDeps is what the workers need.
type WorkerDeps struct {
	Store     *store.Store
	Refresher *Refresher
	Estimates *estimate.Service
	Metrics   *observability.Metrics
	Log       *slog.Logger
	// OfferCacheTTL and OfferMaxAge drive the scheduled offer refresh.
	OfferCacheTTL time.Duration
	OfferMaxAge   time.Duration
	Concurrency   int
}

// MaintenanceInterval is how often housekeeping runs.
const MaintenanceInterval = 15 * time.Minute

// Worker runs the background jobs.
type Worker struct {
	client *river.Client[pgx.Tx]
	log    *slog.Logger
}

// NewWorker builds the River client that works the queues.
func NewWorker(deps WorkerDeps) (*Worker, error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &refreshWorker{deps: deps})
	river.AddWorker(workers, &estimateWorker{deps: deps})
	river.AddWorker(workers, &maintenanceWorker{deps: deps})
	client, err := river.NewClient(riverpgxv5.New(deps.Store.Pool), &river.Config{
		Logger:     deps.Log,
		Workers:    workers,
		Queues:     map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: deps.Concurrency}},
		JobTimeout: 2 * time.Minute,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(MaintenanceInterval),
				func() (river.JobArgs, *river.InsertOpts) { return MaintenanceArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true},
			),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: worker client: %w", err)
	}
	return &Worker{client: client, log: deps.Log}, nil
}

// Start begins fetching and working jobs. The context must outlive the
// shutdown signal: on SIGTERM the worker has to keep running long enough to
// finish in-flight jobs, so pass a context that is not cancelled by it.
func (w *Worker) Start(ctx context.Context) error {
	if err := w.client.Start(ctx); err != nil {
		return fmt.Errorf("jobs: start worker: %w", err)
	}
	return nil
}

// Stop shuts the worker down gracefully: it stops fetching new jobs and
// waits up to timeout for in-flight jobs to finish. Jobs still running at
// the deadline have their contexts cancelled, which makes River release them
// to be retried by another worker. It reports whether the stop was clean.
func (w *Worker) Stop(timeout time.Duration) bool {
	stopCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := w.client.Stop(stopCtx); err == nil {
		return true
	}
	w.log.Warn("shutdown: jobs still running at the deadline, cancelling them")
	hardCtx, hardCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hardCancel()
	if err := w.client.StopAndCancel(hardCtx); err != nil {
		w.log.Error("shutdown: worker did not stop cleanly", "error", err)
	}
	return false
}

func observe(m *observability.Metrics, kind string, err error) {
	if m == nil {
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	m.JobsCompleted.WithLabelValues(kind, outcome).Inc()
}

type refreshWorker struct {
	river.WorkerDefaults[RefreshOffersArgs]
	deps WorkerDeps
}

func (w *refreshWorker) Work(ctx context.Context, job *river.Job[RefreshOffersArgs]) error {
	err := w.deps.Refresher.Refresh(ctx, job.Args.ProductID)
	observe(w.deps.Metrics, job.Kind, err)
	return err
}

type estimateWorker struct {
	river.WorkerDefaults[EstimateArgs]
	deps WorkerDeps
}

// Timeout gives estimates room for an LLM refinement (LLM_TIMEOUT is
// validated to at most five minutes) on top of evidence gathering.
func (w *estimateWorker) Timeout(*river.Job[EstimateArgs]) time.Duration { return 6 * time.Minute }

func (w *estimateWorker) Work(ctx context.Context, job *river.Job[EstimateArgs]) error {
	err := w.deps.Estimates.Run(ctx, job.Args.ProductID, job.Args.Cell, job.Args.RadiusM)
	observe(w.deps.Metrics, job.Kind, err)
	return err
}

type maintenanceWorker struct {
	river.WorkerDefaults[MaintenanceArgs]
	deps WorkerDeps
}

func (w *maintenanceWorker) Work(ctx context.Context, job *river.Job[MaintenanceArgs]) error {
	err := w.run(ctx)
	observe(w.deps.Metrics, job.Kind, err)
	return err
}

func (w *maintenanceWorker) run(ctx context.Context) error {
	sessions, err := w.deps.Store.DeleteExpiredSessions(ctx)
	if err != nil {
		return fmt.Errorf("jobs: delete expired sessions: %w", err)
	}
	estimates, err := w.deps.Store.DeleteExpiredEstimates(ctx)
	if err != nil {
		return fmt.Errorf("jobs: delete expired estimates: %w", err)
	}
	queued := 0
	if w.deps.Refresher.HasSources() {
		now := time.Now()
		staleBefore := now.Add(-w.deps.OfferCacheTTL)
		stale, err := w.deps.Store.ListProductsNeedingRefresh(ctx, db.ListProductsNeedingRefreshParams{
			RequestedSince: now.Add(-w.deps.OfferMaxAge), StaleBefore: &staleBefore, MaxResults: 50,
		})
		if err != nil {
			return fmt.Errorf("jobs: list stale products: %w", err)
		}
		client := river.ClientFromContext[pgx.Tx](ctx)
		for _, productID := range stale {
			if _, err := client.Insert(ctx, RefreshOffersArgs{ProductID: productID}, nil); err != nil {
				return fmt.Errorf("jobs: enqueue scheduled refresh: %w", err)
			}
			queued++
		}
	}
	w.deps.Log.InfoContext(ctx, "maintenance done", "expired_sessions", sessions, "expired_estimates", estimates, "refreshes_queued", queued)
	return nil
}
