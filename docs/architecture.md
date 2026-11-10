# Architecture

## Overview

```
          Gateway API (Envoy Gateway; TLS by cert-manager)
                         │
                ┌────────▼────────┐     River jobs (in Postgres)     ┌──────────────────┐
  shoppers ───► │ ponnokhoj serve │ ───────── enqueue ─────────────► │ ponnokhoj worker │ ──► shops' product pages
  vendors       │  HTML + /api/v1 │                                   │ refresh/estimate │     (robots.txt, 5 s/host)
  admins        └────────┬────────┘                                   └────────┬─────────┘ ──► LLM (OpenAI-compatible)
                         │      PostgreSQL 17 + PostGIS 3.6 + pg_trgm (CloudNativePG)   │
                         └───────────────────────────┬──────────────────────────────────┘
```

- **One binary**, `ponnokhoj`, runs as two Deployments from the same image: `serve` (2+ replicas in prod, HPA on CPU) and `worker`. A Job runs `migrate up` and `seed` before each rollout. See [ADR 0001](adr/0001-modular-monolith.md).
- **PostgreSQL is the only stateful dependency.** It holds the domain data, sessions, the audit log, cached online offers, cached estimates and the River job queue.
- **Searches never wait on the network.** Online offers are served from cache, and a refresh job is queued when they are older than `OFFER_CACHE_TTL` (stale-while-revalidate). Estimates are async jobs that the page polls.

## Packages (`internal/`)

| Package | Responsibility |
|---|---|
| `config` | Environment configuration, validation, the README config table |
| `domain` | Money (int64 poisha), quantities and units, modes, freshness, confidence |
| `search` | Query parsing and normalization (Bangla ↔ Latin transliteration, digits, sizes); fuzzed |
| `geo` | Points, radius clamping, directions URLs, privacy rounding |
| `store` + `store/db` | pgx pool, transactions, sqlc-generated queries |
| `migrations` | goose SQL migrations (embedded) and River's migrations, under an advisory lock |
| `pricefinder` | The search use case: product candidates, local listings per scope, online offers, estimate state |
| `sources` | Fetcher (robots, rate limit, retry, breaker), JSON-LD/microdata parsing, source catalog and compliance |
| `matching` | Title-to-product confidence (brand, model, size, variant conflicts) |
| `estimate` | Evidence gathering, baseline, LLM agent, validation, eval harness |
| `llm` | OpenAI-compatible chat client with tools; recorded fake for tests |
| `jobs` | River workers: `refresh_offers`, `estimate`, periodic `maintenance` |
| `vendor`, `moderation`, `auth` | Shops, listings, CSV upload, proposals; reports, anomaly flags, audit; sessions and Argon2id passwords |
| `web`, `api` | HTML pages (html/template, HTMX, Leaflet) and the JSON API |
| `httpx` | Router, middleware (security headers, CSP, cross-origin protection, request IDs, logging), rate limits, graceful server |
| `observability` | slog logging, Prometheus metrics, OpenTelemetry tracing (OTLP when configured) |
| `app` | Wiring: builds the runtime for `serve` and `worker` |

## Data model (main tables)

- **`areas`**: division → district → upazila, bilingual names, district centroids.
- **`shops`**: owner, area, PostGIS `geography(Point)` location, status (`pending`/`verified`/`suspended`), `auto_hidden`, `description`, `is_demo`.
- **`products`** and **`product_aliases`**: normalized `search_key`, unit, quantity, brand/model, category.
- **`listings`**: shop × product, price in poisha, stock, `updated_at`, anomaly flag. Superseded prices go to `listing_price_history`.
- **`online_sources`**, **`product_source_urls`**, **`offers`** and **`product_refreshes`**: the compliance catalog, curated product pages, cached offers and refresh bookkeeping.
- **`estimates`**: cached per product × cell/area × radius bucket, with stored evidence.
- **`reports`**, **`audit_log`**, **`users`**, **`sessions`**, **`search_stats`**, plus River's tables.

## Indexes and query plans

GiST index on `shops.location`; GIN `gin_trgm_ops` on `products.search_key` and `product_aliases.alias_key`; composite indexes matched to the queries (e.g. `listings_product_price_idx`, `offers_product_price_idx`). pg_trgm's operator functions get `COST 100` (migration 00003, [ADR 0013](adr/0013-trigram-operator-cost.md)); without it the planner prefers sequential scans.

The plans below are from `TestSeedAndProductSearch` (integration), run on PostGIS 17-3.6 with 20,030 products and 20,026 shops.

**Product search** (`search_key %> 'miniket rice 5000g'`, products and aliases):

```
Nested Loop  (cost=4578.84..4611.84 rows=4 width=8)
  ->  Unique
        ->  Merge Append
              Sort Key: p_1.id
              ->  Sort
                    ->  Bitmap Heap Scan on products p_1
                          Filter: ('miniket rice 5000g'::text <% search_key)
                          ->  Bitmap Index Scan on products_search_key_trgm
                                Index Cond: (search_key %> 'miniket rice 5000g'::text)
              ->  Sort
                    ->  Bitmap Heap Scan on product_aliases a
                          Filter: ('miniket rice 5000g'::text <% alias_key)
                          ->  Bitmap Index Scan on product_aliases_key_trgm
                                Index Cond: (alias_key %> 'miniket rice 5000g'::text)
  ->  Index Scan using products_pkey on products p
        Index Cond: (id = p_1.id)
        Filter: (status = 'active'::text)
```

The same query with pg_trgm's default `COST 1` uses `Seq Scan on products` and `Seq Scan on product_aliases` (total cost 1034 vs 4578 estimated; the planner's choice is wrong, the index plan is far faster in practice).

**Radius query** (verified shops within 3 km):

```
Bitmap Heap Scan on shops s  (cost=4.54..290.11 rows=2 width=8)
  Filter: ((NOT auto_hidden) AND (status = 'verified'::text) AND st_dwithin(location, '…'::geography, '3000'::double precision, true))
  ->  Bitmap Index Scan on shops_location_gist  (cost=0.00..4.54 rows=18 width=0)
        Index Cond: (location && _st_expand('…'::geography, '3000'::double precision))
```

Area scopes (upazila, district) filter on `shops.area_id` / `areas.parent_id` with keyset pagination on (price, id).

## Load and rolling restart

Measured on 2026-11-10 against the kind deployment on the owner's workstation: one web replica, one worker, CloudNativePG with 1 instance, and seeded demo data (30 products, 26 shops, 539 listings). Workload: `test/load` (vegeta), an even mix of point-radius search, district search (JSON API) and the district HTML results page. Any 5xx, timeout or 429 fails the run.

| Rate | Requests | Success | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| 50/s for 30 s (`make load`) | 1,500 | 100% | 2 ms | 4 ms | 5 ms | 89 ms |
| 200/s for 30 s | 6,000 | 100% | 2 ms | 5 ms | 57 ms | 709 ms |

Target: p95 < 300 ms with zero 5xx. **Met.**

Rolling restart (`TestRollingRestartWithoutErrors`, e2e): continuous requests during `kubectl rollout restart` of web gave 1,451 ok and 0 failed. Readiness fails first and the server drains for `SHUTDOWN_DRAIN_DELAY` before it stops accepting requests.

Two problems the load test found and fixed:
- **Search-stat hot row.** Every search originally upserted the same `search_stats` row, serializing concurrent searches on its row lock and a WAL flush each. At 50/s almost every request hit the 10 s timeout. Counts are now aggregated in memory and flushed every 30 s and on shutdown (`pricefinder.FlushStats`).
- **Rate limit on kind.** Every client of kind shares one Docker-bridge IP, so the per-IP search limit (60/min) throttled the tests and the browser together. The dev overlay raises it, and the home page is never rate-limited.

## Deployment

- Kustomize base plus overlays: `dev` (kind: real shops, OpenRouter LLM), `ci` (kind, offline) and `prod`.
- The phased deploy is scripted in `tools/devctl` (see the [runbook](runbook.md)).
- Pod hardening: non-root, read-only root filesystem, no capabilities, seccomp `RuntimeDefault`, resources and probes on every container. The policy test (`test/policy`) enforces this for every overlay.
- Default-deny NetworkPolicies. Allowed flows: gateway → web:8080, web/worker → Postgres, worker → egress 443, Prometheus → :9090.
