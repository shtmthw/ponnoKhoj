package web

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/auth"
	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/pricefinder"
	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/sources"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
	"github.com/shtmthw/ponnoKhoj/internal/vendor"
)

// AuthVM is the login / sign-up form's view model.
type AuthVM struct {
	SignUp bool
	Action string
	Next   string
	Name   string
	Email  string
	Error  string
}

func (s *Server) authPage(w http.ResponseWriter, r *http.Request, status int, vm AuthVM) {
	l := s.localizer(r)
	title := l.T("auth.login")
	vm.Action = "/vendor/login"
	if vm.SignUp {
		title, vm.Action = l.T("auth.signup"), "/vendor/signup"
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, status, "auth.html", s.page(r, title, vm))
}

func (s *Server) handleAuthForm(w http.ResponseWriter, r *http.Request) {
	if s.currentUser(r) != nil {
		http.Redirect(w, r, "/vendor", http.StatusSeeOther)
		return
	}
	s.authPage(w, r, http.StatusOK, AuthVM{SignUp: r.URL.Path == "/vendor/signup", Next: safeNext(r.URL.Query().Get("next"), "")})
}

// authErrorKey maps an auth error to a catalog key ("" = not a user error).
func authErrorKey(err error) string {
	switch {
	case errors.Is(err, auth.ErrInvalidEmail):
		return "auth.err.invalid_email"
	case errors.Is(err, auth.ErrInvalidName):
		return "auth.err.invalid_name"
	case errors.Is(err, auth.ErrEmailTaken):
		return "auth.err.email_taken"
	case errors.Is(err, auth.ErrPasswordTooShort):
		return "auth.err.password_short"
	case errors.Is(err, auth.ErrPasswordTooLong):
		return "auth.err.password_long"
	case errors.Is(err, auth.ErrInvalidCredentials):
		return "auth.err.invalid_credentials"
	case errors.Is(err, auth.ErrLocked):
		return "auth.err.locked"
	}
	return ""
}

func (s *Server) handleSignUp(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	vm := AuthVM{SignUp: true, Name: r.PostForm.Get("name"), Email: r.PostForm.Get("email"), Next: safeNext(r.PostForm.Get("next"), "")}
	password := r.PostForm.Get("password")
	if _, err := s.d.Auth.SignUp(r.Context(), vm.Email, password, vm.Name); err != nil {
		if key := authErrorKey(err); key != "" {
			vm.Error = s.localizer(r).T(key)
			s.authPage(w, r, http.StatusUnprocessableEntity, vm)
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.finishLogin(w, r, vm, password)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	vm := AuthVM{Email: r.PostForm.Get("email"), Next: safeNext(r.PostForm.Get("next"), "")}
	s.finishLogin(w, r, vm, r.PostForm.Get("password"))
}

func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, vm AuthVM, password string) {
	token, _, err := s.d.Auth.Login(r.Context(), vm.Email, password)
	if err != nil {
		if key := authErrorKey(err); key != "" {
			vm.SignUp = false
			vm.Error = s.localizer(r).T(key)
			s.authPage(w, r, http.StatusUnauthorized, vm)
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.d.Auth.SetSessionCookie(w, token)
	http.Redirect(w, r, safeNext(vm.Next, "/vendor"), http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.d.Auth.Logout(r.Context(), s.d.Auth.TokenFromRequest(r)); err != nil {
		s.d.Log.ErrorContext(r.Context(), "logout failed", "error", err)
	}
	s.d.Auth.ClearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// DashboardVM is the vendor dashboard's view model.
type DashboardVM struct {
	Shops []db.ListShopsByOwnerRow
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	shops, err := s.d.Store.ListShopsByOwner(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "vendor_dashboard.html", s.page(r, s.localizer(r).T("vendor.dashboard"), DashboardVM{Shops: shops}))
}

// ShopFormVM is the shop create/edit form's view model.
type ShopFormVM struct {
	Action, Cancel string
	Name, Address  string
	Description    string
	AreaID         int64
	AreaGroups     []AreaGroup
	Lat, Lng       string
	Phone          string
	OpeningHours   string
	MapLat, MapLng float64
	MapZoom        int
	Error          string
}

// AreaGroup is one district in the shop form's area picker: the district
// itself (for shops whose city area is not in the upazila list) and its
// upazilas.
type AreaGroup struct {
	DistrictID int64
	NameEn     string
	NameBn     string
	Upazilas   []db.ListUpazilasRow
}

func (s *Server) areaGroups(r *http.Request) ([]AreaGroup, error) {
	districts, _, err := s.reference(r.Context())
	if err != nil {
		return nil, err
	}
	upazilas, err := s.d.Store.ListUpazilas(r.Context())
	if err != nil {
		return nil, err
	}
	byDistrict := map[int64][]db.ListUpazilasRow{}
	for _, u := range upazilas {
		byDistrict[u.DistrictID] = append(byDistrict[u.DistrictID], u)
	}
	groups := make([]AreaGroup, 0, len(districts))
	for _, d := range districts {
		groups = append(groups, AreaGroup{DistrictID: d.ID, NameEn: d.NameEn, NameBn: d.NameBn, Upazilas: byDistrict[d.ID]})
	}
	return groups, nil
}

// vendorErrorText translates a vendor validation error.
func (s *Server) vendorErrorText(l *Localizer, err error) (string, bool) {
	var ve *vendor.ValidationError
	if !errors.As(err, &ve) {
		return "", false
	}
	key := "verr." + ve.Field + "." + ve.Code
	if msg := l.T(key); msg != key {
		return msg, true
	}
	return l.T("verr.generic"), true
}

func (s *Server) vendorFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, vendor.ErrNotFound), errors.Is(err, vendor.ErrForbidden):
		// Not revealing whether someone else's shop exists.
		s.renderError(w, r, http.StatusNotFound, "error.not_found")
	default:
		s.serverError(w, r, err)
	}
}

func (s *Server) shopFormPage(w http.ResponseWriter, r *http.Request, status int, vm ShopFormVM, isNew bool) {
	l := s.localizer(r)
	groups, err := s.areaGroups(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	vm.AreaGroups = groups
	title := l.T("shopform.edit_title")
	if isNew {
		title = l.T("shopform.new_title")
	}
	if p, err := geo.ParsePoint(vm.Lat, vm.Lng); err == nil && vm.Lat != "" {
		vm.MapLat, vm.MapLng, vm.MapZoom = p.Lat, p.Lng, 15
	} else {
		districts, _, err := s.reference(r.Context())
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		vm.MapZoom = 7
		for _, d := range districts {
			if d.NameEn == "Dhaka" {
				vm.MapLat, vm.MapLng = d.Lat, d.Lng
			}
		}
	}
	s.render(w, r, status, "vendor_shop_form.html", s.page(r, title, vm))
}

func (s *Server) handleShopForm(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") == "" {
		s.shopFormPage(w, r, http.StatusOK, ShopFormVM{Action: "/vendor/shops/new", Cancel: "/vendor"}, true)
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		s.renderError(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	shop, err := s.d.Vendor.OwnedShop(r.Context(), *s.currentUser(r), id)
	if err != nil {
		s.vendorFail(w, r, err)
		return
	}
	s.shopFormPage(w, r, http.StatusOK, ShopFormVM{
		Action: fmt.Sprintf("/vendor/shops/%d/edit", id), Cancel: fmt.Sprintf("/vendor/shops/%d", id),
		Name: shop.Name, Description: shop.Description, Address: shop.Address, AreaID: shop.AreaID, Lat: coord(shop.Lat), Lng: coord(shop.Lng),
		Phone: shop.Phone, OpeningHours: shop.OpeningHours,
	}, false)
}

func (s *Server) handleShopSave(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	u := *s.currentUser(r)
	isNew := r.PathValue("id") == ""
	id, ok := pathID(r, "id")
	if !isNew && !ok {
		s.renderError(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	f := r.PostForm
	vm := ShopFormVM{
		Action: r.URL.Path, Cancel: "/vendor", Name: f.Get("name"), Description: f.Get("description"), Address: f.Get("address"), AreaID: formID(r, "area"),
		Lat: domain.ToASCIIDigits(strings.TrimSpace(f.Get("lat"))), Lng: domain.ToASCIIDigits(strings.TrimSpace(f.Get("lng"))),
		Phone: domain.ToASCIIDigits(f.Get("phone")), OpeningHours: f.Get("opening_hours"),
	}
	in := vendor.ShopInput{Name: vm.Name, Description: vm.Description, Address: vm.Address, AreaID: vm.AreaID, Phone: vm.Phone, OpeningHours: vm.OpeningHours}
	if p, err := geo.ParsePoint(vm.Lat, vm.Lng); err == nil {
		in.Location = p
	}
	var err error
	if isNew {
		id, err = s.d.Vendor.CreateShop(r.Context(), u, in)
	} else {
		err = s.d.Vendor.UpdateShop(r.Context(), u, id, in)
	}
	if err != nil {
		if msg, ok := s.vendorErrorText(s.localizer(r), err); ok {
			vm.Error = msg
			s.shopFormPage(w, r, http.StatusUnprocessableEntity, vm, isNew)
			return
		}
		s.vendorFail(w, r, err)
		return
	}
	redirectFlash(w, r, fmt.Sprintf("/vendor/shops/%d", id), "flash.shop_saved")
}

// VendorListingVM is one of the vendor's own listings.
type VendorListingVM struct {
	ID            int64
	ProductID     int64
	NameEn        string
	NameBn        string
	ProductStatus string
	Price         domain.Money
	InStock       bool
	UnderReview   bool
	AutoHidden    bool
	UpdatedAt     time.Time
	Freshness     domain.Freshness
}

// BulkVM reports the outcome of a CSV upload.
type BulkVM struct {
	Applied int
	Flagged int
	Errors  []vendor.RowError
	Fatal   string
}

// VendorShopVM is the shop management page's view model.
type VendorShopVM struct {
	Shop         db.GetShopRow
	Listings     []VendorListingVM
	ProductQuery string
	Products     []pricefinder.Product
	Bulk         *BulkVM
	CSVMaxRows   int
}

func (s *Server) vendorShopVM(r *http.Request, id int64) (VendorShopVM, error) {
	shop, err := s.d.Vendor.OwnedShop(r.Context(), *s.currentUser(r), id)
	if err != nil {
		return VendorShopVM{}, err
	}
	rows, err := s.d.Store.ListListingsByShop(r.Context(), id)
	if err != nil {
		return VendorShopVM{}, fmt.Errorf("web: list listings: %w", err)
	}
	now := time.Now()
	policy := domain.FreshnessPolicy{FreshFor: s.d.Cfg.ListingFreshFor, StaleFor: s.d.Cfg.ListingStaleFor}
	vm := VendorShopVM{Shop: shop, CSVMaxRows: s.d.Cfg.CSVMaxRows, ProductQuery: strings.TrimSpace(r.URL.Query().Get("pq"))}
	for _, row := range rows {
		vm.Listings = append(vm.Listings, VendorListingVM{
			ID: row.ID, ProductID: row.ProductID, NameEn: row.NameEn, NameBn: row.NameBn, ProductStatus: row.ProductStatus,
			Price: domain.Money(row.PricePoisha), InStock: row.InStock, UnderReview: row.AnomalyFlagged, AutoHidden: row.AutoHidden,
			UpdatedAt: row.UpdatedAt, Freshness: policy.Of(row.UpdatedAt, now),
		})
	}
	if vm.ProductQuery != "" {
		vm.Products, err = s.d.Finder.Candidates(r.Context(), search.ParseQuery(vm.ProductQuery), nil, 10)
		if err != nil {
			return VendorShopVM{}, err
		}
	}
	return vm, nil
}

func (s *Server) handleVendorShop(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.renderError(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	vm, err := s.vendorShopVM(r, id)
	if err != nil {
		s.vendorFail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "vendor_shop.html", s.page(r, vm.Shop.Name, vm))
}

// handleProductOptions is the search-as-you-type product picker (HTMX).
func (s *Server) handleProductOptions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.renderError(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	vm, err := s.vendorShopVM(r, id)
	if err != nil {
		s.vendorFail(w, r, err)
		return
	}
	s.renderNamed(w, r, http.StatusOK, "vendor_shop.html", "product_options", s.page(r, "", vm))
}

func (s *Server) handleListingSet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || !parseForm(w, r) {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	back := fmt.Sprintf("/vendor/shops/%d", id)
	price, err := sources.ParsePrice(r.PostForm.Get("price"))
	if err != nil {
		redirectFlash(w, r, back, "flash.err.price")
		return
	}
	res, err := s.d.Vendor.SetListing(r.Context(), *s.currentUser(r), id, formID(r, "product"), price, r.PostForm.Get("in_stock") != "")
	var ve *vendor.ValidationError
	switch {
	case errors.As(err, &ve):
		redirectFlash(w, r, back, "flash.err.listing")
	case err != nil:
		s.vendorFail(w, r, err)
	case res.Flagged:
		redirectFlash(w, r, back, "flash.listing_flagged")
	default:
		redirectFlash(w, r, back, "flash.listing_saved")
	}
}

func (s *Server) handleListingDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || !parseForm(w, r) {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	if err := s.d.Vendor.DeleteListing(r.Context(), *s.currentUser(r), id); err != nil {
		s.vendorFail(w, r, err)
		return
	}
	back := "/vendor"
	if shopID := formID(r, "shop"); shopID > 0 {
		back = fmt.Sprintf("/vendor/shops/%d", shopID)
	}
	redirectFlash(w, r, back, "flash.listing_deleted")
}

func (s *Server) handleBulkUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.renderError(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	l := s.localizer(r)
	bulk := &BulkVM{}
	// Explicit upload cap: the CSV limit plus a little multipart overhead.
	r.Body = http.MaxBytesReader(w, r.Body, s.d.Cfg.CSVMaxBytes+8<<10)
	file, _, err := r.FormFile("csv")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			bulk.Fatal = l.T("bulk.fatal.too_large")
		} else {
			bulk.Fatal = l.T("bulk.fatal.no_file")
		}
	} else {
		defer func() { _ = file.Close() }()
		res, err := s.d.Vendor.BulkUpload(r.Context(), *s.currentUser(r), id, file)
		bulk.Applied, bulk.Flagged, bulk.Errors = res.Applied, res.Flagged, res.Errors
		switch {
		case errors.Is(err, vendor.ErrCSVEmpty):
			bulk.Fatal = l.T("bulk.fatal.empty")
		case errors.Is(err, vendor.ErrCSVHeader):
			bulk.Fatal = l.T("bulk.fatal.header")
		case errors.Is(err, vendor.ErrCSVTooManyRow):
			bulk.Fatal = l.T("bulk.fatal.too_many", l.Num(s.d.Cfg.CSVMaxRows))
		case errors.Is(err, vendor.ErrCSVMalformed):
			bulk.Fatal = l.T("bulk.fatal.malformed")
		case err != nil:
			s.vendorFail(w, r, err)
			return
		}
	}
	vm, err := s.vendorShopVM(r, id)
	if err != nil {
		s.vendorFail(w, r, err)
		return
	}
	vm.Bulk = bulk
	status := http.StatusOK
	if bulk.Fatal != "" {
		status = http.StatusUnprocessableEntity
	}
	s.render(w, r, status, "vendor_shop.html", s.page(r, vm.Shop.Name, vm))
}

// handleCatalogCSV lets vendors download the product IDs for bulk uploads.
func (s *Server) handleCatalogCSV(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d.Store.ListActiveProducts(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ponnokhoj-catalog.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"product_id", "name_en", "name_bn", "brand", "size", "category"})
	for _, p := range rows {
		_ = cw.Write([]string{
			strconv.FormatInt(p.ID, 10), csvSafe(p.NameEn), csvSafe(p.NameBn), csvSafe(p.Brand),
			domain.Quantity{Value: p.Quantity, Unit: domain.Unit(p.Unit)}.Format(false), csvSafe(p.CategoryEn),
		})
	}
	cw.Flush()
}

// csvSafe defuses spreadsheet formula injection in exported text cells.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// ProposeVM is the product proposal form's view model.
type ProposeVM struct {
	ShopID     int64
	NameEn     string
	NameBn     string
	Brand      string
	Model      string
	CategoryID int64
	Size       string
	Categories []db.Category
	Error      string
}

func (s *Server) proposePage(w http.ResponseWriter, r *http.Request, status int, vm ProposeVM) {
	_, categories, err := s.reference(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	vm.Categories = categories
	s.render(w, r, status, "vendor_product_new.html", s.page(r, s.localizer(r).T("vendor.propose_product"), vm))
}

func (s *Server) handleProposeForm(w http.ResponseWriter, r *http.Request) {
	s.proposePage(w, r, http.StatusOK, ProposeVM{ShopID: queryID(r.URL.Query(), "shop")})
}

func (s *Server) handlePropose(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		s.renderError(w, r, http.StatusBadRequest, "error.bad_request")
		return
	}
	f := r.PostForm
	vm := ProposeVM{
		ShopID: formID(r, "shop"), NameEn: f.Get("name_en"), NameBn: f.Get("name_bn"), Brand: f.Get("brand"),
		Model: f.Get("model"), CategoryID: formID(r, "category"), Size: f.Get("size"),
	}
	id, err := s.d.Vendor.ProposeProduct(r.Context(), *s.currentUser(r), vendor.ProductInput{
		NameEn: vm.NameEn, NameBn: vm.NameBn, Brand: vm.Brand, Model: vm.Model, CategoryID: vm.CategoryID, Size: vm.Size,
	})
	if err != nil {
		if msg, ok := s.vendorErrorText(s.localizer(r), err); ok {
			vm.Error = msg
			s.proposePage(w, r, http.StatusUnprocessableEntity, vm)
			return
		}
		s.vendorFail(w, r, err)
		return
	}
	_ = id
	back := "/vendor"
	if vm.ShopID > 0 {
		back = fmt.Sprintf("/vendor/shops/%d", vm.ShopID)
	}
	redirectFlash(w, r, back, "flash.product_proposed")
}
