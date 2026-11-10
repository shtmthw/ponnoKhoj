package search

import (
	"sort"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
)

// Candidate is a product matched by text similarity, before ranking.
type Candidate struct {
	ProductID  int64
	Similarity float64 // trigram word similarity in [0, 1]
	Quantity   *domain.Quantity
}

// Ranking weights. A quantity match is a strong signal ("5 kg" asked, 5 kg
// product) but must not outrank a clearly better text match.
const (
	quantityMatchBoost    = 0.25
	quantityMismatchMalus = 0.15
)

// Score combines text similarity with quantity agreement.
func Score(q Query, c Candidate) float64 {
	s := c.Similarity
	if q.Quantity != nil && c.Quantity != nil {
		if *q.Quantity == *c.Quantity {
			s += quantityMatchBoost
		} else {
			s -= quantityMismatchMalus
		}
	}
	return s
}

// Rank orders candidates best-first by Score, breaking ties by product ID so
// results are stable across requests. It sorts in place and returns cands.
func Rank(q Query, cands []Candidate) []Candidate {
	sort.SliceStable(cands, func(i, j int) bool {
		si, sj := Score(q, cands[i]), Score(q, cands[j])
		if si != sj {
			return si > sj
		}
		return cands[i].ProductID < cands[j].ProductID
	})
	return cands
}
