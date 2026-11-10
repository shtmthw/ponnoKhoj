package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/matching"
	"github.com/shtmthw/ponnoKhoj/internal/observability"
	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/sources"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// Refresher pulls a product's online offers from the enabled sources,
// matches them to the product and caches the confident matches.
type Refresher struct {
	store   *store.Store
	sources []sources.Source
	metrics *observability.Metrics
	log     *slog.Logger
	now     func() time.Time
}

// NewRefresher builds a refresher over the given enabled sources.
func NewRefresher(st *store.Store, enabled []sources.Source, metrics *observability.Metrics, log *slog.Logger) *Refresher {
	return &Refresher{store: st, sources: enabled, metrics: metrics, log: log, now: time.Now}
}

// HasSources reports whether any online source is enabled.
func (r *Refresher) HasSources() bool { return len(r.sources) > 0 }

// Refresh queries every enabled source for one product. A failing source is
// recorded (health, metrics, the product's failed-source list) and does not
// fail the job: the fetch client has already retried, and hammering a
// struggling site through job retries would be impolite. The next scheduled
// or shopper-triggered refresh tries again.
func (r *Refresher) Refresh(ctx context.Context, productID int64) error {
	prod, err := r.store.GetProduct(ctx, productID)
	if store.IsNotFound(err) {
		return nil // the product was removed after the job was queued
	}
	if err != nil {
		return fmt.Errorf("refresh: load product: %w", err)
	}
	mp := matching.Product{
		NameEn: prod.NameEn, NameBn: prod.NameBn, Brand: prod.Brand, Model: prod.Model,
		Quantity: domain.Quantity{Value: prod.Quantity, Unit: domain.Unit(prod.Unit)},
	}
	query := search.ParseQuery(strings.TrimSpace(prod.Brand + " " + prod.NameEn))

	failed := []string{}
	asked := 0
	for _, src := range r.sources {
		res, attempted, err := r.fetch(ctx, src, productID, query)
		if !attempted {
			continue // this source has no page for this product
		}
		asked++
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err() // shutting down or timed out: let River reschedule
			}
			class := sources.ErrorClass(err)
			r.metrics.SourceRequests.WithLabelValues(src.ID(), "error").Inc()
			r.metrics.SourceErrors.WithLabelValues(src.ID(), class).Inc()
			r.log.WarnContext(ctx, "online source failed", "source", src.ID(), "class", class, "error", err)
			failed = append(failed, src.ID())
			if err := r.store.RecordSourceFailure(ctx, db.RecordSourceFailureParams{ID: src.ID(), LastError: class}); err != nil {
				return fmt.Errorf("refresh: record source failure: %w", err)
			}
			continue
		}
		r.metrics.SourceRequests.WithLabelValues(src.ID(), "ok").Inc()
		r.metrics.SourceParseFailures.WithLabelValues(src.ID()).Add(float64(res.Skipped))
		if err := r.store.RecordSourceSuccess(ctx, src.ID()); err != nil {
			return fmt.Errorf("refresh: record source success: %w", err)
		}
		matched := 0
		for _, offer := range res.Offers {
			conf := matching.Confidence(mp, offer.Title)
			if conf < matching.MinConfidence {
				r.log.InfoContext(ctx, "online offer did not match the product", "source", src.ID(), "product_id", productID, "title", offer.Title, "confidence", conf)
				continue
			}
			if err := r.store.UpsertOffer(ctx, db.UpsertOfferParams{
				SourceID: src.ID(), ProductID: productID, Url: offer.URL, Title: offer.Title,
				PricePoisha: int64(offer.Price), InStock: offer.InStock,
				MatchConfidence: float32(conf), ObservedAt: r.now(),
			}); err != nil {
				return fmt.Errorf("refresh: save offer: %w", err)
			}
			matched++
		}
		r.metrics.SourceOffers.WithLabelValues(src.ID()).Add(float64(matched))
		r.log.InfoContext(ctx, "online source refreshed", "source", src.ID(), "product_id", productID,
			"found", len(res.Offers), "matched", matched, "skipped", res.Skipped)
	}
	if err := r.store.RecordProductRefresh(ctx, db.RecordProductRefreshParams{
		ProductID: productID, Succeeded: len(failed) < asked || asked == 0, FailedSources: failed,
	}); err != nil {
		return fmt.Errorf("refresh: record product refresh: %w", err)
	}
	return nil
}

// fetch asks one source about one product. A page reader reads every page
// mapped to the product for that shop; a searcher searches by name.
// attempted is false when the source has nothing to say about the product.
func (r *Refresher) fetch(ctx context.Context, src sources.Source, productID int64, query search.Query) (sources.ParseResult, bool, error) {
	switch s := src.(type) {
	case sources.PageReader:
		urls, err := r.store.ListProductSourceURLs(ctx, db.ListProductSourceURLsParams{ProductID: productID, SourceID: s.ID()})
		if err != nil {
			return sources.ParseResult{}, true, fmt.Errorf("refresh: product pages: %w", err)
		}
		if len(urls) == 0 {
			return sources.ParseResult{}, false, nil
		}
		var all sources.ParseResult
		for _, u := range urls {
			start := r.now()
			res, err := s.ReadPage(ctx, u)
			r.metrics.SourceLatency.WithLabelValues(s.ID()).Observe(r.now().Sub(start).Seconds())
			if err != nil {
				return all, true, err
			}
			all.Offers = append(all.Offers, res.Offers...)
			all.Skipped += res.Skipped
		}
		return all, true, nil
	case sources.Searcher:
		start := r.now()
		res, err := s.Search(ctx, query)
		r.metrics.SourceLatency.WithLabelValues(s.ID()).Observe(r.now().Sub(start).Seconds())
		return res, true, err
	default:
		return sources.ParseResult{}, false, nil
	}
}
