# 0010. Coverage: merged unit + integration profiles

- Status: accepted
- Date: 2026-11-10

## Context
The brief sets overall coverage ≥ 70% and ≥ 85% for domain, search normalization, price parsing and estimate baseline/validation. Much of `store`, `jobs`, `web` and `pricefinder` is only meaningfully testable against a real database.

## Decision
`make test` writes `unit.out`; `make test-integration` runs with `-coverpkg=./internal/...` and writes `integration.out`; `tools/covercheck` merges them block by block (covered if any profile covered it) and enforces `test/coverage-thresholds.json`. "Overall" is computed over `internal/` (the `cmd/` entry points and `tools/` are excluded). The per-package floors are per-file for the named files.

## Consequences
- A statement covered only by integration tests counts. The 85% floors are met by unit tests alone.
- Last measured merged overall: 85.1%.
