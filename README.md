# ponnoKhoj (পণ্যখোঁজ)

[![CI](https://github.com/shtmthw/ponnoKhoj/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/shtmthw/ponnoKhoj/actions/workflows/ci.yml)
[![Go version](https://img.shields.io/github/go-mod/go-version/shtmthw/ponnoKhoj)](go.mod)
[![Docker Hub](https://img.shields.io/docker/v/matthew552/ponnokhoj?label=docker%20hub)](https://hub.docker.com/r/matthew552/ponnokhoj)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

> ## 🤖 Fully Agent-Driven Project
>
> ponnoKhoj is planned, architected, implemented, tested, containerized and maintained by **AI coding agents**.
> The human owner, Matt ([@shtmthw](https://github.com/shtmthw)), provides product direction, reviews the work and holds the credentials.
> Every significant decision the agents made is recorded in [`docs/adr/`](docs/adr/), and their progress, evidence and known gaps in [`docs/PROGRESS.md`](docs/PROGRESS.md).

## What is ponnoKhoj?

- It is an application/website that lets you find the best price of a product (*ponno*) you want from the available online sites.

  But wait, that's not all: IT ALSO LETS YOU FIND THE BEST PRICES OF A PRODUCT THAT IS NOT SOLD ONLINE BUT OFFLINE AND LOCALLY. When
  you want to buy a product for the best price but the online sellers are showing inflated prices, what do you do now?

  You can just switch the filtering mode to offline local vendors and get the best prices of the product you want to buy, from shops
  within the area you chose!

  Local vendors DO NOT need to sell the product online for their prices to show up in the offline filtering. They just add
  their offline shop's name, address and products and list the prices, that's it. Users can then check their local offline shops,
  get the location of the vendor's store and the prices of the products, and then go to the store and pick up the item themselves!

  But wait, what if the vendors in your local area do not list their product prices?

  WORRY NOT, this is where the AI-powered data fetch based on local area context, product context and the user's request kicks in!

  For cases where the vendors are not listing their product prices, the AI will help you get an estimate of the product price based
  on the context of your local area and local stores. But be aware that the prices may not always be spot on: expect a lower or higher
  price than the evaluation.

## How it works

| Mode | Where the price comes from | What you get |
|---|---|---|
| **Local shops** | Prices that shop owners enter themselves (no online store needed) | Shops near you, cheapest first, with their address, map location and directions |
| **Online** | Public product pages of online shops (currently [Star Tech](https://www.startech.com.bd) and [Tech Land BD](https://www.techlandbd.com), electronics) | Each shop's current price, cheapest first, with a link to buy |
| **AI estimate** | Evidence the system retrieved: prices further away, online prices, similar products, past prices | A price **range** with a confidence level and the evidence behind it |

**Where you are, three ways:**
- **District only** — you see every shop in the whole district, and the page says so.
- **District + upazila** — you see only the shops registered in that upazila.
- **Exact location** (map pin or your device's location) — you see shops within a radius, which you can change (1, 3, 5, 10 or 25 km).

> **About the AI estimate.** An estimate is a range built only from evidence the system actually found; the real price may be
> higher or lower. The language model may only reason over that evidence, its answer is checked on the server (every cited piece
> of evidence must exist, the range must stay close to the evidence), and if it fails the check twice a statistical estimate is used
> instead. **When there is not enough data, you get no estimate rather than a guess.** See [`docs/estimation.md`](docs/estimation.md).

The interface is in Bangla and English, works on phones first, and keeps working without JavaScript.

## Architecture

```
          Gateway API (HTTPRoute, TLS via cert-manager)
                         │
                ┌────────▼────────┐        River job queue (in Postgres)        ┌──────────────────┐
  shoppers ───► │ ponnokhoj serve │ ─────────── enqueue jobs ─────────────────► │ ponnokhoj worker │ ──► online shops' product pages
  vendors       │  HTML + /api/v1 │                                              │ refresh, estimate│     (robots.txt-aware, rate-limited)
  admins        └────────┬────────┘                                              └────────┬─────────┘ ──► LLM endpoint (OpenAI-compatible)
                         │            PostgreSQL 17 + PostGIS + pg_trgm (CloudNativePG)   │
                         └──────────────────────────┬───────────────────────────────────┘
```

One Go binary, `ponnokhoj`, runs every workload (`serve`, `worker`, `migrate`, `seed`, `healthcheck`, `version`, `config check`,
`estimate eval`). PostgreSQL is the only stateful dependency. Details, query plans and load-test results:
[`docs/architecture.md`](docs/architecture.md). JSON API: [`docs/api.md`](docs/api.md).

**Language policy.** Everything is written in Go: the server, the worker, migrations tooling, CLIs, the deploy tool (`tools/devctl`),
tests, load tests and dev tooling. Pages are rendered by Go's `html/template`. The repository is **not** 100% Go: it vendors two
pinned third-party JavaScript libraries with no reasonable Go equivalent — [HTMX](https://htmx.org) 2.0.11 and
[Leaflet](https://leafletjs.com) 1.9.4 — plus one small hand-written file, `internal/web/static/app.js`, that starts Leaflet maps
([ADR 0008](docs/adr/0008-map-glue-script.md)). There is no Node build step and no SPA framework. The Makefile, Dockerfile,
SQL migrations and Kubernetes YAML are configuration, not application code.

## Quickstart

### Local Kubernetes (kind) — the full stack

Requires Docker, [kind](https://kind.sigs.k8s.io) and kubectl.

```sh
make kind-up      # kind cluster + CloudNativePG + cert-manager + Envoy Gateway, build, load, deploy (~10 min the first time)
open http://localhost:8088          # https://localhost:8443 with a self-signed certificate
make deploy-dev   # redeploy after code changes
make kind-down    # delete everything
```

Demo logins: `admin@ponnokhoj.test` and `vendor@ponnokhoj.test`; their random passwords are in
`deploy/k8s/overlays/dev/.env.dev` (git-ignored, created by `make kind-up`). Local shops are **fictional demo data** and are labelled
"Demo". To enable AI estimates, add `LLM_API_KEY=...` to that file (the dev overlay uses OpenRouter) and run `make deploy-dev`.
Stopping and starting: `docker stop ponnokhoj-control-plane` / `docker start ponnokhoj-control-plane` keeps all data.

### Docker Compose — without Kubernetes

```sh
make dev-up       # PostGIS + migrate + seed (demo data) + web + worker on http://localhost:8080
make dev-down     # stop (the database volume is kept)
```

Secrets and demo passwords are generated into `deploy/compose/.env` (git-ignored). This stack is offline: the online prices come
from the built-in fictional demo shop, and estimates are statistical. To try real sources or an LLM, set `SOURCES_ENABLED` and the
`LLM_*` variables in that file.

### Docker

```sh
docker run --rm -p 8080:8080 \
  -e DATABASE_URL='postgres://user:pass@db-host:5432/ponnokhoj' \
  -e APP_SECRET='at-least-32-random-characters-.............' \
  docker.io/matthew552/ponnokhoj:<tag> serve
```

The database needs the PostGIS and pg_trgm extensions. Run `migrate up` and `seed` (add `--demo` for demo data) once with the same
image before `serve`, and run `worker` alongside for online prices and estimates. The image runs as a non-root user and has no shell;
`ponnokhoj healthcheck` probes `/readyz` for Compose-style health checks.

### Configuration

All configuration is environment variables, validated at startup (`ponnokhoj config check` prints the effective values with
secrets redacted). This table is generated from `internal/config` and a test fails if they drift.

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

## For vendors

You don't need an online store. Sign up under **For shop owners**, add your shop (name, a short description, address, area, and a pin
on the map), then list your prices one by one or upload a CSV (`product_id,price,in_stock`; product IDs are in the downloadable product
list). New shops appear in search after an admin verifies them. Prices older than 7 days are marked "may be outdated" and are hidden
after 30 days, so keep them fresh. Unusual prices (more than 3× or under ⅓ of the local median) are accepted but reviewed by an admin.

## For contributors

- `make ci` runs every CI gate locally: lint, generated-code check, unit tests with the race detector, fuzzing, integration tests
  against PostGIS, coverage thresholds, govulncheck, manifest validation and policy tests, hadolint, image checks (size, Trivy),
  the estimator backtest, and the end-to-end tests on kind.
- `main` is protected: changes arrive through pull requests that pass every gate. Nothing reaches `main` or Docker Hub otherwise.
- Commits follow [Conventional Commits](https://www.conventionalcommits.org) (`feat:`, `fix:`, `ci:`, `docs:` …).
- Decisions go in an ADR under [`docs/adr/`](docs/adr/); progress and known gaps in [`docs/PROGRESS.md`](docs/PROGRESS.md).
- Security issues: see [`SECURITY.md`](SECURITY.md).

## Data and compliance

- **Online sources** are chosen after a review of each shop's terms of service and `robots.txt`; the decisions (approved, forbidden,
  pending review) are in [`docs/sources.md`](docs/sources.md). ponnoKhoj reads only pages the shop's `robots.txt` allows, identifies itself
  as `ponnoKhojBot` with a contact URL, waits five seconds between requests to a shop, and links every price back to the shop.
- **Areas** (divisions, districts, upazilas) come from [nuhil/bangladesh-geocode](https://github.com/nuhil/bangladesh-geocode) (MIT);
  see [`docs/data-sources.md`](docs/data-sources.md), including its gaps (no city-corporation thanas, no upazila coordinates).
- **Maps** use [OpenStreetMap](https://www.openstreetmap.org/copyright) tiles; map data © OpenStreetMap contributors, available under
  the Open Database License. The tile server is configurable (`MAP_TILE_URL`); heavy use should follow the
  [OSM tile usage policy](https://operations.osmfoundation.org/policies/tiles/) or use another provider.
- **Privacy.** Shopper locations are never stored or logged exactly — only a ~1 km area, for aggregate statistics and estimate caching.

## License

[MIT](LICENSE) © Matt ([@shtmthw](https://github.com/shtmthw)). Third-party components keep their own licenses: HTMX and Leaflet (`internal/web/static/vendor/*/LICENSE`), the area dataset (`internal/seed/data/LICENSE.bangladesh-geocode`), and map data © OpenStreetMap contributors (ODbL).
