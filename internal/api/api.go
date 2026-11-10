// Package api serves the JSON API under /api/v1. It is read-mostly: search,
// product lookup, shop detail and estimates. Amounts are integers in poisha
// (1 BDT = 100 poisha) with a decimal-string convenience field.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/config"
	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/estimate"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/httpx"
	"github.com/shtmthw/ponnoKhoj/internal/pricefinder"
	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// Deps is everything the API needs.
type Deps struct {
	Cfg           config.Config
	Store         *store.Store
	Finder        *pricefinder.Service
	Log           *slog.Logger
	ClientIP      func(*http.Request) string
	SearchLimit   *httpx.RateLimiter
	EstimateLimit *httpx.RateLimiter
}

// Server serves the JSON API.
type Server struct {
	d Deps
}

// New builds the API.
func New(d Deps) *Server { return &Server{d: d} }

// Register adds the API routes.
func (s *Server) Register(rt *httpx.Router) {
	limited := func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, http.StatusTooManyRequests, "rate_limited", "too many requests; slow down")
	}
	searchLimit := s.d.SearchLimit.Middleware(s.d.ClientIP, limited)
	estimateLimit := s.d.EstimateLimit.Middleware(s.d.ClientIP, limited)

	rt.HandleFunc("GET /api/v1/search", s.handleSearch, searchLimit)
	rt.HandleFunc("GET /api/v1/products", s.handleProducts, searchLimit)
	rt.HandleFunc("GET /api/v1/shops/{id}", s.handleShop, searchLimit)
	rt.HandleFunc("GET /api/v1/districts", s.handleDistricts, searchLimit)
	rt.HandleFunc("GET /api/v1/estimates", s.handleEstimateGet, searchLimit)
	rt.HandleFunc("POST /api/v1/estimates", s.handleEstimatePost, estimateLimit)
	rt.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "no such API endpoint")
	})
}

// Money is an amount in the API.
type Money struct {
	Poisha int64  `json:"poisha"`
	BDT    string `json:"bdt"`
}

func money(m domain.Money) Money { return Money{Poisha: int64(m), BDT: m.Decimal()} }

// Product is a catalog product.
type Product struct {
	ID         int64   `json:"id"`
	NameEn     string  `json:"name_en"`
	NameBn     string  `json:"name_bn"`
	Brand      string  `json:"brand,omitempty"`
	Model      string  `json:"model,omitempty"`
	Unit       string  `json:"unit"`
	Quantity   int64   `json:"quantity"`
	Size       string  `json:"size"`
	CategoryID int64   `json:"category_id"`
	IsDemo     bool    `json:"is_demo"`
	Score      float64 `json:"score,omitempty"`
}

func product(p pricefinder.Product) Product {
	return Product{
		ID: p.ID, NameEn: p.NameEn, NameBn: p.NameBn, Brand: p.Brand, Model: p.Model,
		Unit: string(p.Quantity.Unit), Quantity: p.Quantity.Value, Size: p.Quantity.Format(false),
		CategoryID: p.CategoryID, IsDemo: p.IsDemo, Score: p.Score,
	}
}

// LocalListing is one nearby shop price.
type LocalListing struct {
	ListingID   int64     `json:"listing_id"`
	Price       Money     `json:"price"`
	InStock     bool      `json:"in_stock"`
	UnderReview bool      `json:"price_under_review"`
	UpdatedAt   time.Time `json:"updated_at"`
	Freshness   string    `json:"freshness"`
	ShopID      int64     `json:"shop_id"`
	ShopName    string    `json:"shop_name"`
	ShopAddress string    `json:"shop_address"`
	ShopPhone   string    `json:"shop_phone,omitempty"`
	Lat         float64   `json:"lat"`
	Lng         float64   `json:"lng"`
	// DistanceM is present only when the search was around an exact point.
	DistanceM     *float64 `json:"distance_m,omitempty"`
	Area          string   `json:"area_en"`
	AreaBn        string   `json:"area_bn"`
	District      string   `json:"district_en,omitempty"`
	DistrictBn    string   `json:"district_bn,omitempty"`
	DirectionsURL string   `json:"directions_url"`
	IsDemo        bool     `json:"is_demo"`
}

// Scope says what a local search covered.
type Scope struct {
	// Kind is point (a radius around an exact location), upazila (shops
	// registered in it) or district (every shop in the district).
	Kind       string  `json:"kind"`
	AreaID     int64   `json:"area_id,omitempty"`
	AreaEn     string  `json:"area_en,omitempty"`
	AreaBn     string  `json:"area_bn,omitempty"`
	DistrictEn string  `json:"district_en,omitempty"`
	DistrictBn string  `json:"district_bn,omitempty"`
	RadiusM    int     `json:"radius_m,omitempty"`
	Lat        float64 `json:"lat,omitempty"`
	Lng        float64 `json:"lng,omitempty"`
}

func scopeDTO(p *pricefinder.ResolvedPlace, radius int) *Scope {
	if p == nil {
		return nil
	}
	sc := &Scope{Kind: string(p.Kind), AreaID: p.AreaID, AreaEn: p.AreaEn, AreaBn: p.AreaBn, DistrictEn: p.DistrictEn, DistrictBn: p.DistrictBn}
	if p.Kind == pricefinder.ScopePoint {
		sc.RadiusM = radius
	}
	return sc
}

// LocalResult is the local-mode section of a search response.
type LocalResult struct {
	NeedsLocation bool           `json:"needs_location"`
	Scope         *Scope         `json:"scope,omitempty"`
	RadiusM       int            `json:"radius_m"`
	Total         int64          `json:"total"`
	Listings      []LocalListing `json:"listings"`
	NextCursor    string         `json:"next_cursor,omitempty"`
}

// Offer is one cached online price.
type Offer struct {
	SourceID   string    `json:"source_id"`
	SourceName string    `json:"source_name"`
	IsDemo     bool      `json:"is_demo"`
	URL        string    `json:"url"`
	Title      string    `json:"title"`
	Price      Money     `json:"price"`
	InStock    bool      `json:"in_stock"`
	ObservedAt time.Time `json:"observed_at"`
}

// OnlineResult is the online-mode section of a search response.
type OnlineResult struct {
	SourcesEnabled int `json:"sources_enabled"`
	// Covered is false when none of the enabled shops sells this product.
	Covered         bool       `json:"covered"`
	Refreshing      bool       `json:"refreshing"`
	LastRefreshedAt *time.Time `json:"last_refreshed_at,omitempty"`
	FailedSources   []string   `json:"failed_sources"`
	Offers          []Offer    `json:"offers"`
}

// Estimate is an estimate (or the lack of one) in the API.
type Estimate struct {
	// State is none, suggested, needs_location, pending, ready,
	// insufficient_data or failed.
	State          string                   `json:"state"`
	Scope          *Scope                   `json:"scope,omitempty"`
	IsEstimate     bool                     `json:"is_estimate"`
	Disclaimer     map[string]string        `json:"disclaimer"`
	Cell           string                   `json:"cell,omitempty"`
	RadiusM        int                      `json:"radius_m"`
	Low            *Money                   `json:"low,omitempty"`
	Point          *Money                   `json:"point,omitempty"`
	High           *Money                   `json:"high,omitempty"`
	Confidence     string                   `json:"confidence,omitempty"`
	Method         string                   `json:"method,omitempty"`
	ModelID        string                   `json:"model_id,omitempty"`
	FallbackReason string                   `json:"fallback_reason,omitempty"`
	ExplanationEn  string                   `json:"explanation_en,omitempty"`
	ExplanationBn  string                   `json:"explanation_bn,omitempty"`
	Evidence       *estimate.StoredEvidence `json:"evidence,omitempty"`
	CreatedAt      *time.Time               `json:"created_at,omitempty"`
}

// disclaimer is attached to every estimate payload, in both languages.
var disclaimer = map[string]string{
	"en": "This is an estimate, not a quoted price. It is a range built from local evidence, and the real price may be higher or lower.",
	"bn": "এটি একটি অনুমান, কোনো নির্ধারিত দাম নয়। স্থানীয় তথ্য থেকে তৈরি একটি পরিসর; প্রকৃত দাম এর চেয়ে বেশি বা কম হতে পারে।",
}

func estimateDTO(e *pricefinder.EstimateResult) *Estimate {
	if e == nil {
		return nil
	}
	out := &Estimate{State: e.State, IsEstimate: true, Disclaimer: disclaimer, Cell: e.Cell, RadiusM: e.RadiusM, Scope: scopeDTO(e.Place, 0)}
	v := e.View
	switch domain.EstimateStatus(e.State) {
	case domain.EstimateReady:
		low, point, high := money(v.Low), money(v.Point), money(v.High)
		out.Low, out.Point, out.High = &low, &point, &high
		out.Confidence, out.Method, out.ModelID = string(v.Confidence), string(v.Method), v.ModelID
		out.FallbackReason, out.ExplanationEn, out.ExplanationBn = v.FallbackReason, v.ExplanationEn, v.ExplanationBn
		out.Evidence, out.CreatedAt = &v.Evidence, &v.CreatedAt
	case domain.EstimateInsufficient:
		out.Evidence, out.CreatedAt = &v.Evidence, &v.CreatedAt
	case domain.EstimatePending, domain.EstimateFailed:
	}
	return out
}

// SearchResponse is the body of GET /api/v1/search.
type SearchResponse struct {
	Query           string        `json:"query"`
	NormalizedQuery string        `json:"normalized_query"`
	Mode            string        `json:"mode"`
	Products        []Product     `json:"products"`
	Selected        *Product      `json:"selected_product"`
	Local           *LocalResult  `json:"local,omitempty"`
	Online          *OnlineResult `json:"online,omitempty"`
	Estimate        *Estimate     `json:"estimate,omitempty"`
	// Counts gives the number of results per mode that was searched.
	Counts map[string]int64 `json:"counts"`
}

func intParam(r *http.Request, name string) (int64, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil && n >= 0
}

// location resolves lat/lng or district_id into a point. ok=false means the
// parameters were present but invalid.
// location reads where the shopper is: lat and lng (an exact point), else
// upazila_id, else district_id. ok=false means a parameter was invalid.
func (s *Server) location(r *http.Request) (*pricefinder.Place, bool) {
	q := r.URL.Query()
	if q.Get("lat") != "" || q.Get("lng") != "" {
		p, err := geo.ParsePoint(q.Get("lat"), q.Get("lng"))
		if err != nil {
			return nil, false
		}
		return pricefinder.PointPlace(p), true
	}
	for _, name := range []string{"upazila_id", "district_id"} {
		id, ok := intParam(r, name)
		if !ok {
			return nil, false
		}
		if id > 0 {
			return pricefinder.AreaPlace(id), true
		}
	}
	return nil, true
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, pricefinder.ErrBadCursor):
		httpx.WriteError(w, r, http.StatusBadRequest, "bad_cursor", "the cursor is not valid")
	case errors.Is(err, pricefinder.ErrUnknownArea):
		httpx.WriteError(w, r, http.StatusBadRequest, "bad_location", "no such district or upazila")
	case errors.Is(err, pricefinder.ErrProductNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "product_not_found", "no such product")
	default:
		s.d.Log.ErrorContext(r.Context(), "api request failed", "request_id", httpx.RequestID(r.Context()), "error", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, "internal", "internal error")
	}
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mode, ok := domain.ParseMode(q.Get("mode"))
	if !ok {
		httpx.WriteError(w, r, http.StatusBadRequest, "bad_mode", "mode must be online, local, estimate or all")
		return
	}
	loc, ok := s.location(r)
	if !ok {
		httpx.WriteError(w, r, http.StatusBadRequest, "bad_location", "give lat and lng, an upazila_id, or a district_id")
		return
	}
	radius, ok1 := intParam(r, "radius_m")
	productID, ok2 := intParam(r, "product_id")
	categoryID, ok3 := intParam(r, "category_id")
	if !ok1 || !ok2 || !ok3 || radius > int64(s.d.Cfg.SearchMaxRadiusM) {
		httpx.WriteError(w, r, http.StatusBadRequest, "bad_parameter", "radius_m, product_id and category_id must be non-negative integers; radius_m must not exceed the maximum")
		return
	}
	text := strings.TrimSpace(q.Get("q"))
	if text == "" && productID == 0 {
		httpx.WriteError(w, r, http.StatusBadRequest, "missing_query", "give q or product_id")
		return
	}
	req := pricefinder.Request{Query: text, Mode: mode, Place: loc, RadiusM: int(radius), ProductID: productID, Cursor: q.Get("cursor")}
	if categoryID > 0 {
		req.CategoryID = &categoryID
	}
	resp, err := s.d.Finder.Search(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := SearchResponse{Query: resp.Query.Raw, NormalizedQuery: resp.Query.Text, Mode: string(mode), Products: []Product{}, Counts: map[string]int64{}}
	for _, p := range resp.Products {
		out.Products = append(out.Products, product(p))
	}
	if resp.Selected != nil {
		p := product(*resp.Selected)
		out.Selected = &p
	}
	if l := resp.Local; l != nil {
		lr := &LocalResult{NeedsLocation: l.NeedsLocation, Scope: scopeDTO(l.Scope, l.RadiusM), RadiusM: l.RadiusM, Total: l.Total, NextCursor: l.NextCursor, Listings: []LocalListing{}}
		for _, li := range l.Listings {
			lr.Listings = append(lr.Listings, LocalListing{
				ListingID: li.ListingID, Price: money(li.Price), InStock: li.InStock, UnderReview: li.UnderReview,
				UpdatedAt: li.UpdatedAt, Freshness: string(li.Freshness), ShopID: li.ShopID, ShopName: li.ShopName,
				ShopAddress: li.ShopAddress, ShopPhone: li.ShopPhone, Lat: li.Location.Lat, Lng: li.Location.Lng,
				DistanceM: distance(li), Area: li.AreaEn, AreaBn: li.AreaBn, District: li.ParentAreaEn, DistrictBn: li.ParentAreaBn,
				DirectionsURL: li.DirectionsURL, IsDemo: li.IsDemo,
			})
		}
		out.Local, out.Counts["local"] = lr, l.Total
	}
	if o := resp.Online; o != nil {
		or := &OnlineResult{SourcesEnabled: o.SourcesEnabled, Covered: o.Covered, Refreshing: o.Refreshing, LastRefreshedAt: o.LastRefreshedAt, FailedSources: o.FailedSources, Offers: []Offer{}}
		for _, of := range o.Offers {
			or.Offers = append(or.Offers, Offer{
				SourceID: of.SourceID, SourceName: of.SourceName, IsDemo: of.IsDemo, URL: of.URL, Title: of.Title,
				Price: money(of.Price), InStock: of.InStock, ObservedAt: of.ObservedAt,
			})
		}
		out.Online, out.Counts["online"] = or, int64(len(or.Offers))
	}
	if resp.Estimate != nil {
		out.Estimate = estimateDTO(resp.Estimate)
		if resp.Estimate.State == string(domain.EstimateReady) {
			out.Counts["estimate"] = 1
		} else {
			out.Counts["estimate"] = 0
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func distance(li pricefinder.LocalListing) *float64 {
	if !li.HasDistance {
		return nil
	}
	d := li.DistanceM
	return &d
}

func (s *Server) handleProducts(w http.ResponseWriter, r *http.Request) {
	q := search.ParseQuery(r.URL.Query().Get("q"))
	if q.Empty() {
		httpx.WriteError(w, r, http.StatusBadRequest, "missing_query", "give q")
		return
	}
	cands, err := s.d.Finder.Candidates(r.Context(), q, nil, 20)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]Product, 0, len(cands))
	for _, p := range cands {
		out = append(out, product(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"products": out})
}

func (s *Server) handleDistricts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d.Store.ListDistricts(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	type district struct {
		ID     int64   `json:"id"`
		NameEn string  `json:"name_en"`
		NameBn string  `json:"name_bn"`
		Lat    float64 `json:"lat"`
		Lng    float64 `json:"lng"`
	}
	out := make([]district, 0, len(rows))
	for _, d := range rows {
		out = append(out, district{ID: d.ID, NameEn: d.NameEn, NameBn: d.NameBn, Lat: d.Lat, Lng: d.Lng})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"districts": out})
}

func (s *Server) handleShop(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, r, http.StatusNotFound, "shop_not_found", "no such shop")
		return
	}
	shop, err := s.d.Store.GetShop(r.Context(), id)
	// The API is anonymous: only verified, visible shops exist here.
	if store.IsNotFound(err) || (err == nil && (shop.Status != string(domain.ShopVerified) || shop.AutoHidden)) {
		httpx.WriteError(w, r, http.StatusNotFound, "shop_not_found", "no such shop")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := time.Now()
	policy := domain.FreshnessPolicy{FreshFor: s.d.Cfg.ListingFreshFor, StaleFor: s.d.Cfg.ListingStaleFor}
	rows, err := s.d.Store.ListShopListingsPublic(r.Context(), db.ListShopListingsPublicParams{ShopID: id, VisibleSince: now.Add(-policy.StaleFor)})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	type listing struct {
		ListingID   int64     `json:"listing_id"`
		ProductID   int64     `json:"product_id"`
		NameEn      string    `json:"name_en"`
		NameBn      string    `json:"name_bn"`
		Price       Money     `json:"price"`
		InStock     bool      `json:"in_stock"`
		UnderReview bool      `json:"price_under_review"`
		UpdatedAt   time.Time `json:"updated_at"`
		Freshness   string    `json:"freshness"`
	}
	listings := make([]listing, 0, len(rows))
	for _, l := range rows {
		listings = append(listings, listing{
			ListingID: l.ID, ProductID: l.ProductID, NameEn: l.NameEn, NameBn: l.NameBn, Price: money(domain.Money(l.PricePoisha)),
			InStock: l.InStock, UnderReview: l.AnomalyFlagged, UpdatedAt: l.UpdatedAt, Freshness: string(policy.Of(l.UpdatedAt, now)),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": shop.ID, "name": shop.Name, "address": shop.Address, "phone": shop.Phone, "opening_hours": shop.OpeningHours,
		"area_en": shop.AreaEn, "area_bn": shop.AreaBn, "lat": shop.Lat, "lng": shop.Lng, "is_demo": shop.IsDemo,
		"directions_url": geo.DirectionsURL(geo.Point{Lat: shop.Lat, Lng: shop.Lng}), "listings": listings,
	})
}

func (s *Server) handleEstimateGet(w http.ResponseWriter, r *http.Request) {
	productID, ok1 := intParam(r, "product_id")
	radius, ok2 := intParam(r, "radius_m")
	loc, ok3 := s.location(r)
	if !ok1 || !ok2 || !ok3 || productID == 0 || loc == nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "bad_parameter", "product_id and a location (lat and lng, upazila_id, or district_id) are required")
		return
	}
	s.estimate(w, r, productID, loc, int(radius), false)
}

func (s *Server) handleEstimatePost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProductID  int64    `json:"product_id"`
		Lat        *float64 `json:"lat"`
		Lng        *float64 `json:"lng"`
		UpazilaID  int64    `json:"upazila_id"`
		DistrictID int64    `json:"district_id"`
		RadiusM    int      `json:"radius_m"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.ProductID <= 0 || body.RadiusM < 0 || body.UpazilaID < 0 || body.DistrictID < 0 {
		httpx.WriteError(w, r, http.StatusBadRequest, "bad_body", "send JSON with product_id and lat+lng, upazila_id or district_id (radius_m optional)")
		return
	}
	var place *pricefinder.Place
	switch {
	case body.Lat != nil || body.Lng != nil:
		if body.Lat == nil || body.Lng == nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "bad_location", "give both lat and lng")
			return
		}
		p, err := geo.NewPoint(*body.Lat, *body.Lng)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "bad_location", "lat or lng is out of range")
			return
		}
		place = pricefinder.PointPlace(p)
	case body.UpazilaID > 0:
		place = pricefinder.AreaPlace(body.UpazilaID)
	case body.DistrictID > 0:
		place = pricefinder.AreaPlace(body.DistrictID)
	default:
		httpx.WriteError(w, r, http.StatusBadRequest, "bad_location", "a location is required")
		return
	}
	s.estimate(w, r, body.ProductID, place, body.RadiusM, true)
}

func (s *Server) estimate(w http.ResponseWriter, r *http.Request, productID int64, place *pricefinder.Place, radius int, start bool) {
	if _, err := s.d.Finder.Product(r.Context(), productID); err != nil {
		s.fail(w, r, err)
		return
	}
	resolved, err := s.d.Finder.Resolve(r.Context(), place)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := s.d.Finder.Estimate(r.Context(), productID, resolved, radius, start)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if start && res.State == string(domain.EstimatePending) {
		status = http.StatusAccepted
	}
	httpx.WriteJSON(w, status, estimateDTO(res))
}
