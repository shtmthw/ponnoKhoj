# 0003. sqlc + pgx v5, generated code committed

- Status: accepted
- Date: 2026-11-10

## Context
The brief requires parameterized SQL only and a generate-check gate.

## Decision
Queries live in `internal/store/queries/*.sql`; `sqlc generate` writes `internal/store/db` (committed). pgx v5 pool. PostGIS geography columns are read through `ST_Y/ST_X` in the queries (no geometry types in Go), so sqlc needs no custom type overrides beyond `numeric`/`timestamptz`.

## Consequences
- `make generate-check` fails CI when generated code is stale.
- Dynamic SQL is not possible by construction; optional filters are written as `(@x::bigint = 0 OR col = @x)`.
