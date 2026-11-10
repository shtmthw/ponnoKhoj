# Architecture decision records

One file per significant decision: context, options, decision, consequences. Copy `0000-template.md` for a new one.

- [0001](0001-modular-monolith.md) One binary, modular monolith
- [0002](0002-goose-migrations-advisory-lock.md) goose for migrations, guarded by an advisory lock
- [0003](0003-sqlc-pgx.md) sqlc + pgx v5, generated code committed
- [0004](0004-river-periodic-jobs.md) River for background work, periodic jobs instead of CronJobs
- [0005](0005-gateway-api-envoy-gateway.md) Gateway API with Envoy Gateway
- [0006](0006-money-and-units.md) Money as int64 poisha; listings priced per product unit
- [0007](0007-in-memory-rate-limits.md) In-memory per-IP rate limits
- [0008](0008-map-glue-script.md) Vendored Leaflet + HTMX and one glue script
- [0009](0009-secrets.md) Secrets: secretGenerator in dev, External Secrets in prod
- [0010](0010-coverage-measurement.md) Coverage: merged unit + integration profiles
- [0011](0011-online-sources-product-pages.md) Online sources: product pages + structured data only, per-store review
- [0012](0012-location-scopes.md) Three location scopes: point radius, upazila, district
- [0013](0013-trigram-operator-cost.md) Raise pg_trgm operator cost so the GIN index is used
- [0014](0014-llm-openrouter.md) LLM estimates through an OpenAI-compatible API (OpenRouter free model in dev)
- [0015](0015-ascii-search-key.md) ASCII transliterated search keys for bilingual search
- [0016](0016-offline-ci-overlay.md) An offline `ci` overlay for the e2e gate
