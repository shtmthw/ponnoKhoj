// Package moderation holds the trust-and-safety rules: the price anomaly
// guard, shopper reports with auto-hide, and the admin review actions. Every
// mutation is written to the audit log.
package moderation

import "sort"

// Anomaly guard parameters: a listing priced above AnomalyFactor times, or
// below 1/AnomalyFactor of, the local median is flagged, but only when at
// least MinComparables other prices exist.
const (
	AnomalyFactor  = 3
	MinComparables = 5
)

// IsAnomalous applies the price anomaly guard. comparables are the prices of
// the same product at other trustworthy shops nearby (in poisha).
func IsAnomalous(price int64, comparables []int64) bool {
	if len(comparables) < MinComparables || price <= 0 {
		return false
	}
	med := MedianInt(comparables)
	if med <= 0 {
		return false
	}
	// price > 3 x median, or price < median / 3 (written without division).
	return price > AnomalyFactor*med || price*AnomalyFactor < med
}

// MedianInt returns the median of xs (the mean of the two middle values,
// rounded down, for an even count). It returns 0 for an empty slice.
func MedianInt(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]int64(nil), xs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	a, b := sorted[n/2-1], sorted[n/2]
	return a + (b-a)/2
}
