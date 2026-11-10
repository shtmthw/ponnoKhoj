package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/estimate"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/moderation"
	"github.com/shtmthw/ponnoKhoj/internal/pricefinder"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// SearchForm holds the search form's current values.
type SearchForm struct {
	Q    string
	Mode string
	// Where is "area" (district, optionally an upazila) or "point" (an
	// exact location with a radius).
	Where      string
	DistrictID int64
	UpazilaID  int64
	Lat, Lng   string
	RadiusM    int
	CategoryID int64
	ProductID  int64
}

// Option types for the form controls.
type (
	// ModeOption is one filtering-mode choice.
	ModeOption struct {
		Value, Label string
		Active       bool
	}
	// RadiusOption is one radius choice, as a select option and as a link.
	RadiusOption struct {
		Value  int
		Meters float64
		Active bool
		URL    string
	}
	// TabLink is one mode tab above the results.
	TabLink struct {
		Label, URL string
		Active     bool
		Count      int64
		HasCount   bool
	}
	// ProductLink is one candidate product chip.
	ProductLink struct {
		NameEn, NameBn, URL string
		Active              bool
	}
)

// EstimateVM is the estimate panel's view model.
type EstimateVM struct {
	State     string
	ProductID int64
	// PlaceFields are the hidden inputs that tell /estimate where.
	PlaceFields map[string]string
	Place       *pricefinder.ResolvedPlace
	RadiusM     int
	RadiusF     float64
	View        estimate.View
	PollURL     string
	Back        string
}

// SearchVM is the search page's view model.
type SearchVM struct {
	Form       SearchForm
	Modes      []ModeOption
	Radii      []RadiusOption
	Districts  []db.ListDistrictsRow
	Upazilas   []db.ListUpazilasByDistrictRow
	Categories []db.Category
	MapLat     float64
	MapLng     float64
	MapZoom    int
	HasQuery   bool
	// PointMode is true when the search is around an exact location.
	PointMode    bool
	Resp         pricefinder.Response
	Tabs         []TabLink
	ProductLinks []ProductLink
	NextURL      string
	SelfURL      string
	Scope        *pricefinder.ResolvedPlace
	RadiusF      float64
	Markers      string
	Estimate     *EstimateVM
	// AreaURLs offer a one-click switch between district-wide and
	// upazila-only results.
	DistrictURL string
}

var modeLabels = []struct{ value, label string }{
	{"all", "mode.all"}, {"local", "mode.local"}, {"online", "mode.online"}, {"estimate", "mode.estimate"},
}

type marker struct {
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Label string  `json:"label"`
}

func markersJSON(ms []marker) string {
	if ms == nil {
		ms = []marker{}
	}
	raw, err := json.Marshal(ms)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func coord(f float64) string { return strconv.FormatFloat(f, 'f', 6, 64) }

// values renders the form as query parameters. Only the fields of the
// active "where" mode are kept, so links never carry a stale location.
func (f SearchForm) values() url.Values {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" && val != "0" {
			v.Set(k, val)
		}
	}
	set("q", f.Q)
	set("mode", f.Mode)
	set("category", strconv.FormatInt(f.CategoryID, 10))
	set("product", strconv.FormatInt(f.ProductID, 10))
	if f.Where == "point" {
		v.Set("where", "point")
		set("lat", f.Lat)
		set("lng", f.Lng)
		set("radius", strconv.Itoa(f.RadiusM))
	} else {
		set("district", strconv.FormatInt(f.DistrictID, 10))
		set("upazila", strconv.FormatInt(f.UpazilaID, 10))
	}
	return v
}

// searchURL rebuilds the search URL for a form, with overrides ("" deletes).
func searchURL(f SearchForm, override map[string]string) string {
	v := f.values()
	for k, val := range override {
		v.Del(k)
		if val != "" && val != "0" {
			v.Set(k, val)
		}
	}
	return "/search?" + v.Encode()
}

// readSearchForm parses and normalizes the search parameters.
func (s *Server) readSearchForm(q url.Values) SearchForm {
	mode, ok := domain.ParseMode(q.Get("mode"))
	if !ok {
		mode = domain.ModeAll
	}
	f := SearchForm{
		Q: strings.TrimSpace(q.Get("q")), Mode: string(mode),
		DistrictID: queryID(q, "district"), UpazilaID: queryID(q, "upazila"),
		CategoryID: queryID(q, "category"), ProductID: queryID(q, "product"),
	}
	if n, err := strconv.Atoi(q.Get("radius")); err == nil {
		f.RadiusM = n
	}
	f.RadiusM = geo.ClampRadius(f.RadiusM, s.d.Cfg.SearchDefaultRadiusM, s.d.Cfg.SearchMaxRadiusM)
	lat := domain.ToASCIIDigits(strings.TrimSpace(q.Get("lat")))
	lng := domain.ToASCIIDigits(strings.TrimSpace(q.Get("lng")))
	// An exact location is used when chosen (or, for old links without
	// "where", whenever coordinates are present) and valid.
	wantPoint := q.Get("where") == "point" || (q.Get("where") == "" && lat != "")
	if p, err := geo.ParsePoint(lat, lng); wantPoint && err == nil {
		f.Where, f.Lat, f.Lng = "point", coord(p.Lat), coord(p.Lng)
	} else {
		f.Where = "area"
	}
	return f
}

// place turns the form into a pricefinder place, validating that the upazila
// belongs to the chosen district.
func (s *Server) place(f *SearchForm, upazilas []db.ListUpazilasByDistrictRow) *pricefinder.Place {
	if f.Where == "point" {
		p, err := geo.ParsePoint(f.Lat, f.Lng)
		if err == nil {
			return pricefinder.PointPlace(p)
		}
		f.Where = "area"
	}
	if f.UpazilaID > 0 {
		for _, u := range upazilas {
			if u.ID == f.UpazilaID {
				return pricefinder.AreaPlace(u.ID)
			}
		}
		f.UpazilaID = 0 // not in the chosen district (or no district)
	}
	if f.DistrictID > 0 {
		return pricefinder.AreaPlace(f.DistrictID)
	}
	return nil
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	l := s.localizer(r)
	q := r.URL.Query()
	districts, categories, err := s.reference(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	form := s.readSearchForm(q)
	validDistrict := false
	for _, d := range districts {
		validDistrict = validDistrict || d.ID == form.DistrictID
	}
	if !validDistrict {
		form.DistrictID, form.UpazilaID = 0, 0
	}
	var upazilas []db.ListUpazilasByDistrictRow
	if form.DistrictID > 0 {
		if upazilas, err = s.d.Finder.Upazilas(r.Context(), form.DistrictID); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	place := s.place(&form, upazilas)

	vm := SearchVM{Districts: districts, Upazilas: upazilas, Categories: categories, MapZoom: 7, RadiusF: float64(form.RadiusM)}
	// Default map view: the Dhaka district centroid from the area dataset.
	for _, d := range districts {
		if d.NameEn == "Dhaka" {
			vm.MapLat, vm.MapLng = d.Lat, d.Lng
		}
		if d.ID == form.DistrictID && form.Where == "area" {
			vm.MapLat, vm.MapLng, vm.MapZoom = d.Lat, d.Lng, 10
		}
	}
	if form.Where == "point" {
		p, _ := geo.ParsePoint(form.Lat, form.Lng)
		vm.MapLat, vm.MapLng, vm.MapZoom, vm.PointMode = p.Lat, p.Lng, 13, true
	}

	for _, m := range modeLabels {
		vm.Modes = append(vm.Modes, ModeOption{Value: m.value, Label: m.label, Active: m.value == form.Mode})
	}
	radii := append([]int(nil), geo.RadiusBuckets...)
	if !slices.Contains(radii, form.RadiusM) {
		radii = append(radii, form.RadiusM)
		slices.Sort(radii)
	}
	for _, rad := range radii {
		if rad <= s.d.Cfg.SearchMaxRadiusM {
			vm.Radii = append(vm.Radii, RadiusOption{Value: rad, Meters: float64(rad), Active: rad == form.RadiusM,
				URL: searchURL(form, map[string]string{"radius": strconv.Itoa(rad), "cursor": ""})})
		}
	}
	vm.Form = form
	vm.HasQuery = form.Q != "" || form.ProductID > 0
	title := l.T("search.heading")

	if vm.HasQuery {
		req := pricefinder.Request{
			Query: form.Q, Mode: domain.Mode(form.Mode), Place: place, RadiusM: form.RadiusM,
			ProductID: form.ProductID, Cursor: q.Get("cursor"),
		}
		if form.CategoryID > 0 {
			req.CategoryID = &form.CategoryID
		}
		resp, err := s.d.Finder.Search(r.Context(), req)
		switch {
		case errors.Is(err, pricefinder.ErrBadCursor), errors.Is(err, pricefinder.ErrUnknownArea):
			s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
			return
		case errors.Is(err, pricefinder.ErrProductNotFound):
			s.renderError(w, r, http.StatusNotFound, "error.not_found")
			return
		case err != nil:
			s.serverError(w, r, err)
			return
		}
		vm.Resp = resp
		vm.SelfURL = searchURL(form, nil)
		if form.UpazilaID > 0 {
			vm.DistrictURL = searchURL(form, map[string]string{"upazila": "", "cursor": ""})
		}
		for _, m := range modeLabels {
			tab := TabLink{Label: m.label, URL: searchURL(form, map[string]string{"mode": m.value, "cursor": ""}), Active: m.value == form.Mode}
			vm.Tabs = append(vm.Tabs, tab)
		}
		if resp.Selected != nil {
			title = l.Name(resp.Selected.NameEn, resp.Selected.NameBn)
			for _, p := range resp.Products {
				vm.ProductLinks = append(vm.ProductLinks, ProductLink{
					NameEn: p.NameEn, NameBn: p.NameBn, Active: p.ID == resp.Selected.ID,
					URL: searchURL(form, map[string]string{"product": strconv.FormatInt(p.ID, 10), "cursor": ""}),
				})
			}
		}
		if resp.Local != nil {
			vm.Scope = resp.Local.Scope
			var ms []marker
			for _, li := range resp.Local.Listings {
				ms = append(ms, marker{Lat: li.Location.Lat, Lng: li.Location.Lng, Label: li.ShopName + " — " + l.Money(li.Price)})
			}
			vm.Markers = markersJSON(ms)
			if resp.Local.NextCursor != "" {
				vm.NextURL = searchURL(form, map[string]string{"cursor": resp.Local.NextCursor})
			}
		}
		if resp.Estimate != nil && resp.Selected != nil {
			vm.Estimate = s.estimateVM(resp.Estimate, resp.Selected.ID, form, vm.SelfURL)
			if vm.Scope == nil {
				vm.Scope = resp.Estimate.Place
			}
		}
	}
	s.render(w, r, http.StatusOK, "search.html", s.page(r, title, vm))
}

// handleUpazilaOptions returns the <option>s of a district's upazilas, for
// the search form's HTMX-driven upazila select.
func (s *Server) handleUpazilaOptions(w http.ResponseWriter, r *http.Request) {
	districtID := queryID(r.URL.Query(), "district")
	var upazilas []db.ListUpazilasByDistrictRow
	if districtID > 0 {
		var err error
		if upazilas, err = s.d.Finder.Upazilas(r.Context(), districtID); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	s.renderNamed(w, r, http.StatusOK, "search.html", "upazila_options", map[string]any{
		"L": s.localizer(r), "Upazilas": upazilas, "Selected": int64(0), "HasDistrict": districtID > 0,
	})
}

func queryID(q url.Values, name string) int64 {
	id, err := strconv.ParseInt(q.Get(name), 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

// placeFields renders a form's place as the hidden fields /estimate takes.
func placeFields(f SearchForm) map[string]string {
	if f.Where == "point" {
		return map[string]string{"where": "point", "lat": f.Lat, "lng": f.Lng, "radius": strconv.Itoa(f.RadiusM)}
	}
	out := map[string]string{"where": "area"}
	if f.DistrictID > 0 {
		out["district"] = strconv.FormatInt(f.DistrictID, 10)
	}
	if f.UpazilaID > 0 {
		out["upazila"] = strconv.FormatInt(f.UpazilaID, 10)
	}
	return out
}

func (s *Server) estimateVM(res *pricefinder.EstimateResult, productID int64, f SearchForm, back string) *EstimateVM {
	vm := &EstimateVM{
		State: res.State, ProductID: productID, RadiusM: res.RadiusM, RadiusF: float64(res.RadiusM),
		View: res.View, Back: back, Place: res.Place, PlaceFields: placeFields(f),
	}
	v := url.Values{}
	for k, val := range vm.PlaceFields {
		v.Set(k, val)
	}
	v.Set("product", strconv.FormatInt(productID, 10))
	v.Set("back", back)
	vm.PollURL = "/estimate?" + v.Encode()
	return vm
}

// estimateRequest reads the estimate panel's parameters from a form or query.
func (s *Server) estimateRequest(r *http.Request, get func(string) string) (productID int64, f SearchForm, place *pricefinder.Place, back string, ok bool) {
	productID, err := strconv.ParseInt(get("product"), 10, 64)
	if err != nil || productID <= 0 {
		return 0, SearchForm{}, nil, "", false
	}
	vals := url.Values{}
	for _, k := range []string{"where", "lat", "lng", "radius", "district", "upazila"} {
		vals.Set(k, get(k))
	}
	f = s.readSearchForm(vals)
	var upazilas []db.ListUpazilasByDistrictRow
	if f.UpazilaID > 0 && f.DistrictID > 0 {
		if upazilas, err = s.d.Finder.Upazilas(r.Context(), f.DistrictID); err != nil {
			return 0, SearchForm{}, nil, "", false
		}
	}
	place = s.place(&f, upazilas)
	return productID, f, place, safeNext(get("back"), "/"), true
}

// handleEstimatePoll returns the estimate panel (HTMX polls it while the
// estimate is pending). Without HTMX it sends the browser back to the page.
func (s *Server) handleEstimatePoll(w http.ResponseWriter, r *http.Request) {
	productID, f, place, back, ok := s.estimateRequest(r, r.URL.Query().Get)
	if !ok {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	if r.Header.Get("HX-Request") == "" {
		http.Redirect(w, r, back, http.StatusSeeOther) //nolint:gosec // G710: back passed safeNext (same-site paths only)
		return
	}
	s.estimatePanel(w, r, productID, f, place, back, false)
}

// handleEstimateStart is the explicit "estimate this" action.
func (s *Server) handleEstimateStart(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	productID, f, place, back, ok := s.estimateRequest(r, r.PostForm.Get)
	if !ok {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	if r.Header.Get("HX-Request") == "" {
		resolved, err := s.d.Finder.Resolve(r.Context(), place)
		if err == nil {
			_, err = s.d.Finder.Estimate(r.Context(), productID, resolved, f.RadiusM, true)
		}
		if err != nil {
			s.estimateError(w, r, err)
			return
		}
		http.Redirect(w, r, back, http.StatusSeeOther) //nolint:gosec // G710: back passed safeNext (same-site paths only)
		return
	}
	s.estimatePanel(w, r, productID, f, place, back, true)
}

func (s *Server) estimateError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case store.IsNotFound(err) || errors.Is(err, pricefinder.ErrProductNotFound):
		s.renderError(w, r, http.StatusNotFound, "error.not_found")
	case errors.Is(err, pricefinder.ErrUnknownArea):
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
	default:
		s.serverError(w, r, err)
	}
}

func (s *Server) estimatePanel(w http.ResponseWriter, r *http.Request, productID int64, f SearchForm, place *pricefinder.Place, back string, start bool) {
	if _, err := s.d.Finder.Product(r.Context(), productID); err != nil {
		s.estimateError(w, r, err)
		return
	}
	resolved, err := s.d.Finder.Resolve(r.Context(), place)
	if err != nil {
		s.estimateError(w, r, err)
		return
	}
	res, err := s.d.Finder.Estimate(r.Context(), productID, resolved, f.RadiusM, start)
	if err != nil {
		s.estimateError(w, r, err)
		return
	}
	data := map[string]any{"L": s.localizer(r), "Data": s.estimateVM(res, productID, f, back)}
	w.Header().Set("Cache-Control", "no-store")
	s.renderNamed(w, r, http.StatusOK, "search.html", "estimate", data)
}

// ShopListingVM is one price on the public shop page.
type ShopListingVM struct {
	ID          int64
	ProductID   int64
	NameEn      string
	NameBn      string
	Price       domain.Money
	InStock     bool
	UnderReview bool
	UpdatedAt   time.Time
	Freshness   domain.Freshness
}

// ShopVM is the public shop page's view model.
type ShopVM struct {
	Shop          db.GetShopRow
	Listings      []ShopListingVM
	DirectionsURL string
	Markers       string
}

func (s *Server) handleShop(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.renderError(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	shop, err := s.d.Store.GetShop(r.Context(), id)
	if store.IsNotFound(err) {
		s.renderError(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Unverified, suspended or auto-hidden shops are visible only to their
	// owner and to admins.
	if shop.Status != string(domain.ShopVerified) || shop.AutoHidden {
		if u := s.currentUser(r); u == nil || (u.ID != shop.OwnerID && !u.IsAdmin()) {
			s.renderError(w, r, http.StatusNotFound, "error.not_found")
			return
		}
	}
	now := time.Now()
	policy := domain.FreshnessPolicy{FreshFor: s.d.Cfg.ListingFreshFor, StaleFor: s.d.Cfg.ListingStaleFor}
	rows, err := s.d.Store.ListShopListingsPublic(r.Context(), db.ListShopListingsPublicParams{ShopID: id, VisibleSince: now.Add(-policy.StaleFor)})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	loc := geo.Point{Lat: shop.Lat, Lng: shop.Lng}
	vm := ShopVM{Shop: shop, DirectionsURL: geo.DirectionsURL(loc), Markers: markersJSON([]marker{{Lat: shop.Lat, Lng: shop.Lng, Label: shop.Name}})}
	for _, row := range rows {
		vm.Listings = append(vm.Listings, ShopListingVM{
			ID: row.ID, ProductID: row.ProductID, NameEn: row.NameEn, NameBn: row.NameBn, Price: domain.Money(row.PricePoisha),
			InStock: row.InStock, UnderReview: row.AnomalyFlagged, UpdatedAt: row.UpdatedAt, Freshness: policy.Of(row.UpdatedAt, now),
		})
	}
	s.render(w, r, http.StatusOK, "shop.html", s.page(r, shop.Name, vm))
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	target := r.PostForm.Get("target")
	id := formID(r, "id")
	back := "/"
	if shopID := formID(r, "shop"); shopID > 0 {
		back = fmt.Sprintf("/shops/%d", shopID)
	}
	res, err := s.d.Moderation.Report(r.Context(), target, id, domain.ReportReason(r.PostForm.Get("reason")), r.PostForm.Get("note"), s.d.ClientIP(r))
	switch {
	case errors.Is(err, moderation.ErrInvalidReport):
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
	case errors.Is(err, moderation.ErrNotFound):
		s.renderError(w, r, http.StatusNotFound, "error.not_found")
	case err != nil:
		s.serverError(w, r, err)
	case res.Hidden && target == moderation.TargetShop:
		redirectFlash(w, r, "/", "flash.report_thanks") // the shop page is now hidden
	case res.Duplicate:
		redirectFlash(w, r, back, "flash.report_duplicate")
	default:
		redirectFlash(w, r, back, "flash.report_thanks")
	}
}
