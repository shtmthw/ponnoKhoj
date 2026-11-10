package geo

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestNewPoint(t *testing.T) {
	t.Parallel()
	if _, err := NewPoint(22.3351, 91.8341); err != nil {
		t.Fatalf("valid point rejected: %v", err)
	}
	for _, c := range [][2]float64{{91, 0}, {-91, 0}, {0, 181}, {0, -181}, {math.NaN(), 0}, {0, math.NaN()}, {math.Inf(1), 0}} {
		if _, err := NewPoint(c[0], c[1]); !errors.Is(err, ErrInvalidPoint) {
			t.Errorf("NewPoint(%v, %v) err = %v, want ErrInvalidPoint", c[0], c[1], err)
		}
	}
}

func TestParsePoint(t *testing.T) {
	t.Parallel()
	p, err := ParsePoint("22.3351", "91.8341")
	if err != nil || p.Lat != 22.3351 || p.Lng != 91.8341 {
		t.Fatalf("ParsePoint = %+v, %v", p, err)
	}
	for _, c := range [][2]string{{"", "91"}, {"22", ""}, {"abc", "91"}, {"22", "east"}, {"95", "91"}} {
		if _, err := ParsePoint(c[0], c[1]); !errors.Is(err, ErrInvalidPoint) {
			t.Errorf("ParsePoint(%q, %q) err = %v", c[0], c[1], err)
		}
	}
}

func TestCell(t *testing.T) {
	t.Parallel()
	p := Point{Lat: 22.335109, Lng: 91.834073}
	cell := p.Cell()
	if len(cell) != CellPrecision {
		t.Fatalf("cell %q has wrong precision", cell)
	}
	center, ok := CellCenter(cell)
	if !ok {
		t.Fatal("CellCenter rejected its own cell")
	}
	if center.Cell() != cell {
		t.Errorf("cell center %v is not inside its cell %q", center, cell)
	}
	// The cell is about a kilometre across, so the center is close to p.
	if d := DistanceMeters(p, center); d > 1000 {
		t.Errorf("cell center is %.0f m from the point", d)
	}
	for _, bad := range []string{"", "abc", "wh0r3p!", "aaaaaaa", "ilo000"} {
		if _, ok := CellCenter(bad); ok {
			t.Errorf("CellCenter(%q) accepted", bad)
		}
	}
}

func TestDistanceMeters(t *testing.T) {
	t.Parallel()
	a := Point{Lat: 22.0, Lng: 91.0}
	if d := DistanceMeters(a, a); d != 0 {
		t.Errorf("distance to self = %v", d)
	}
	// One degree of latitude is ~111.2 km on the mean sphere.
	d := DistanceMeters(a, Point{Lat: 23.0, Lng: 91.0})
	if math.Abs(d-111_195) > 100 {
		t.Errorf("1 degree latitude = %.0f m, want ~111195", d)
	}
	if ab, ba := DistanceMeters(a, Point{Lat: 23.7, Lng: 90.4}), DistanceMeters(Point{Lat: 23.7, Lng: 90.4}, a); math.Abs(ab-ba) > 1e-6 {
		t.Errorf("distance is not symmetric: %v vs %v", ab, ba)
	}
}

func TestRadius(t *testing.T) {
	t.Parallel()
	for in, want := range map[int]int{1: 1000, 1000: 1000, 1001: 3000, 3000: 3000, 4000: 5000, 9999: 10000, 25000: 25000, 99999: 25000} {
		if got := BucketRadius(in); got != want {
			t.Errorf("BucketRadius(%d) = %d, want %d", in, got, want)
		}
	}
	for _, tc := range [][4]int{{0, 3000, 25000, 3000}, {-5, 3000, 25000, 3000}, {500, 3000, 25000, 500}, {90000, 3000, 25000, 25000}} {
		if got := ClampRadius(tc[0], tc[1], tc[2]); got != tc[3] {
			t.Errorf("ClampRadius(%d) = %d, want %d", tc[0], got, tc[3])
		}
	}
}

func TestDirectionsURL(t *testing.T) {
	t.Parallel()
	got := DirectionsURL(Point{Lat: 22.335109, Lng: 91.834073})
	if !strings.HasPrefix(got, "https://www.openstreetmap.org/directions?to=22.335109%2C91.834073") {
		t.Errorf("DirectionsURL = %q", got)
	}
}
