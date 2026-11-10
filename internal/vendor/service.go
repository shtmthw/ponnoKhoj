// Package vendor implements what offline vendors do: register shops (with a
// pin dropped on a map), list prices one by one or in bulk, and propose
// products that are missing from the catalog. Every mutation is audited.
package vendor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shtmthw/ponnoKhoj/internal/auth"
	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/moderation"
	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// AnomalyRadiusM is the radius within which other shops' prices form the
// local median for the price anomaly guard.
const AnomalyRadiusM = 10_000

// ValidationError is a user-fixable problem with one form field. Code is a
// stable identifier the UI translates.
type ValidationError struct {
	Field string
	Code  string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("vendor: invalid %s (%s)", e.Field, e.Code)
}

func invalid(field, code string) error { return &ValidationError{Field: field, Code: code} }

// Service errors.
var (
	ErrNotFound  = errors.New("vendor: not found")
	ErrForbidden = errors.New("vendor: not your shop")
)

// Config configures the vendor service.
type Config struct {
	AutoVerifyShops bool
	Freshness       domain.FreshnessPolicy
	CSVMaxRows      int
}

// Service implements vendor operations.
type Service struct {
	store *store.Store
	cfg   Config
	now   func() time.Time
}

// NewService builds the vendor service.
func NewService(st *store.Store, cfg Config) *Service {
	return &Service{store: st, cfg: cfg, now: time.Now}
}

// ShopInput is a shop as submitted by its owner.
type ShopInput struct {
	Name string
	// Description says what the shop sells and how it trades, so an admin
	// can judge the shop before verifying it.
	Description  string
	Address      string
	AreaID       int64
	Location     geo.Point
	Phone        string
	OpeningHours string
}

// Validate normalizes and checks a shop submission.
func (in *ShopInput) Validate() error {
	in.Name = oneLine(in.Name)
	in.Description = paragraph(in.Description)
	in.Address = oneLine(in.Address)
	in.Phone = oneLine(in.Phone)
	in.OpeningHours = oneLine(in.OpeningHours)
	switch {
	case in.Name == "" || utf8.RuneCountInString(in.Name) > 120:
		return invalid("name", "length")
	case utf8.RuneCountInString(in.Description) < MinShopDescription || utf8.RuneCountInString(in.Description) > MaxShopDescription:
		return invalid("description", "length")
	case in.Address == "" || utf8.RuneCountInString(in.Address) > 300:
		return invalid("address", "length")
	case in.AreaID <= 0:
		return invalid("area", "required")
	case !in.Location.Valid() || (in.Location.Lat == 0 && in.Location.Lng == 0):
		return invalid("location", "required")
	case len(in.Phone) > 30 || strings.Trim(in.Phone, "0123456789+-() ") != "":
		return invalid("phone", "format")
	case utf8.RuneCountInString(in.OpeningHours) > 200:
		return invalid("opening_hours", "length")
	}
	return nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Shop description bounds (runes).
const (
	MinShopDescription = 20
	MaxShopDescription = 1000
)

// paragraph normalizes free text that may span lines: spaces collapse
// within a line, blank-line runs collapse to one, and the ends are trimmed.
func paragraph(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, l := range lines {
		l = oneLine(l)
		if l == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// checkShopArea accepts a district (for shops whose city area is not in the
// upazila list) or an upazila.
func checkShopArea(ctx context.Context, q *db.Queries, areaID int64) error {
	area, err := q.GetArea(ctx, areaID)
	if store.IsNotFound(err) {
		return invalid("area", "unknown")
	}
	if err != nil {
		return fmt.Errorf("vendor: load area: %w", err)
	}
	if area.Kind != "district" && area.Kind != "upazila" {
		return invalid("area", "unknown")
	}
	return nil
}

// CreateShop registers a shop. It starts pending (or verified when
// auto-verify is on for development) and appears in search once verified.
func (s *Service) CreateShop(ctx context.Context, owner auth.User, in ShopInput) (int64, error) {
	if err := in.Validate(); err != nil {
		return 0, err
	}
	status := domain.ShopPending
	if s.cfg.AutoVerifyShops {
		status = domain.ShopVerified
	}
	var id int64
	err := s.store.InTx(ctx, func(q *db.Queries) error {
		if err := checkShopArea(ctx, q, in.AreaID); err != nil {
			return err
		}
		var err error
		id, err = q.CreateShop(ctx, db.CreateShopParams{
			OwnerID: owner.ID, Name: in.Name, Description: in.Description, Address: in.Address, AreaID: in.AreaID,
			Lat: in.Location.Lat, Lng: in.Location.Lng, Phone: in.Phone, OpeningHours: in.OpeningHours,
			Status: string(status),
		})
		if err != nil {
			return fmt.Errorf("vendor: create shop: %w", err)
		}
		return moderation.Audit(ctx, q, &owner.ID, "shop_created", "shop", id, map[string]any{"status": status, "area_id": in.AreaID})
	})
	return id, err
}

// UpdateShop edits a shop's details. Moving the pin or renaming a verified
// shop keeps its status; admins see the change in the audit log.
func (s *Service) UpdateShop(ctx context.Context, owner auth.User, shopID int64, in ShopInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(q *db.Queries) error {
		if err := checkShopArea(ctx, q, in.AreaID); err != nil {
			return err
		}
		n, err := q.UpdateShop(ctx, db.UpdateShopParams{
			ID: shopID, OwnerID: owner.ID, Name: in.Name, Description: in.Description, Address: in.Address, AreaID: in.AreaID,
			Lat: in.Location.Lat, Lng: in.Location.Lng, Phone: in.Phone, OpeningHours: in.OpeningHours,
		})
		if err != nil {
			return fmt.Errorf("vendor: update shop: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return moderation.Audit(ctx, q, &owner.ID, "shop_updated", "shop", shopID, nil)
	})
}

// OwnedShop loads a shop and checks that user owns it.
func (s *Service) OwnedShop(ctx context.Context, user auth.User, shopID int64) (db.GetShopRow, error) {
	shop, err := s.store.GetShop(ctx, shopID)
	if store.IsNotFound(err) {
		return db.GetShopRow{}, ErrNotFound
	}
	if err != nil {
		return db.GetShopRow{}, fmt.Errorf("vendor: load shop: %w", err)
	}
	if shop.OwnerID != user.ID {
		return db.GetShopRow{}, ErrForbidden
	}
	return shop, nil
}

// ListingResult says what setting a listing did.
type ListingResult struct {
	ID int64
	// Flagged is true when the price anomaly guard flagged the price: the
	// listing is accepted but marked for admin review.
	Flagged bool
}

// SetListing creates or updates a shop's price for a product.
func (s *Service) SetListing(ctx context.Context, owner auth.User, shopID, productID int64, price domain.Money, inStock bool) (ListingResult, error) {
	if !price.Valid() {
		return ListingResult{}, invalid("price", "range")
	}
	var res ListingResult
	err := s.store.InTx(ctx, func(q *db.Queries) error {
		var err error
		res, err = s.setListing(ctx, q, owner, shopID, productID, price, inStock, "listing_set")
		return err
	})
	return res, err
}

func (s *Service) setListing(ctx context.Context, q *db.Queries, owner auth.User, shopID, productID int64, price domain.Money, inStock bool, action string) (ListingResult, error) {
	shop, err := q.GetShop(ctx, shopID)
	if store.IsNotFound(err) {
		return ListingResult{}, ErrNotFound
	}
	if err != nil {
		return ListingResult{}, fmt.Errorf("vendor: load shop: %w", err)
	}
	if shop.OwnerID != owner.ID {
		return ListingResult{}, ErrForbidden
	}
	prod, err := q.GetProduct(ctx, productID)
	if store.IsNotFound(err) {
		return ListingResult{}, invalid("product", "unknown")
	}
	if err != nil {
		return ListingResult{}, fmt.Errorf("vendor: load product: %w", err)
	}
	if prod.Status == "rejected" {
		return ListingResult{}, invalid("product", "rejected")
	}

	comparables, err := q.LocalComparablePrices(ctx, db.LocalComparablePricesParams{
		Lat: shop.Lat, Lng: shop.Lng, ProductID: productID, ExcludeShopID: shopID,
		VisibleSince: s.now().Add(-s.cfg.Freshness.StaleFor), RadiusM: AnomalyRadiusM,
	})
	if err != nil {
		return ListingResult{}, fmt.Errorf("vendor: comparables: %w", err)
	}
	flagged := moderation.IsAnomalous(int64(price), comparables)

	previous, err := q.GetListingByShopProduct(ctx, db.GetListingByShopProductParams{ShopID: shopID, ProductID: productID})
	isNew := store.IsNotFound(err)
	if err != nil && !isNew {
		return ListingResult{}, fmt.Errorf("vendor: load listing: %w", err)
	}
	id, err := q.UpsertListing(ctx, db.UpsertListingParams{
		ShopID: shopID, ProductID: productID, PricePoisha: int64(price), Unit: prod.Unit,
		InStock: inStock, AnomalyFlagged: flagged,
	})
	if err != nil {
		return ListingResult{}, fmt.Errorf("vendor: save listing: %w", err)
	}
	if isNew || previous.PricePoisha != int64(price) {
		if err := q.InsertPriceHistory(ctx, db.InsertPriceHistoryParams{
			ListingID: id, ShopID: shopID, ProductID: productID, PricePoisha: int64(price),
		}); err != nil {
			return ListingResult{}, fmt.Errorf("vendor: price history: %w", err)
		}
	}
	detail := map[string]any{"product_id": productID, "price_poisha": int64(price), "in_stock": inStock, "anomaly_flagged": flagged}
	if !isNew {
		detail["previous_price_poisha"] = previous.PricePoisha
	}
	if err := moderation.Audit(ctx, q, &owner.ID, action, "listing", id, detail); err != nil {
		return ListingResult{}, err
	}
	return ListingResult{ID: id, Flagged: flagged}, nil
}

// DeleteListing removes one of the owner's listings.
func (s *Service) DeleteListing(ctx context.Context, owner auth.User, listingID int64) error {
	return s.store.InTx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteListing(ctx, db.DeleteListingParams{ID: listingID, OwnerID: owner.ID})
		if err != nil {
			return fmt.Errorf("vendor: delete listing: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return moderation.Audit(ctx, q, &owner.ID, "listing_deleted", "listing", listingID, nil)
	})
}

// ProductInput is a product proposed by a vendor.
type ProductInput struct {
	NameEn     string
	NameBn     string
	Brand      string
	Model      string
	CategoryID int64
	// Size is free text such as "5 kg", "৫০০ মিলি" or "1 pc".
	Size string
}

// ProposeProduct adds a product to the admin review queue. The vendor can
// list a price for it right away; it appears in search once approved.
func (s *Service) ProposeProduct(ctx context.Context, owner auth.User, in ProductInput) (int64, error) {
	in.NameEn, in.NameBn = oneLine(in.NameEn), oneLine(in.NameBn)
	in.Brand, in.Model = oneLine(in.Brand), oneLine(in.Model)
	if in.NameBn == "" {
		in.NameBn = in.NameEn
	}
	if in.NameEn == "" {
		in.NameEn = in.NameBn
	}
	switch {
	case in.NameEn == "" || utf8.RuneCountInString(in.NameEn) > 150 || utf8.RuneCountInString(in.NameBn) > 150:
		return 0, invalid("name", "length")
	case utf8.RuneCountInString(in.Brand) > 80 || utf8.RuneCountInString(in.Model) > 80:
		return 0, invalid("brand", "length")
	case in.CategoryID <= 0:
		return 0, invalid("category", "required")
	}
	qty := search.ParseQuery(in.Size).Quantity
	if qty == nil {
		return 0, invalid("size", "format")
	}
	key := search.ProductKey(in.NameEn, in.NameBn, in.Brand, in.Model, qty)
	if key == "" {
		return 0, invalid("name", "length")
	}
	var id int64
	err := s.store.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.GetCategory(ctx, in.CategoryID); err != nil {
			if store.IsNotFound(err) {
				return invalid("category", "unknown")
			}
			return fmt.Errorf("vendor: load category: %w", err)
		}
		var err error
		id, err = q.InsertProduct(ctx, db.InsertProductParams{
			CategoryID: in.CategoryID, NameEn: in.NameEn, NameBn: in.NameBn, Brand: in.Brand, Model: in.Model,
			Unit: string(qty.Unit), Quantity: qty.Value, SearchKey: key, Status: "proposed", ProposedBy: &owner.ID,
		})
		if err != nil {
			return fmt.Errorf("vendor: propose product: %w", err)
		}
		return moderation.Audit(ctx, q, &owner.ID, "product_proposed", "product", id, map[string]any{"name_en": in.NameEn})
	})
	return id, err
}

// BulkResult summarizes a CSV upload.
type BulkResult struct {
	Applied int
	Flagged int
	Errors  []RowError
}

// BulkUpload applies a CSV of prices to a shop. Valid rows are applied even
// when other rows have errors; every row error is reported back with its
// line number. The reader must already be size-capped by the caller.
func (s *Service) BulkUpload(ctx context.Context, owner auth.User, shopID int64, r io.Reader) (BulkResult, error) {
	if _, err := s.OwnedShop(ctx, owner, shopID); err != nil {
		return BulkResult{}, err
	}
	rows, rowErrs, err := ParseListingCSV(r, s.cfg.CSVMaxRows)
	if err != nil {
		return BulkResult{Errors: rowErrs}, err
	}
	res := BulkResult{Errors: rowErrs}
	for _, row := range rows {
		var lr ListingResult
		err := s.store.InTx(ctx, func(q *db.Queries) error {
			var err error
			lr, err = s.setListing(ctx, q, owner, shopID, row.ProductID, row.Price, row.InStock, "listing_bulk_set")
			return err
		})
		var ve *ValidationError
		switch {
		case err == nil:
			res.Applied++
			if lr.Flagged {
				res.Flagged++
			}
		case errors.As(err, &ve) && ve.Code == "unknown":
			res.Errors = append(res.Errors, RowError{Line: row.Line, Code: RowUnknownProd, Detail: fmt.Sprint(row.ProductID)})
		case errors.As(err, &ve):
			res.Errors = append(res.Errors, RowError{Line: row.Line, Code: RowNotAllowed, Detail: fmt.Sprint(row.ProductID)})
		default:
			return res, err
		}
	}
	return res, nil
}
