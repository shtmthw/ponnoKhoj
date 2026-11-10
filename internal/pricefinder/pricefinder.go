// Package pricefinder answers a shopper's search across the three price
// sources: local listings, cached online offers and estimates. It is shared
// by the HTML pages and the JSON API.
package pricefinder

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/estimate"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/observability"
	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// Enqueuer queues background work.
type Enqueuer interface {
	EnqueueRefreshOffers(ctx context.Context, productID int64) error
	EnqueueEstimate(ctx context.Context, productID int64, cell string, radiusM int) error
}

// Config configures searching.
type Config struct {
	DefaultRadiusM int
	MaxRadiusM     int
	Freshness      domain.FreshnessPolicy
	OfferCacheTTL  time.Duration
	OfferMaxAge    time.Duration
	// SourcesEnabled is how many online sources are switched on, and
	// SearchSourcesEnabled how many of them can search by text (the rest
	// read known product pages only).
	SourcesEnabled       int
	SearchSourcesEnabled int
	PageSize             int
}

// Service implements search.
type Service struct {
	store     *store.Store
	estimates *estimate.Service
	enqueuer  Enqueuer
	cfg       Config
	metrics   *observability.Metrics
	log       *slog.Logger
	now       func() time.Time

	// Search counts are aggregated in memory and written by FlushStats:
	// writing on every search made all concurrent searches queue on the
	// same search_stats row lock (and on a WAL flush each).
	statsMu sync.Mutex
	stats   map[statKey]int64
}

type statKey struct{ cell, mode string }

// NewService builds the search service.
func NewService(st *store.Store, est *estimate.Service, enq Enqueuer, cfg Config, metrics *observability.Metrics, log *slog.Logger) *Service {
	if cfg.PageSize <= 0 {
		cfg.PageSize = 20
	}
	return &Service{store: st, estimates: est, enqueuer: enq, cfg: cfg, metrics: metrics, log: log, now: time.Now, stats: map[statKey]int64{}}
}

// Errors.
var (
	ErrBadCursor       = errors.New("pricefinder: invalid cursor")
	ErrProductNotFound = errors.New("pricefinder: product not found")
	ErrNeedsLocation   = errors.New("pricefinder: a location is required")
)

// Request is one search.
type Request struct {
	Query      string
	CategoryID *int64
	Mode       domain.Mode
	// Place is where the shopper is: an exact point, an upazila or a
	// district. nil means no location was given.
	Place *Place
	// RadiusM applies to an exact point only.
	RadiusM int
	// ProductID selects a specific product among the candidates (0 = best match).
	ProductID int64
	Cursor    string
}

// Product is a catalog product in search results.
type Product struct {
	ID         int64           `json:"id"`
	NameEn     string          `json:"name_en"`
	NameBn     string          `json:"name_bn"`
	Brand      string          `json:"brand,omitempty"`
	Model      string          `json:"model,omitempty"`
	Quantity   domain.Quantity `json:"-"`
	CategoryID int64           `json:"category_id"`
	IsDemo     bool            `json:"is_demo"`
	Score      float64         `json:"score,omitempty"`
}

// LocalListing is one nearby shop's price.
type LocalListing struct {
	ListingID   int64
	Price       domain.Money
	InStock     bool
	UnderReview bool // flagged by the price anomaly guard, not yet reviewed
	UpdatedAt   time.Time
	Freshness   domain.Freshness
	ShopID      int64
	ShopName    string
	ShopAddress string
	ShopPhone   string
	Location    geo.Point
	// DistanceM is from the shopper's exact point; HasDistance is false for
	// area-scoped searches, where there is no point to measure from.
	DistanceM     float64
	HasDistance   bool
	AreaEn        string
	AreaBn        string
	ParentAreaEn  string
	ParentAreaBn  string
	DirectionsURL string
	IsDemo        bool
}

// LocalResult is the local-mode answer.
type LocalResult struct {
	// NeedsLocation is true when no location was given: local prices cannot
	// be searched without one.
	NeedsLocation bool
	// Scope is what was searched: a radius around a point, an upazila, or a
	// whole district.
	Scope      *ResolvedPlace
	RadiusM    int
	Listings   []LocalListing
	Total      int64
	NextCursor string
}

// Offer is one cached online price.
type Offer struct {
	ID         int64
	SourceID   string
	SourceName string
	IsDemo     bool
	URL        string
	Title      string
	Price      domain.Money
	InStock    bool
	ObservedAt time.Time
}

// OnlineResult is the online-mode answer.
type OnlineResult struct {
	Offers []Offer
	// SourcesEnabled is how many online sources are switched on. Zero means
	// online prices are unavailable, not that the product is not sold online.
	SourcesEnabled int
	// Refreshing is true when a background refresh was queued for this search.
	Refreshing      bool
	LastRefreshedAt *time.Time
	// FailedSources lists sources that failed on the last refresh.
	FailedSources []string
	// Covered is false when no enabled source can be asked about this
	// product (no page for it at the shops we read, and no searchable shop).
	Covered bool
}

// Estimate states beyond the stored statuses.
const (
	EstimateNone          = "none"           // not requested
	EstimateSuggested     = "suggested"      // no local listings: an estimate is offered
	EstimateNeedsLocation = "needs_location" // cannot estimate without a location
)

// EstimateResult is the estimate-mode answer.
type EstimateResult struct {
	// State is one of the Estimate* constants or a domain.EstimateStatus
	// value (pending, ready, insufficient_data, failed).
	State   string
	Cell    string
	RadiusM int
	// Place is what the estimate is for; its Point is where evidence is
	// gathered around.
	Place *ResolvedPlace
	View  estimate.View
}

// Response is the answer to a search.
type Response struct {
	Query    search.Query
	Mode     domain.Mode
	Products []Product // candidates, best first
	Selected *Product
	Local    *LocalResult
	Online   *OnlineResult
	Estimate *EstimateResult
}

type cursor struct {
	Price    int64   `json:"p"`
	Distance float64 `json:"d"`
	ID       int64   `json:"i"`
}

func encodeCursor(c cursor) string {
	raw, _ := json.Marshal(c) // a struct of numbers always encodes
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (cursor, error) {
	if s == "" {
		return cursor{Price: 0, Distance: -1, ID: 0}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) > 200 {
		return cursor{}, ErrBadCursor
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil || c.Price <= 0 || c.ID <= 0 || c.Distance < 0 || c.Distance != c.Distance {
		return cursor{}, ErrBadCursor
	}
	return c, nil
}

// maxCandidates is how many product matches are offered to choose from.
const maxCandidates = 8

// Candidates returns the catalog products matching free text, best first.
func (s *Service) Candidates(ctx context.Context, q search.Query, categoryID *int64, limit int) ([]Product, error) {
	if q.Empty() {
		return nil, nil
	}
	rows, err := s.store.SearchProducts(ctx, db.SearchProductsParams{Key: q.Key, CategoryID: categoryID, MaxResults: 40})
	if err != nil {
		return nil, fmt.Errorf("pricefinder: search products: %w", err)
	}
	cands := make([]search.Candidate, 0, len(rows))
	byID := make(map[int64]db.SearchProductsRow, len(rows))
	for _, r := range rows {
		qty := domain.Quantity{Value: r.Quantity, Unit: domain.Unit(r.Unit)}
		cands = append(cands, search.Candidate{ProductID: r.ID, Similarity: r.Similarity, Quantity: &qty})
		byID[r.ID] = r
	}
	search.Rank(q, cands)
	out := make([]Product, 0, min(len(cands), limit))
	for _, c := range cands[:min(len(cands), limit)] {
		r := byID[c.ProductID]
		out = append(out, Product{
			ID: r.ID, NameEn: r.NameEn, NameBn: r.NameBn, Brand: r.Brand, Model: r.Model,
			Quantity: *c.Quantity, CategoryID: r.CategoryID, IsDemo: r.IsDemo, Score: search.Score(q, c),
		})
	}
	return out, nil
}

// Search runs a search in the requested mode(s).
func (s *Service) Search(ctx context.Context, req Request) (Response, error) {
	resp := Response{Query: search.ParseQuery(req.Query), Mode: req.Mode}
	after, err := decodeCursor(req.Cursor)
	if err != nil {
		return resp, err
	}
	radius := geo.ClampRadius(req.RadiusM, s.cfg.DefaultRadiusM, s.cfg.MaxRadiusM)

	resp.Products, err = s.Candidates(ctx, resp.Query, req.CategoryID, maxCandidates)
	if err != nil {
		return resp, err
	}
	switch {
	case req.ProductID > 0:
		p, err := s.Product(ctx, req.ProductID)
		if err != nil {
			return resp, err
		}
		resp.Selected = &p
	case len(resp.Products) > 0:
		resp.Selected = &resp.Products[0]
	default:
		s.countSearch(ctx, req.Mode, nil, "no_match")
		return resp, nil
	}

	productID := resp.Selected.ID
	place, err := s.Resolve(ctx, req.Place)
	if err != nil {
		return resp, err
	}
	if req.Mode == domain.ModeLocal || req.Mode == domain.ModeAll {
		resp.Local, err = s.local(ctx, productID, place, radius, after)
		if err != nil {
			return resp, err
		}
	}
	if req.Mode == domain.ModeOnline || req.Mode == domain.ModeAll {
		resp.Online, err = s.online(ctx, productID)
		if err != nil {
			return resp, err
		}
	}
	if req.Mode == domain.ModeEstimate || req.Mode == domain.ModeAll {
		// A search (GET) never starts an estimate: starting one is an
		// explicit action (POST /estimate or POST /api/v1/estimates). Here
		// it is looked up, and suggested when nothing local was found.
		resp.Estimate, err = s.Estimate(ctx, productID, place, radius, false)
		if err != nil {
			return resp, err
		}
		if resp.Estimate.State == EstimateNone && resp.Local != nil && !resp.Local.NeedsLocation && resp.Local.Total == 0 {
			resp.Estimate.State = EstimateSuggested
		}
	}
	s.countSearch(ctx, req.Mode, place, "ok")
	return resp, nil
}

// Product loads one active product.
func (s *Service) Product(ctx context.Context, id int64) (Product, error) {
	row, err := s.store.GetProduct(ctx, id)
	if store.IsNotFound(err) || (err == nil && row.Status != "active") {
		return Product{}, ErrProductNotFound
	}
	if err != nil {
		return Product{}, fmt.Errorf("pricefinder: load product: %w", err)
	}
	return Product{
		ID: row.ID, NameEn: row.NameEn, NameBn: row.NameBn, Brand: row.Brand, Model: row.Model,
		Quantity: domain.Quantity{Value: row.Quantity, Unit: domain.Unit(row.Unit)}, CategoryID: row.CategoryID, IsDemo: row.IsDemo,
	}, nil
}

func (s *Service) local(ctx context.Context, productID int64, place *ResolvedPlace, radius int, after cursor) (*LocalResult, error) {
	if place == nil {
		return &LocalResult{NeedsLocation: true, RadiusM: radius}, nil
	}
	if place.Kind != ScopePoint {
		return s.localArea(ctx, productID, place, after)
	}
	loc := place.Point
	now := s.now()
	visibleSince := now.Add(-s.cfg.Freshness.StaleFor)
	rows, err := s.store.SearchLocalListings(ctx, db.SearchLocalListingsParams{
		Lat: loc.Lat, Lng: loc.Lng, ProductID: productID, VisibleSince: visibleSince, RadiusM: float64(radius),
		AfterPrice: after.Price, AfterDistance: after.Distance, AfterID: after.ID,
		PageSize: int32(s.cfg.PageSize + 1), //nolint:gosec // page size is a small constant
	})
	if err != nil {
		return nil, fmt.Errorf("pricefinder: local listings: %w", err)
	}
	total, err := s.store.CountLocalListings(ctx, db.CountLocalListingsParams{
		Lat: loc.Lat, Lng: loc.Lng, ProductID: productID, VisibleSince: visibleSince, RadiusM: float64(radius),
	})
	if err != nil {
		return nil, fmt.Errorf("pricefinder: count local listings: %w", err)
	}
	res := &LocalResult{Scope: place, RadiusM: radius, Total: total, Listings: []LocalListing{}}
	if len(rows) > s.cfg.PageSize {
		rows = rows[:s.cfg.PageSize]
		last := rows[len(rows)-1]
		res.NextCursor = encodeCursor(cursor{Price: last.PricePoisha, Distance: last.DistanceM, ID: last.ListingID})
	}
	for _, r := range rows {
		p := geo.Point{Lat: r.Lat, Lng: r.Lng}
		res.Listings = append(res.Listings, LocalListing{
			ListingID: r.ListingID, Price: domain.Money(r.PricePoisha), InStock: r.InStock, UnderReview: r.AnomalyFlagged,
			UpdatedAt: r.UpdatedAt, Freshness: s.cfg.Freshness.Of(r.UpdatedAt, now),
			ShopID: r.ShopID, ShopName: r.ShopName, ShopAddress: r.ShopAddress, ShopPhone: r.ShopPhone,
			Location: p, DistanceM: r.DistanceM, HasDistance: true,
			AreaEn: r.AreaEn, AreaBn: r.AreaBn, ParentAreaEn: r.ParentAreaEn, ParentAreaBn: r.ParentAreaBn,
			DirectionsURL: geo.DirectionsURL(p), IsDemo: r.IsDemo,
		})
	}
	return res, nil
}

// localArea lists the shops registered in an upazila, or anywhere in a
// district, cheapest first.
func (s *Service) localArea(ctx context.Context, productID int64, place *ResolvedPlace, after cursor) (*LocalResult, error) {
	now := s.now()
	visibleSince := now.Add(-s.cfg.Freshness.StaleFor)
	rows, err := s.store.SearchAreaListings(ctx, db.SearchAreaListingsParams{
		ProductID: productID, VisibleSince: visibleSince, AreaID: place.AreaID,
		AfterPrice: after.Price, AfterID: after.ID,
		PageSize: int32(s.cfg.PageSize + 1), //nolint:gosec // page size is a small constant
	})
	if err != nil {
		return nil, fmt.Errorf("pricefinder: area listings: %w", err)
	}
	total, err := s.store.CountAreaListings(ctx, db.CountAreaListingsParams{ProductID: productID, VisibleSince: visibleSince, AreaID: place.AreaID})
	if err != nil {
		return nil, fmt.Errorf("pricefinder: count area listings: %w", err)
	}
	res := &LocalResult{Scope: place, Total: total, Listings: []LocalListing{}}
	if len(rows) > s.cfg.PageSize {
		rows = rows[:s.cfg.PageSize]
		last := rows[len(rows)-1]
		res.NextCursor = encodeCursor(cursor{Price: last.PricePoisha, Distance: 0, ID: last.ListingID})
	}
	for _, r := range rows {
		p := geo.Point{Lat: r.Lat, Lng: r.Lng}
		res.Listings = append(res.Listings, LocalListing{
			ListingID: r.ListingID, Price: domain.Money(r.PricePoisha), InStock: r.InStock, UnderReview: r.AnomalyFlagged,
			UpdatedAt: r.UpdatedAt, Freshness: s.cfg.Freshness.Of(r.UpdatedAt, now),
			ShopID: r.ShopID, ShopName: r.ShopName, ShopAddress: r.ShopAddress, ShopPhone: r.ShopPhone,
			Location: p, AreaEn: r.AreaEn, AreaBn: r.AreaBn, ParentAreaEn: r.ParentAreaEn, ParentAreaBn: r.ParentAreaBn,
			DirectionsURL: geo.DirectionsURL(p), IsDemo: r.IsDemo,
		})
	}
	return res, nil
}

// online returns cached offers immediately and queues a refresh when the
// cache is stale (stale-while-revalidate).
func (s *Service) online(ctx context.Context, productID int64) (*OnlineResult, error) {
	now := s.now()
	res := &OnlineResult{SourcesEnabled: s.cfg.SourcesEnabled, Offers: []Offer{}, FailedSources: []string{}}
	if s.cfg.SourcesEnabled == 0 {
		return res, nil
	}
	pages, err := s.store.CountProductSourceURLs(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("pricefinder: product pages: %w", err)
	}
	res.Covered = pages > 0 || s.cfg.SearchSourcesEnabled > 0
	if !res.Covered {
		return res, nil
	}
	state, err := s.store.TouchProductRequest(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("pricefinder: refresh state: %w", err)
	}
	res.LastRefreshedAt = state.LastSuccessAt
	res.FailedSources = state.FailedSources
	if state.LastAttemptAt == nil || now.Sub(*state.LastAttemptAt) > s.cfg.OfferCacheTTL {
		if err := s.enqueuer.EnqueueRefreshOffers(ctx, productID); err != nil {
			// Serving the cached offers matters more than the refresh.
			s.log.ErrorContext(ctx, "could not queue offers refresh", "product_id", productID, "error", err)
		} else {
			res.Refreshing = true
		}
	}
	rows, err := s.store.ListOffersByProduct(ctx, db.ListOffersByProductParams{
		ProductID: productID, ObservedSince: now.Add(-s.cfg.OfferMaxAge), MaxResults: 50,
	})
	if err != nil {
		return nil, fmt.Errorf("pricefinder: offers: %w", err)
	}
	for _, r := range rows {
		res.Offers = append(res.Offers, Offer{
			ID: r.ID, SourceID: r.SourceID, SourceName: r.SourceName, IsDemo: r.ComplianceStatus == "demo",
			URL: r.Url, Title: r.Title, Price: domain.Money(r.PricePoisha), InStock: r.InStock, ObservedAt: r.ObservedAt,
		})
	}
	return res, nil
}

// Estimate looks up the estimate for a product at a place. With start=true
// it also starts one when none is cached. The place's point is coarsened to
// its ~1 km geohash cell before anything is stored or queued. Area-scoped
// places (district, upazila) are estimated around their representative
// point from a wider starting radius.
func (s *Service) Estimate(ctx context.Context, productID int64, place *ResolvedPlace, radiusM int, start bool) (*EstimateResult, error) {
	radius := geo.ClampRadius(radiusM, s.cfg.DefaultRadiusM, s.cfg.MaxRadiusM)
	if place == nil || place.Point == nil {
		return &EstimateResult{State: EstimateNeedsLocation, RadiusM: geo.BucketRadius(radius), Place: place}, nil
	}
	radius = geo.BucketRadius(place.estimateRadius(radius))
	cell := place.Point.Cell()
	res := &EstimateResult{State: EstimateNone, Cell: cell, RadiusM: radius, Place: place}
	view, found, err := s.estimates.Lookup(ctx, productID, cell, radius)
	if err != nil {
		return nil, err
	}
	if found && view.Status != domain.EstimateFailed {
		res.State, res.View = string(view.Status), view
		return res, nil
	}
	if !start {
		if found {
			res.State, res.View = string(view.Status), view
		}
		return res, nil
	}
	started, err := s.estimates.Begin(ctx, productID, cell, radius)
	if err != nil {
		return nil, err
	}
	if started {
		if err := s.enqueuer.EnqueueEstimate(ctx, productID, cell, radius); err != nil {
			return nil, err
		}
	}
	res.State = string(domain.EstimatePending)
	return res, nil
}

// countSearch records the search in metrics and in the coarse per-cell
// aggregate table. Only the ~1 km cell is stored, never the coordinates.
func (s *Service) countSearch(ctx context.Context, mode domain.Mode, place *ResolvedPlace, outcome string) {
	if s.metrics != nil {
		s.metrics.Searches.WithLabelValues(string(mode), outcome).Inc()
	}
	cell := "none"
	if place != nil && place.Point != nil {
		cell = place.Point.Cell()
	}
	s.statsMu.Lock()
	s.stats[statKey{cell, string(mode)}]++
	s.statsMu.Unlock()
}

// FlushStats writes the search counts gathered since the last flush. The
// serve process calls it periodically and on shutdown. Counts that fail to
// write are kept for the next flush.
func (s *Service) FlushStats(ctx context.Context) error {
	s.statsMu.Lock()
	pending := s.stats
	s.stats = map[statKey]int64{}
	s.statsMu.Unlock()
	var errs []error
	for k, n := range pending {
		if err := s.store.BumpSearchStat(ctx, db.BumpSearchStatParams{Cell: k.cell, Mode: k.mode, Searches: n}); err != nil {
			errs = append(errs, err)
			s.statsMu.Lock()
			s.stats[k] += n
			s.statsMu.Unlock()
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("pricefinder: flush search stats: %w", err)
	}
	return nil
}
