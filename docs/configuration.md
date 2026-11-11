# Configuration

All configuration is environment variables, validated at startup. `ponnokhoj config check` prints the effective values with
secrets redacted.

This table is generated from `internal/config` and a test fails if they drift
(regenerate: `PONNOKHOJ_UPDATE_CONFIG_DOC=1 go test ./internal/config -run ConfigDoc`).

<!-- config-table:start -->
| Variable | Default | Description |
|---|---|---|
| `PONNOKHOJ_ENV` | `dev` | `dev` or `prod`. `prod` turns on HSTS and defaults logs to JSON. |
| `HTTP_ADDR` | `:8080` | Listen address for the web UI and JSON API. |
| `METRICS_ADDR` | `:9090` | Listen address for Prometheus `/metrics` (never routed through the Gateway). |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | Public origin of the site; used in the fetch client's User-Agent contact URL. |
| `DATABASE_URL` | **required** | PostgreSQL connection string (PostGIS and pg_trgm required). |
| `DB_MAX_CONNS` | `10` | Maximum connections in the pool. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `LOG_FORMAT` | — | `json` or `text`. Defaults to `json` in prod and `text` in dev. |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | After SIGTERM, how long readiness fails before the server stops accepting requests. |
| `SHUTDOWN_TIMEOUT` | `20s` | Deadline for in-flight requests and jobs to finish after the drain delay. |
| `APP_SECRET` | **required** | At least 32 characters. Keys the hash that anonymizes report submitters. |
| `TRUSTED_PROXIES` | — | Comma-separated CIDRs whose `X-Forwarded-For` is honored. Empty trusts nobody. |
| `COOKIE_SECURE` | `true` | Set the `Secure` flag on the session cookie. Only disable for plain-HTTP local development. |
| `SESSION_TTL` | `168h` | Lifetime of a vendor/admin session. |
| `AUTO_VERIFY_SHOPS` | `false` | Mark new shops verified without admin review (development only). |
| `REPORT_HIDE_THRESHOLD` | `3` | Distinct open reports that auto-hide a listing or shop pending review. |
| `SEARCH_DEFAULT_RADIUS_M` | `3000` | Local search radius when none is given, in meters. |
| `SEARCH_MAX_RADIUS_M` | `25000` | Largest accepted search radius, in meters. |
| `LISTING_FRESH_FOR` | `168h` | Listings updated within this are fresh. |
| `LISTING_STALE_FOR` | `720h` | Listings older than fresh but within this are stale; older ones are hidden. |
| `RATE_LIMIT_SEARCH_PER_MIN` | `60` | Search requests per client IP per minute. |
| `RATE_LIMIT_ESTIMATE_PER_MIN` | `10` | Estimate requests per client IP per minute. |
| `RATE_LIMIT_AUTH_PER_MIN` | `10` | Login and sign-up attempts per client IP per minute. |
| `RATE_LIMIT_REPORT_PER_MIN` | `5` | Report submissions per client IP per minute. |
| `CSV_MAX_BYTES` | `1048576` | Largest accepted bulk-upload CSV, in bytes. |
| `CSV_MAX_ROWS` | `2000` | Most data rows accepted in one bulk upload. |
| `MAP_TILE_URL` | `https://tile.openstreetmap.org/{z}/{x}/{y}.png` | Leaflet tile URL template. The default is OpenStreetMap's server; follow its tile usage policy. |
| `MAP_TILE_ATTRIBUTION` | `© OpenStreetMap contributors` | Attribution shown on every map. |
| `SOURCES_ENABLED` | — | Comma-separated online source IDs to enable. Every source is off unless listed (see docs/sources.md). |
| `DEMO_SHOP_ENABLED` | `false` | Serve the built-in fictional online shop under `/demo-shop/` (demo data only). |
| `DEMO_SHOP_BASE_URL` | `http://localhost:8080/demo-shop` | Where the worker reaches the demo shop when the `demoshop` source is enabled. |
| `OFFER_CACHE_TTL` | `6h` | Online offers older than this trigger a background refresh (stale-while-revalidate). |
| `OFFER_MAX_AGE` | `168h` | Online offers older than this are no longer shown. |
| `ESTIMATE_TTL` | `24h` | How long an estimate is cached per product, ~1 km cell and radius bucket. |
| `ESTIMATE_MIN_EVIDENCE` | `3` | Independent evidence items required before any estimate is produced. |
| `ESTIMATE_ENVELOPE_LOW` | `0.5` | An estimate's low bound may not fall below this multiple of the cheapest evidence. |
| `ESTIMATE_ENVELOPE_HIGH` | `1.5` | An estimate's high bound may not exceed this multiple of the dearest evidence. |
| `LLM_ENABLED` | `false` | Enable LLM-assisted refinement of estimates. Off means statistical estimates only. |
| `LLM_BASE_URL` | — | OpenAI-compatible base URL, e.g. `http://localhost:11434/v1` for Ollama. Required when LLM_ENABLED. |
| `LLM_MODEL` | — | Model name sent to the LLM endpoint. Required when LLM_ENABLED. |
| `LLM_API_KEY` | — | Bearer token for the LLM endpoint (may be empty for local Ollama). |
| `LLM_MAX_STEPS` | `6` | Maximum tool-calling steps per estimate. |
| `LLM_TIMEOUT` | `20s` | Total time budget for the LLM on one estimate. |
| `LLM_MAX_TOKENS` | `1024` | Completion token cap per LLM call. |
| `OTEL_TRACING_ENABLED` | `false` | Export OpenTelemetry traces over OTLP/HTTP (configure the exporter with the standard `OTEL_EXPORTER_OTLP_*` variables). |
| `WORKER_CONCURRENCY` | `5` | Jobs the worker runs at once. |
| `SEED_ADMIN_PASSWORD` | — | Password for the demo admin account. Read only by `ponnokhoj seed --demo`. |
| `SEED_VENDOR_PASSWORD` | — | Password for the demo vendor accounts. Read only by `ponnokhoj seed --demo`. |
<!-- config-table:end -->
