package estimate

import (
	"errors"
	"math"
	"sort"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
)

// Result is an estimate: always a range, never a lone point.
type Result struct {
	Low        domain.Money
	Point      domain.Money
	High       domain.Money
	Confidence domain.Confidence
}

// ErrNoEvidence is returned when there is nothing to estimate from.
var ErrNoEvidence = errors.New("estimate: no usable evidence")

// Baseline computes the statistical estimate. It is fully deterministic:
//
//   - point: weighted median of the adjusted evidence prices;
//   - range: weighted interquartile spread around the point, widened when
//     the effective sample size is small, with a minimum relative width so
//     the range never collapses, then clamped to the evidence envelope;
//   - confidence: from evidence count, agreement and freshness.
func Baseline(ev []Evidence, p Params) (Result, error) {
	items := make([]Evidence, 0, len(ev))
	var sumW, sumW2 float64
	for _, e := range ev {
		if e.Weight <= 0 || e.AdjustedPoisha <= 0 {
			continue
		}
		items = append(items, e)
		sumW += e.Weight
		sumW2 += e.Weight * e.Weight
	}
	if len(items) == 0 || sumW <= 0 {
		return Result{}, ErrNoEvidence
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].AdjustedPoisha != items[j].AdjustedPoisha {
			return items[i].AdjustedPoisha < items[j].AdjustedPoisha
		}
		return items[i].ID < items[j].ID
	})

	point := weightedQuantile(items, sumW, 0.5)
	q25 := weightedQuantile(items, sumW, 0.25)
	q75 := weightedQuantile(items, sumW, 0.75)

	// Effective sample size (Kish): equal weights give n, one dominant
	// weight gives ~1.
	nEff := sumW * sumW / sumW2
	widen := 1 + 2/nEff
	minHalf := float64(point) * (0.05 + 0.15/math.Sqrt(nEff))
	lowHalf := math.Max(float64(point-q25)*widen, minHalf)
	highHalf := math.Max(float64(q75-point)*widen, minHalf)

	low := int64(math.Floor(float64(point) - lowHalf))
	high := int64(math.Ceil(float64(point) + highHalf))
	if point >= 10*domain.PoishaPerTaka {
		// Whole-taka bounds: an estimate is not accurate to the poisha, and
		// rounding outwards never narrows the range.
		low = low / domain.PoishaPerTaka * domain.PoishaPerTaka
		high = (high + domain.PoishaPerTaka - 1) / domain.PoishaPerTaka * domain.PoishaPerTaka
	}
	low, high = clampToEnvelope(low, high, point, items, p)

	return Result{
		Low: domain.Money(low), Point: domain.Money(point), High: domain.Money(high),
		Confidence: confidence(items, sumW, point, q25, q75, p),
	}, nil
}

// weightedQuantile returns the smallest evidence price at which the
// cumulative weight reaches q of the total. items must be sorted by price.
func weightedQuantile(items []Evidence, sumW, q float64) int64 {
	target := q * sumW
	var cum float64
	for _, e := range items {
		cum += e.Weight
		if cum >= target-1e-12 {
			return e.AdjustedPoisha
		}
	}
	return items[len(items)-1].AdjustedPoisha
}

// Envelope returns the bounds any estimate must stay within:
// [EnvelopeLow x cheapest evidence, EnvelopeHigh x dearest evidence].
func Envelope(ev []Evidence, p Params) (lo, hi int64) {
	var minP, maxP int64
	for _, e := range ev {
		if e.AdjustedPoisha <= 0 {
			continue
		}
		if minP == 0 || e.AdjustedPoisha < minP {
			minP = e.AdjustedPoisha
		}
		if e.AdjustedPoisha > maxP {
			maxP = e.AdjustedPoisha
		}
	}
	lo = max(int64(math.Floor(float64(minP)*p.EnvelopeLow)), 1)
	hi = min(int64(math.Ceil(float64(maxP)*p.EnvelopeHigh)), int64(domain.MaxMoney))
	return lo, hi
}

func clampToEnvelope(low, high, point int64, items []Evidence, p Params) (int64, int64) {
	lo, hi := Envelope(items, p)
	low = min(max(low, lo), point)
	high = max(min(high, hi), point)
	return low, high
}

// confidence grades the estimate. The thresholds are documented in
// docs/estimation.md and covered by tests.
func confidence(items []Evidence, sumW float64, point, q25, q75 int64, p Params) domain.Confidence {
	independent := Independent(SameProduct(items))
	sameProductLocal := 0
	var freshW float64
	freshDays := p.FreshFor.Hours() / 24
	for _, e := range items {
		if e.Type == EvLocal {
			sameProductLocal++
		}
		if e.AgeDays <= freshDays {
			freshW += e.Weight
		}
	}
	spread := float64(q75-q25) / float64(point)
	freshShare := freshW / sumW
	switch {
	case independent >= 8 && sameProductLocal >= 5 && spread <= 0.15 && freshShare >= 0.6:
		return domain.ConfidenceHigh
	case independent >= 4 && spread <= 0.35 && freshShare >= 0.3:
		return domain.ConfidenceMedium
	default:
		return domain.ConfidenceLow
	}
}

// confidenceRank orders confidence levels.
func confidenceRank(c domain.Confidence) int {
	switch c {
	case domain.ConfidenceHigh:
		return 2
	case domain.ConfidenceMedium:
		return 1
	default:
		return 0
	}
}

// MinConfidence returns the lower of two confidence levels.
func MinConfidence(a, b domain.Confidence) domain.Confidence {
	if confidenceRank(a) <= confidenceRank(b) {
		return a
	}
	return b
}
