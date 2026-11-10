// Package geo holds geographic points, radius bucketing and geohash helpers.
// It performs no I/O.
package geo

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"

	"github.com/mmcloughlin/geohash"
)

// Point is a WGS 84 position. Field order is latitude then longitude
// everywhere in Go code; the single place that flips to PostGIS's
// longitude-first order is the SQL function pk_point (see migrations).
type Point struct {
	Lat float64
	Lng float64
}

// ErrInvalidPoint is returned for coordinates outside the valid ranges.
var ErrInvalidPoint = errors.New("geo: latitude must be in [-90, 90] and longitude in [-180, 180]")

// NewPoint validates and builds a Point.
func NewPoint(lat, lng float64) (Point, error) {
	p := Point{Lat: lat, Lng: lng}
	if !p.Valid() {
		return Point{}, ErrInvalidPoint
	}
	return p, nil
}

// ParsePoint parses decimal latitude and longitude strings.
func ParsePoint(lat, lng string) (Point, error) {
	la, err := strconv.ParseFloat(lat, 64)
	if err != nil {
		return Point{}, fmt.Errorf("geo: latitude: %w", ErrInvalidPoint)
	}
	lo, err := strconv.ParseFloat(lng, 64)
	if err != nil {
		return Point{}, fmt.Errorf("geo: longitude: %w", ErrInvalidPoint)
	}
	return NewPoint(la, lo)
}

// Valid reports whether p is a real position (finite and in range).
func (p Point) Valid() bool {
	return !math.IsNaN(p.Lat) && !math.IsNaN(p.Lng) &&
		p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180
}

// CellPrecision is the geohash precision used to coarsen shopper locations:
// 6 characters is a cell of roughly 1.2 km x 0.6 km. Exact shopper
// coordinates are never persisted or logged; only this cell is.
const CellPrecision = 6

// Cell returns the geohash cell containing p at CellPrecision.
func (p Point) Cell() string {
	return geohash.EncodeWithPrecision(p.Lat, p.Lng, CellPrecision)
}

// CellCenter returns the center point of a geohash cell. It returns false for
// strings that are not valid geohashes of CellPrecision.
func CellCenter(cell string) (Point, bool) {
	if len(cell) != CellPrecision || geohash.Validate(cell) != nil {
		return Point{}, false
	}
	lat, lng := geohash.DecodeCenter(cell)
	return Point{Lat: lat, Lng: lng}, true
}

// DistanceMeters is the great-circle (haversine) distance between two points
// on a sphere of mean Earth radius. It is used for evidence weighting in
// tests and evaluation; database queries use PostGIS geography distance.
func DistanceMeters(a, b Point) float64 {
	const earthRadiusM = 6_371_008.8
	rad := math.Pi / 180
	dLat := (b.Lat - a.Lat) * rad
	dLng := (b.Lng - a.Lng) * rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusM * math.Asin(math.Min(1, math.Sqrt(h)))
}

// RadiusBuckets are the radii estimates are cached under, in meters.
var RadiusBuckets = []int{1000, 3000, 5000, 10000, 25000}

// BucketRadius rounds a radius up to the nearest bucket (the largest bucket
// for anything bigger), so nearby requests share one cached estimate.
func BucketRadius(radiusM int) int {
	for _, b := range RadiusBuckets {
		if radiusM <= b {
			return b
		}
	}
	return RadiusBuckets[len(RadiusBuckets)-1]
}

// ClampRadius applies the default for a missing radius and the configured
// maximum.
func ClampRadius(radiusM, defaultM, maxM int) int {
	if radiusM <= 0 {
		return defaultM
	}
	if radiusM > maxM {
		return maxM
	}
	return radiusM
}

// DirectionsURL builds an OpenStreetMap directions link to a destination. The
// origin is left for the shopper's device to fill in, so their position is
// not embedded in our pages.
func DirectionsURL(dest Point) string {
	q := url.Values{}
	q.Set("to", fmt.Sprintf("%.6f,%.6f", dest.Lat, dest.Lng))
	return "https://www.openstreetmap.org/directions?" + q.Encode() + fmt.Sprintf("#map=16/%.5f/%.5f", dest.Lat, dest.Lng)
}
