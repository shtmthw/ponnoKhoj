# 0002. goose for migrations, guarded by an advisory lock

- Status: accepted
- Date: 2026-11-10

## Context
Migrations must be embedded, have down steps, follow expand/contract, run as a Kubernetes Job before the rollout (never on app start), and be safe if two Jobs race. River (the job queue) has its own schema migrations.

## Options
1. goose v3 with `go:embed` SQL files.
2. golang-migrate.

## Decision
goose v3 (`internal/migrations`). `ponnokhoj migrate up|down|status` takes a Postgres session advisory lock, then runs the goose migrations and River's own migrations under the same lock. Each SQL file has `-- +goose Down`.

## Consequences
- Integration tests run up → down → up against PostGIS on every CI run.
- Expand/contract: a release only adds columns/tables with defaults; drops happen one release later. `00004` (shop description, product pages) is additive.
- The Job is named after the image tag (`devctl deploy`) because Job templates are immutable.
