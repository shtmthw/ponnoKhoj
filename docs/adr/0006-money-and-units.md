# 0006. Money as int64 poisha; listings priced per product unit

- Status: accepted
- Date: 2026-11-10

## Decision
All prices are `int64` poisha (1 taka = 100 poisha) in Go and `bigint` in SQL; never floats. A listing is the price of one product as defined (e.g. "Miniket Rice 5 kg"), not a per-kg price; quantities are normalized to base units (g, ml, pc) for matching and comparison only. Display formatting (৳, Bangla digits) happens in the localizer.

## Consequences
- No rounding drift; the estimator computes in float64 and rounds to whole taka at the end.
- A vendor selling rice loose by the kg lists the 1 kg product, not the 5 kg one.
