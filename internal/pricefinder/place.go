package pricefinder

import (
	"context"
	"errors"
	"fmt"

	"github.com/shtmthw/ponnoKhoj/internal/geo"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// ScopeKind says how the shopper told us where they are.
type ScopeKind string

// Place scopes, from most to least precise.
const (
	// ScopePoint: an exact location (map pin, device location or typed
	// coordinates). Local search covers a radius the shopper can change.
	ScopePoint ScopeKind = "point"
	// ScopeUpazila: only an upazila. Local search covers the shops
	// registered in that upazila.
	ScopeUpazila ScopeKind = "upazila"
	// ScopeDistrict: only a district. Local search covers every shop in the
	// district, including those registered in its upazilas.
	ScopeDistrict ScopeKind = "district"
)

// Place is where the shopper is searching, as given.
type Place struct {
	Kind   ScopeKind
	Point  *geo.Point // ScopePoint
	AreaID int64      // ScopeUpazila / ScopeDistrict
}

// PointPlace builds an exact-location place.
func PointPlace(p geo.Point) *Place { return &Place{Kind: ScopePoint, Point: &p} }

// AreaPlace builds a district or upazila place; the kind is read from the
// area itself when the place is resolved.
func AreaPlace(areaID int64) *Place { return &Place{Kind: ScopeDistrict, AreaID: areaID} }

// ResolvedPlace is a Place with its names and a representative point.
type ResolvedPlace struct {
	Kind   ScopeKind
	AreaID int64
	// Area and District names (for an upazila, District is its district; for
	// a district, Area and District are the same).
	AreaEn, AreaBn         string
	DistrictEn, DistrictBn string
	// Point is the exact location (ScopePoint) or a representative point of
	// the area: a district's centroid from the area dataset, or for an
	// upazila the mean position of its shops (the dataset has no upazila
	// coordinates), falling back to its district's centroid.
	Point *geo.Point
	// Approximate is true when Point stands in for an area.
	Approximate bool
}

// ErrUnknownArea is returned for an area ID that is not a district or upazila.
var ErrUnknownArea = errors.New("pricefinder: unknown district or upazila")

// estimateRadius is the radius an area-scoped estimate starts from.
func (p ResolvedPlace) estimateRadius(requested int) int {
	switch p.Kind {
	case ScopeDistrict:
		return 10_000
	case ScopeUpazila:
		return 5_000
	default:
		return requested
	}
}

// Resolve looks up a place's names and representative point.
func (s *Service) Resolve(ctx context.Context, p *Place) (*ResolvedPlace, error) {
	if p == nil {
		return nil, nil
	}
	if p.Kind == ScopePoint {
		if p.Point == nil || !p.Point.Valid() {
			return nil, geo.ErrInvalidPoint
		}
		return &ResolvedPlace{Kind: ScopePoint, Point: p.Point}, nil
	}
	area, err := s.store.GetArea(ctx, p.AreaID)
	if store.IsNotFound(err) {
		return nil, ErrUnknownArea
	}
	if err != nil {
		return nil, fmt.Errorf("pricefinder: load area: %w", err)
	}
	r := &ResolvedPlace{AreaID: area.ID, AreaEn: area.NameEn, AreaBn: area.NameBn, Approximate: true}
	switch area.Kind {
	case "district":
		r.Kind, r.DistrictEn, r.DistrictBn = ScopeDistrict, area.NameEn, area.NameBn
		if area.HasCentroid {
			r.Point = &geo.Point{Lat: area.Lat, Lng: area.Lng}
		}
	case "upazila":
		r.Kind, r.DistrictEn, r.DistrictBn = ScopeUpazila, area.ParentEn, area.ParentBn
		c, err := s.store.AreaShopCentroid(ctx, area.ID)
		if err != nil {
			return nil, fmt.Errorf("pricefinder: upazila centre: %w", err)
		}
		if c.Shops > 0 {
			r.Point = &geo.Point{Lat: c.Lat, Lng: c.Lng}
		} else if area.ParentID != nil {
			parent, err := s.store.GetArea(ctx, *area.ParentID)
			if err != nil {
				return nil, fmt.Errorf("pricefinder: load district: %w", err)
			}
			if parent.HasCentroid {
				r.Point = &geo.Point{Lat: parent.Lat, Lng: parent.Lng}
			}
		}
	default:
		return nil, ErrUnknownArea
	}
	return r, nil
}

// Upazilas lists a district's upazilas, for the search form.
func (s *Service) Upazilas(ctx context.Context, districtID int64) ([]db.ListUpazilasByDistrictRow, error) {
	rows, err := s.store.ListUpazilasByDistrict(ctx, &districtID)
	if err != nil {
		return nil, fmt.Errorf("pricefinder: upazilas: %w", err)
	}
	return rows, nil
}
