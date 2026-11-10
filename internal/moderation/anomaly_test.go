package moderation

import "testing"

func TestIsAnomalous(t *testing.T) {
	t.Parallel()
	five := []int64{9000, 10000, 10000, 11000, 12000} // median 10000
	tests := []struct {
		name        string
		price       int64
		comparables []int64
		want        bool
	}{
		{"normal price", 10500, five, false},
		{"exactly 3x is allowed", 30000, five, false},
		{"just over 3x", 30001, five, true},
		{"exactly a third is allowed", 3334, five, false},
		{"just under a third", 3333, five, true},
		{"typo ten times too high", 100000, five, true},
		{"too few comparables: never flagged", 100000, five[:4], false},
		{"no comparables", 100000, nil, false},
		{"non-positive price", 0, five, false},
	}
	for _, tc := range tests {
		if got := IsAnomalous(tc.price, tc.comparables); got != tc.want {
			t.Errorf("%s: IsAnomalous(%d) = %v, want %v", tc.name, tc.price, got, tc.want)
		}
	}
	if IsAnomalous(100, []int64{0, 0, 0, 0, 0}) {
		t.Error("zero median should not flag")
	}
}

func TestMedianInt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   []int64
		want int64
	}{
		{nil, 0}, {[]int64{7}, 7}, {[]int64{3, 1, 2}, 2}, {[]int64{4, 1, 3, 2}, 2}, {[]int64{10, 20}, 15},
		{[]int64{9223372036854775807, 9223372036854775807}, 9223372036854775807}, // no overflow
	}
	for _, tc := range tests {
		if got := MedianInt(tc.in); got != tc.want {
			t.Errorf("MedianInt(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
	in := []int64{3, 1, 2}
	MedianInt(in)
	if in[0] != 3 {
		t.Error("MedianInt sorted its input in place")
	}
}

func TestReporterHash(t *testing.T) {
	t.Parallel()
	s := NewService(nil, 3, "secret-one")
	a, b := s.ReporterHash("203.0.113.7"), s.ReporterHash("203.0.113.8")
	if len(a) != 32 || string(a) == string(b) {
		t.Error("hash is not 32 bytes or does not distinguish reporters")
	}
	if string(a) != string(s.ReporterHash("203.0.113.7")) {
		t.Error("hash is not stable")
	}
	if string(a) == string(NewService(nil, 3, "secret-two").ReporterHash("203.0.113.7")) {
		t.Error("hash does not depend on the secret")
	}
}
