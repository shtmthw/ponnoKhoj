# ponnoKhoj — Build Brief for an Autonomous Coding Agent

You are the sole engineer on **ponnoKhoj**. You will design, build, test, containerize, and ship it from an almost empty repository. A human owner (Matt, GitHub `shtmthw`) gives product direction, holds credentials, and reviews your work. Everything else is your job.

Read this whole brief before you write any code. Where this brief and your own judgment disagree, follow the brief and log the disagreement in `docs/adr/` so the owner can decide. Where the brief says nothing, make the most conservative, boring, well-tested choice and record it.

---

## 0. Product context

**The initial idea of it is in the README.md file.** Read it first, then read it again. Everything below puts that idea into concrete terms. If this brief seems to contradict the README's intent, the README's intent wins and you flag the conflict.

The README describes three price sources, and the whole system is organized around them:

| Mode | Source of truth | What the user gets |
|---|---|---|
| **Online** | Prices fetched from online shops | Ranked list of online offers with links, cheapest first |
| **Offline / local** | Prices that local vendors enter themselves (shop name, address, location, product, price). No online store needed | Nearby shops within the user's chosen area/radius, their prices, and their location so the user can go there |
| **AI estimate** | Derived from evidence when no local vendor lists the product | A price **range** with a confidence level and the evidence behind it, always labeled as an estimate that may be higher or lower than reality |

Target market: Bangladesh (the owner is in Chattogram). The currency is BDT (৳). The UI is bilingual, Bangla and English. Product names, prices, and addresses will mix Bangla script, Bangla digits (০–৯), and English.

---

## 1. Hard constraints (non-negotiable)

1. **Go everywhere you can.** All backend services, workers, CLIs, migrations tooling, test harnesses, load tests, and dev tooling are written in Go. The frontend is server-rendered by Go (`html/template`) with HTMX. The only JavaScript allowed is vendored, pinned third-party libraries that have no reasonable Go equivalent: HTMX and a map library (Leaflet). No Node build step and no SPA framework. Say this precisely in the README; do not claim "100% Go" if vendored JS exists.
2. **One server image.** A single statically linked binary, `ponnokhoj`, with subcommands (`serve`, `worker`, `migrate`, `seed`, `healthcheck`, `version`). The same image runs every Kubernetes workload.
3. **Kubernetes is the deployment target.** The repo contains production-grade manifests (Kustomize base + overlays) and a local `kind` workflow that runs the full stack.
4. **The CI pipeline is the gatekeeper.** Nothing reaches `main` or Docker Hub without passing every gate in §11.
5. **The image is pushed to the owner's Docker Hub repository** as `docker.io/${DOCKERHUB_USERNAME}/ponnokhoj` (Docker Hub repository names must be lowercase). The username comes from CI configuration, never hardcoded.
6. **The README states that this is a fully agent-driven project** (details in §13).
7. **No secrets in git, ever.** That covers `.env` files, kubeconfigs, tokens, and test credentials that look real.
8. **Money is never a float.** Store and compute prices as `int64` **poisha** (1 BDT = 100 poisha). Format only at the edges.

---

## 2. How you operate

### 2.1 Working rules
- **Walking skeleton first.** Milestone M0 proves the entire path (code → CI → image on Docker Hub → runs on kind) before any feature work. Feature milestones then extend a pipeline that already works.
- **Small, verifiable increments.** Use Conventional Commits (`feat:`, `fix:`, `ci:`, `docs:`, …). Each commit builds, and `make ci` passes at the end of every milestone.
- **Keep a log.** Maintain `docs/PROGRESS.md` (milestone status, what's done, what's next, known gaps) and `docs/adr/NNNN-title.md` (one ADR per significant decision: context, options, decision, consequences).
- **Look up current versions; don't trust memory.** Before pinning Go, any library, GitHub Action, base image, or Kubernetes component, check its current stable release. Pin exact versions. Pin GitHub Actions by full commit SHA with the version in a trailing comment. Pin base images by digest.

### 2.2 Honesty rules (these matter more than speed)
- Never say a test, lint, build, or deployment passed unless you ran it and saw it pass in this session. Quote the command and the relevant output.
- Never weaken a gate to get to green. That means no lowered coverage thresholds, deleted or `t.Skip`'d tests, disabled linters, `//nolint` without a written reason on the same line, `continue-on-error`, or `|| true` in CI. If a gate seems wrong, write an ADR and ask.
- No fake completeness. At the end of a milestone, no production code path may contain `TODO: implement`, `panic("not implemented")`, or a stub returning hardcoded data. Unfinished work goes in `docs/PROGRESS.md` under "Known gaps".
- Never invent external facts: CSS selectors for a site you haven't inspected, API endpoints, coordinates, area boundaries, dataset contents, or a site's terms of service. If you can't verify something, say so, use a fixture, and record the gap.

### 2.3 Stop and ask the owner before:
- Choosing the Docker Hub username/repo, or creating any GitHub secret or variable
- Picking an LLM provider that costs money, or sending any real data to a third-party API
- Adding any online price source whose terms of service or `robots.txt` are unclear or forbid automated access
- Any destructive git operation (force push, history rewrite, branch deletion), or deploying to a real (non-kind) cluster
- Changing anything in §1

When you're blocked on the owner, keep working on everything that doesn't depend on the answer.

---

## 3. Domain model

Use these names in code, the schema, and docs.

- **Area**: an administrative place in Bangladesh (division → district → upazila/thana, plus optional city wards/neighborhoods). It has a name in both languages, a parent, an optional centroid, and optional metadata (e.g. urban/rural). Seed it from a **cited open dataset** with its license recorded in `docs/data-sources.md`. If you can't obtain or verify one, ship a minimal seed clearly marked as approximate, and don't make up coordinates.
- **Product**: a canonical product (e.g. "Miniket rice, 5 kg", "Walton refrigerator WFC-3F5-GDEL-XX"). It has a category, brand, model, normalized unit and quantity, names in both languages, and aliases (including transliterations such as "চাল" / "chal").
- **Category**: hierarchical. Used for comparables in estimation.
- **Vendor**: a user account that owns one or more Shops.
- **Shop**: a physical store with name, address text, Area, a geographic point (`geography(Point, 4326)`), opening hours (optional), phone (optional), and verification status (`pending`, `verified`, `suspended`).
- **Listing**: a Shop's price for a Product. Fields: price (poisha), unit, in-stock flag, `updated_at`. A listing has a freshness state computed from `updated_at`, with configurable thresholds (default: fresh ≤ 7 days, stale ≤ 30 days, hidden after that).
- **OnlineSource**: a configured online shop integration with its own feature flag, rate limit, compliance status, and health.
- **Offer**: one observed online price for a Product from an OnlineSource. Fields: URL, price, in-stock, `observed_at`, and match confidence.
- **Estimate**: an AI/statistical price estimate for (Product, location, radius). Fields: low/point/high (poisha), confidence (`low`/`medium`/`high`), evidence references, method (`statistical` | `llm_assisted`), model id, `created_at`, and expiry.
- **Report**: a user's flag on a Listing or Shop (wrong price, closed, fake).

---

## 4. Architecture

```
            ┌──────────── Gateway API (HTTPRoute, TLS via cert-manager) ───────────┐
            │                                                                      │
      ┌─────▼──────┐   Postgres (CloudNativePG, PostGIS, pg_trgm)   ┌──────────────┐
      │ ponnokhoj  │◄──────────────────────────────────────────────►│  ponnokhoj   │
      │  serve     │        River job queue (in Postgres)           │   worker     │
      │ (web + API)│───────────── enqueue jobs ────────────────────►│ (fetch,match,│
      └────────────┘                                                │  estimate)   │
            ▲                                                       └──────┬───────┘
            │ /metrics (separate port, cluster-internal only)              │
                                                         online shops ◄────┤ (rate-limited,
                                                         LLM endpoint ◄────┘  robots-aware)
```

- **Modular monolith, one binary.** `serve` handles HTTP (HTML pages and a JSON API under `/api/v1`). `worker` runs background jobs. Both share the internal packages. Don't split into microservices; record that decision in an ADR.
- **PostgreSQL is the only stateful dependency.** Extensions: PostGIS for radius queries, `pg_trgm` for fuzzy multilingual search. Jobs use **River** (`riverqueue/river`, Postgres-backed). There's no Redis and no message broker. If you think one is needed, write an ADR and ask first.
- **Data access:** `pgx` v5 + `sqlc` (generated code committed; CI checks it's up to date). Geography columns need sqlc type overrides. Configure them deliberately, and put `ST_MakePoint(lng, lat)` argument order (longitude first) in a single helper with a test.
- **Migrations:** embedded in the binary (`go:embed`) with a maintained migration library (goose or golang-migrate; your choice, recorded in an ADR). Every migration has a down migration, and migrations follow expand/contract so the previous release keeps running against the new schema during a rollout.
- **HTTP:** standard library `net/http` with method/path patterns. Middleware covers request ID, structured logging, panic recovery, timeouts, body size limits, security headers, rate limiting, and cross-origin protection for state-changing requests (use `net/http`'s built-in `CrossOriginProtection` if the pinned Go version has it, otherwise a vetted CSRF library). Set `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, and `MaxHeaderBytes`.
- **Logging:** `log/slog`, JSON in cluster, text locally. Never log secrets, session tokens, passwords, or precise user coordinates.
- **Metrics/tracing:** Prometheus (`client_golang`) on a separate port that isn't routed through the Gateway. OpenTelemetry tracing with an OTLP exporter, off by default and enabled by env var.
- **Config:** environment variables only. Parse and validate everything at startup and fail fast with a clear message. `ponnokhoj config check` prints the effective config with secrets redacted.

### 4.1 Graceful shutdown (get this exactly right)
On SIGTERM: (1) readiness starts failing immediately; (2) wait a configurable drain delay (default 5 s) so endpoints are removed (do this in-process, because the distroless image has no `sleep`); (3) `http.Server.Shutdown` with a deadline; (4) the worker stops fetching new jobs, finishes or releases in-flight ones, and River's graceful stop completes; (5) close the DB pool. `terminationGracePeriodSeconds` must be longer than drain + shutdown deadline. Test this behavior.

### 4.2 Health endpoints
- `/livez`: process is alive. **Do not check the database**, so a DB outage doesn't cause restart storms.
- `/readyz`: DB ping succeeds (short timeout), migrations are at the expected version, and the process isn't shutting down.

---

## 5. Repository layout

```
cmd/ponnokhoj/            main + subcommands
internal/config/          env parsing & validation
internal/httpx/           server, middleware, errors, rendering
internal/web/             page handlers, templates (embedded), i18n catalogs, static (embedded, vendored JS pinned)
internal/api/             JSON API handlers (/api/v1)
internal/domain/          core types (Money, Product, Shop, …) — no I/O
internal/store/           sqlc queries, generated code, repositories, tx helpers
internal/migrations/      SQL migrations (embedded)
internal/search/          normalization, query parsing, ranking
internal/geo/             points, radius, geohash bucketing
internal/sources/         OnlineSource interface, fetch client, robots, rate limits, per-source adapters, JSON-LD parser
internal/matching/        offer→product matching
internal/estimate/        evidence gathering, statistical baseline, LLM agent, validation
internal/llm/             OpenAI-compatible client, fake client for tests
internal/auth/            vendors/admins, sessions, password hashing
internal/jobs/            River job definitions & schedules
internal/moderation/      reports, anomaly flags
internal/observability/   logging, metrics, tracing setup
deploy/k8s/base/          Kustomize base
deploy/k8s/overlays/{dev,prod}/
deploy/kind/              kind cluster config
test/integration/  test/e2e/  test/load/  test/fixtures/
tools/                    Go dev tools (via go.mod `tool` directives where supported)
docs/                     PROGRESS.md, adr/, architecture.md, api.md, sources.md, data-sources.md, runbook.md, estimation.md
.github/workflows/        ci.yml (+ release logic, see §11)
Dockerfile  Makefile  .golangci.yml  .dockerignore  .env.example  README.md
```

---

## 6. Backend features in detail

### 6.1 Search (shared by all three modes)
- Input: free text (Bangla, English, or mixed), optional category, mode (`online` | `local` | `estimate` | `all`), location (point from map or browser geolocation, or an Area), and radius (default 3 km, max configurable, e.g. 25 km).
- **Normalization pipeline** (pure, heavily tested and fuzzed): Unicode NFKC, case folding, Bangla digits → ASCII digits, whitespace/punctuation cleanup, unit normalization (`1kg` = `1000g` = `১ কেজি`; `ltr`/`L`/`লিটার`), and alias/transliteration expansion from the product alias table. Postgres has no Bangla stemmer, so don't pretend to stem. Use trigram similarity on normalized text plus aliases.
- **Local mode query:** `ST_DWithin(shop.location, $point, $radius_m)` on `geography` with a GiST index, joined to fresh/stale listings of verified shops only. Rank by price ascending, with distance as tiebreaker. Return distance in meters, freshness state, and a directions link.
- **Online mode:** return cached offers immediately and enqueue a refresh job if the cache is stale (stale-while-revalidate). The UI shows each offer's `observed_at` time.
- **Estimate mode:** triggered explicitly, or automatically suggested when local mode returns no listings (§6.4).
- Pagination is cursor-based. Every response includes enough metadata (counts per mode, freshness, which sources failed) for the UI to be honest about gaps.

### 6.2 Offline vendors
- Vendor sign-up and login (email + password; argon2id with documented parameters; login rate limiting and lockout backoff). Sessions are opaque random tokens stored hashed in Postgres; the cookie is `HttpOnly`, `Secure`, `SameSite=Lax`. Admin is a role, not a separate system.
- Shop creation: name, address text, Area selection, and **a pin dropped on a map** (this avoids depending on a geocoder). If you add geocoding, use OSM Nominatim only within its usage policy (≤ 1 req/s, identifying User-Agent, cached results) behind an interface. Map tiles need correct OSM attribution and must respect the tile usage policy; make the tile URL configurable.
- New shops start `pending` and show in search only once `verified` by an admin (config flag to auto-verify in dev).
- Listings: pick a canonical product (search-as-you-type) or propose a new product, which goes into an admin review queue. Support bulk update (CSV upload parsed in Go, with per-row validation errors shown back) since vendors have many items.
- **Price anomaly guard:** a listing more than 3× or less than ⅓ of the local median for that product (when ≥ 5 comparables exist) is accepted but flagged for review and not used as estimation evidence until reviewed.
- Users can report a listing or shop. Reports past a threshold auto-hide pending review.
- Every vendor/admin mutation is written to an audit log table.

### 6.3 Online sources
- Interface (sketch, refine as needed):
  ```go
  type Source interface {
      ID() string
      Search(ctx context.Context, q NormalizedQuery) ([]RawOffer, error)
  }
  ```
- **Compliance comes first.** Each source has an entry in `docs/sources.md`: owner/site, access method (official API, affiliate feed, sitemap + structured data, or HTML), ToS review notes, `robots.txt` status, and an enabled/disabled decision. Prefer official APIs and feeds. Don't enable a source whose ToS forbids automated access. Any source that's unclear needs owner approval (§2.3). All sources start disabled behind feature flags.
- **Shared fetch client:** obeys `robots.txt` (cached), sends an identifying User-Agent with a contact URL, uses a per-host token-bucket rate limiter (`golang.org/x/time/rate`), and has timeouts on connect/headers/body. Retries use jittered exponential backoff on idempotent GETs, only for network errors, 429, and 5xx, and honor `Retry-After`. It has a per-source circuit breaker, a response size cap, and no cookies or session reuse across sources.
- **Generic structured-data adapter:** parse schema.org `Product`/`Offer` JSON-LD (and microdata if cheap). Many shops expose it, and it's far more robust than CSS selectors. Site-specific HTML adapters are only a fallback.
- **Price parsing** (fuzzed): `৳ ১,২৫০`, `Tk 1,250.00`, `BDT 1250`, `1,250/-`, ranges, struck-through "was" prices vs current price, and missing price. Return poisha or a typed error, never a guess.
- **Tests run against saved HTML/JSON fixtures in `test/fixtures/`, never the live network in CI.** A separate, manually triggered `make sources-smoke` may hit live enabled sources.
- Per-source metrics: requests, latency, errors by class, parse failures, offers found.

### 6.4 AI price estimation (the part most likely to be wrong, so build it carefully)

Guiding rule: **the LLM only reasons over evidence the system retrieved; it never invents prices from its own knowledge.** If there's no evidence, there's no estimate.

**Pipeline**
1. **Evidence gathering** (deterministic): (a) listings for the same product in expanding radii (e.g. 3 → 10 → 25 km), (b) the same product in other Areas with similar metadata, (c) recent online offers for the product, (d) local listings of comparable products in the same category with similar unit/size, (e) historical listing/offer prices with timestamps. Every evidence item has a stable ID, value, distance, age, and source type.
2. **Sufficiency check:** below a configurable threshold (e.g. fewer than 3 independent evidence items, or only stale ones), return `insufficient_data` with the online prices that do exist, and don't estimate.
3. **Statistical baseline** (always computed, fully deterministic, unit-tested): weighted median with distance decay and recency decay; an online→offline adjustment ratio learned per category/area from products that have both kinds of data, falling back to the global ratio and finally to none; range = weighted interquartile spread, widened when evidence is thin. Confidence comes from evidence count, agreement, and freshness.
4. **LLM-assisted refinement** (optional, config-gated): a **bounded tool-calling agent loop** over *internal, read-only* tools only, e.g. `get_local_listings`, `get_online_offers`, `get_comparables`, `get_area_info`. Hard limits: max steps (e.g. 6), total timeout (e.g. 20 s), max tokens. The final answer is structured JSON validated against a JSON Schema: `{low, point, high, confidence, evidence_ids[], explanation_bn, explanation_en}`.
5. **Server-side validation of the LLM output:** schema-valid; `low ≤ point ≤ high`; every cited evidence ID exists and was actually retrieved in this run; and the estimate stays within a configurable envelope around the evidence (e.g. [0.5 × min, 1.5 × max]). On failure, retry once with the validation error fed back. If it fails again, fall back to the baseline and record `method=statistical` with the reason.
6. **Response:** always labeled as an estimate in both languages, with the README's caveat that real prices may be higher or lower. Include the range, confidence, method, evidence summary, and timestamp. Never show only a single point value.

**Mechanics**
- LLM client: an **OpenAI-compatible Chat Completions** interface with tool calling, configured by `LLM_BASE_URL`, `LLM_MODEL`, `LLM_API_KEY`. That one client works with local Ollama (`/v1`) and hosted providers. Ship a deterministic **fake client** with recorded responses for tests.
- Estimates run as River jobs. The UI polls via HTMX. Use unique jobs to deduplicate identical in-flight requests. Results are cached per (product, geohash cell, radius bucket) with a TTL.
- Rate-limit estimate requests per client IP. Record token usage and latency metrics.
- **Evaluation harness** (`ponnokhoj estimate eval` or `test/eval`): backtest by hiding known listing prices and estimating them. Report MAPE, median absolute error, and **range coverage** (the share of true prices inside [low, high]) separately for the baseline and the LLM-assisted path. The baseline eval runs in CI on a fixture dataset with committed thresholds. The LLM eval runs manually and its results are written to `docs/estimation.md`.

---

## 7. Data layer
- Use `CHECK` constraints for invariants (price > 0, valid enum states). Foreign keys everywhere. Columns are `timestamptz`.
- Indexes: GiST on `shops.location`; GIN `gin_trgm_ops` on normalized product names and aliases; composite indexes matched to the real queries. Check them with `EXPLAIN` on seeded data and record the plans in `docs/architecture.md`.
- A `seed` subcommand loads Areas, categories, a realistic demo catalog, and demo shops/listings around a few Chattogram and Dhaka locations. Demo data is clearly marked as demo and is never loaded in the prod overlay.
- Store user search locations only in rounded form (e.g. a geohash precision of ~1 km) and only for aggregate metrics. Don't persist exact user coordinates.

---

## 8. Frontend (Go-rendered)
- Pages: home/search, results (tabs or a toggle for Online / Local / Estimate, matching the README's filtering-mode switch), shop detail (map, address, listings, directions link), vendor dashboard (shops, listings, bulk upload), admin (verification queue, reports, product proposals, anomaly flags).
- A Bangla/English language toggle with all strings in message catalogs, and number/currency formatting per locale.
- Works without JavaScript for core search; HTMX enhances it. It's accessible (labels, contrast, keyboard navigation) and mobile-first, because most users will be on phones.
- Static assets are embedded with content-hashed filenames and long cache headers. CSP header that allows only self-hosted scripts.

---

## 9. Container image
- Multi-stage `Dockerfile`:
  - Builder: `FROM --platform=$BUILDPLATFORM golang:<pinned>@sha256:…`. Cross-compile using `TARGETOS`/`TARGETARCH` (fast multi-arch with no QEMU emulation for the compile step). `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w -X …version/commit/date"`. Use BuildKit cache mounts for the module and build caches.
  - Final: current **distroless static, `nonroot` variant, pinned by digest**. Embed timezone data (`import _ "time/tzdata"`) and rely on the base image's CA certificates. No shell.
  - Runs as a non-root UID/GID, `ENTRYPOINT ["/ponnokhoj"]`, OCI labels (source, revision, version, created, licenses).
- `.dockerignore` keeps out `.git`, docs build output, local env files, and test fixtures that aren't needed.
- Platforms: `linux/amd64` and `linux/arm64`.
- `ponnokhoj healthcheck` exists for docker-compose/local use, since there's no curl in the image.

---

## 10. Kubernetes
Kustomize base + `dev` and `prod` overlays. Validate everything with `kubeconform -strict`, including CRD schemas for Gateway API and CloudNativePG.

- **Namespace** `ponnokhoj` with Pod Security Admission label `enforce: restricted`.
- **Deployment `ponnokhoj-web`** (`serve`): ≥ 2 replicas in prod, rolling update `maxUnavailable: 0`, readiness/liveness/startup probes as defined in §4.2, resource requests and limits, `topologySpreadConstraints` across nodes, and a `securityContext` with `runAsNonRoot`, `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false`, dropping all capabilities, and `seccompProfile: RuntimeDefault`. Writable `emptyDir` only where needed.
- **Deployment `ponnokhoj-worker`** (`worker`): same hardening; separate scaling.
- **HPA** for web (CPU-based; document that it requires metrics-server). **PodDisruptionBudget** for web and worker.
- **Migrations as a Job** named with the image's short SHA (Job templates are immutable). The deploy procedure is: apply the Job → `kubectl wait --for=condition=complete` → then roll out the Deployments. Script it in `make deploy-*`. Don't run migrations on app startup. As a safety net, the migrator takes a Postgres advisory lock.
- **Scheduled refreshes** happen through River periodic jobs inside the worker, so there are no extra CronJobs to keep in sync. If you choose CronJobs instead, justify it in an ADR.
- **Postgres via CloudNativePG** (`Cluster` resource using a PostGIS-enabled image): 1 instance in dev, 3 in prod, with a documented backup configuration to object storage (prod overlay, credentials via Secret).
- **Ingress via Gateway API (`Gateway` + `HTTPRoute`), not the Ingress API.** The community ingress-nginx controller was retired in March 2026; don't use it. Pick a maintained Gateway API implementation (e.g. Envoy Gateway, Traefik, or Cilium), record the choice in an ADR, and keep manifests implementation-neutral where possible. TLS via cert-manager.
- **NetworkPolicies:** default deny; allow Gateway → web, web/worker → Postgres, worker → egress 443 (sources, LLM), and the Prometheus scrape to the metrics port.
- **Config and Secrets:** ConfigMap for non-secret config. Secrets are never committed. Dev uses a Kustomize `secretGenerator` from a git-ignored `.env.dev`; prod documents an External Secrets Operator or Sealed Secrets path (ADR). The ServiceAccount uses `automountServiceAccountToken: false`.
- **Local cluster:** `make kind-up` creates a kind cluster, installs CNPG, the Gateway API CRDs plus the chosen controller, and cert-manager (self-signed issuer), builds the image, loads it into kind, and deploys the dev overlay. `make kind-down` tears it all down. Pin all of these versions in one place.
- `docs/runbook.md` covers deploy, rollback (`kubectl rollout undo` plus the expand/contract migration policy), DB restore, rotating secrets, disabling a misbehaving source, and turning off LLM estimation.

---

## 11. CI/CD (GitHub Actions)

### 11.1 Principles
- One workflow file (`ci.yml`) for PRs and pushes. The publish job runs only on `push` to `main` or `v*` tags and `needs:` every gate job. This avoids `workflow_run` complexity and guarantees nothing unverified is published.
- Set `permissions: contents: read` at the top level and grant extra permissions per job only where needed. Every Action is pinned by full commit SHA. Dependabot or Renovate updates Go modules, Actions, and Docker base images.
- Concurrency: cancel in-progress runs for the same PR. Never cancel `main` or tag runs.
- Fork PRs get no secrets. Gate jobs must not need secrets.
- `setup-go` reads `go-version-file: go.mod` with module/build caching. Buildx uses the GitHub Actions cache.

### 11.2 Gate jobs (all required by branch protection on `main`)
1. **lint**: `gofmt -l` must be empty, `go vet ./...`, golangci-lint (pinned version, committed `.golangci.yml` with a deliberate linter set including `staticcheck`, `errcheck`, `gosec`, `bodyclose`, `sqlclosecheck`, `contextcheck`, `errorlint`), and `go mod tidy` + `go mod verify` with no diff.
2. **generate-check**: `sqlc generate` (and any other codegen), then `git diff --exit-code`.
3. **unit**: `go test -race -shuffle=on -count=1 ./...` with coverage. Committed thresholds: overall ≥ 70%; `domain`, `search` normalization, `sources` price parsing, `estimate` baseline/validation ≥ 85%. Run each fuzz target for a short fixed time (e.g. 30 s) on top of its seed corpus.
4. **integration**: `testcontainers-go` with a PostGIS image. Runs migrations up → down → up, then repository, geo, search, River job, and auth flow tests against a real database.
5. **vuln**: `govulncheck ./...` fails on reachable vulnerabilities.
6. **manifests**: `kustomize build` for each overlay piped into `kubeconform -strict` with CRD schemas; plus a policy check that every container has resources, probes, and the hardened `securityContext` (a small Go test that parses the rendered YAML counts).
7. **dockerfile**: hadolint.
8. **image**: build both platforms with no push; Trivy scan that fails on fixable HIGH/CRITICAL findings; check the image runs `ponnokhoj version` and stays under an image-size budget (e.g. 40 MB).
9. **e2e**: create a kind cluster → load the image → `make deploy-dev` → wait for rollout → run `test/e2e` (Go): health endpoints, seeded local search within radius, vendor sign-up → shop → admin verify → listing visible, estimate job with the fake LLM producing a validated result, graceful shutdown during load with no failed requests. Upload logs and `kubectl describe` output as artifacts on failure.
10. **estimate-eval (baseline)**: fixture backtest must meet the committed MAPE and range-coverage thresholds.

### 11.3 Publish job (`push` to `main` or `v*` tag only)
- `needs:` all gate jobs. `permissions: contents: read, id-token: write` (for keyless signing).
- Log in to Docker Hub with `DOCKERHUB_USERNAME` (repository variable or secret) and `DOCKERHUB_TOKEN` (a Docker Hub **personal access token** with read/write scope, never the account password).
- `docker/metadata-action` tags:
  - every `main` push: `sha-<short>` and `main`
  - tag `vX.Y.Z`: `X.Y.Z`, `X.Y`, `X`, and `latest`. `latest` is **only** set from semver tags.
- `docker/build-push-action`: `linux/amd64,linux/arm64`, provenance attestation and SBOM enabled, OCI labels from metadata.
- Sign the pushed **digest** with cosign keyless (GitHub OIDC), then verify the signature in the same job.
- Post-push smoke: pull by digest, run `version`, and assert the commit SHA matches.
- Write the digest and tags to the job summary.
- Optional, behind a repository variable: deploy to a real cluster using a `KUBECONFIG` secret and a GitHub Environment with required reviewers. Off by default (§2.3).

### 11.4 Local parity
`make ci` runs the same checks locally (except multi-arch push and signing), so "it passes locally" means the same thing as in CI. Makefile targets: `tools`, `generate`, `fmt`, `lint`, `test`, `test-integration`, `fuzz`, `build`, `image`, `kind-up`, `kind-down`, `deploy-dev`, `e2e`, `load`, `eval`, `sources-smoke`, `ci`.

---

## 12. Testing, performance, and security

**Testing pyramid**
- Unit: table-driven tests for normalization, units, money, price parsing, ranking, estimation math, validation, and envelope clamping.
- Fuzz: query normalization, price parsing, JSON-LD parsing, CSV bulk upload parsing. None of them may panic.
- Golden files: rendered templates for key pages in both languages; source fixtures.
- Integration and e2e as listed in §11.2.
- Load: `test/load` using a Go load generator (e.g. vegeta as a library) against the kind deployment. Targets measured on kind and recorded in `docs/architecture.md`: p95 < 300 ms for local search with seeded data, zero 5xx at the target rate, and no errors during a rolling restart. Treat these as measured targets, not guarantees.

**Security**
- Input validation at every boundary. Parameterized SQL only (sqlc). Templates rely on `html/template` contextual escaping, with no `template.HTML` from user input.
- Headers: CSP, `X-Content-Type-Options`, `Referrer-Policy`, `Frame-Options`/`frame-ancestors`, HSTS in prod.
- Rate limits on search, estimate, login, sign-up, and report endpoints (per IP, honoring a configured trusted-proxy list for `X-Forwarded-For` and nothing else).
- Upload limits on CSV size and row count. Request body limits everywhere.
- Container and cluster hardening per §9–§10. `gosec`, `govulncheck`, and Trivy in CI.
- Prompt-injection hygiene for the LLM: shop names, product names, and listing text are untrusted data. Pass them to the model as clearly delimited data fields, never as instructions. The model has read-only internal tools only, with no network or write access, and its output is validated as in §6.4.
- `SECURITY.md` with a reporting contact (ask the owner which one).

---

## 13. README requirements

Rewrite `README.md` and keep the original idea section. Preserve its meaning and the owner's voice, and fix only spelling and grammar. Then add:

1. **A prominent "🤖 Fully Agent-Driven Project" section near the top**, stating clearly that ponnoKhoj is planned, architected, implemented, tested, containerized, and maintained by AI coding agents, with the human owner (Matt, [@shtmthw](https://github.com/shtmthw)) providing product direction, review, and credentials. Link to `docs/adr/` and `docs/PROGRESS.md` as the record of the agents' decisions and progress.
2. Badges: CI status, latest Docker Hub image, Go version, and license once chosen (ask the owner for the license).
3. "How it works": the three modes, with the AI-estimate disclaimer (estimates are ranges built from local evidence; real prices may be higher or lower; when there's not enough data you'll get no estimate rather than a guess).
4. Architecture overview (a short diagram plus a link to `docs/architecture.md`), and an exact statement of the language policy from §1.1.
5. Quickstart: `make kind-up` → open the app; `docker run` from Docker Hub with the required env vars; configuration reference (a table of every env var with defaults, generated from or checked against the config package so it can't drift; add a test for that).
6. For vendors: how listing works. For contributors: `make ci`, branch rules, and Conventional Commits.
7. Data and compliance: how online sources are chosen (link `docs/sources.md`), plus map/OSM attribution.

---

## 14. Milestones and acceptance criteria

Each milestone ends with a green `make ci`, an updated `docs/PROGRESS.md`, and a short summary to the owner: what was done, the commands you ran with their results, and known gaps.

**M0 — Walking skeleton**
- `go.mod`, the `ponnokhoj` binary with `serve` (`/livez`, `/readyz`, a placeholder home page), `version`, and graceful shutdown.
- Dockerfile, Makefile, the full `ci.yml` with every gate (gates with no code yet run on what exists, and none are skipped), and the publish job.
- Minimal Kustomize base/dev overlay, `make kind-up`, and an e2e test hitting `/readyz` on kind.
- ✅ Accepted when: a PR shows all gates green; a merge to `main` pushes a signed multi-arch `sha-…` image to `docker.io/${DOCKERHUB_USERNAME}/ponnokhoj`; and pulling that digest and running `version` prints the right commit. (Ask the owner for the Docker Hub variable/secret at the start of M0.)

**M1 — Data foundation**
- Postgres via CNPG on kind; migrations (up/down tested); sqlc; schema for §3; Areas, categories, and demo seed; normalization library with fuzzing.
- ✅ `migrate up/down/up` passes in integration; seeded DB answers a trigram product query and a radius query, with `EXPLAIN` showing index use.

**M2 — Offline vendors and local search**
- Vendor/admin auth, shops with map pin, verification, listings, CSV bulk upload, freshness, anomaly flags, reports, audit log, `/api/v1` search in local mode.
- ✅ e2e: vendor sign-up → shop → admin verify → listing → anonymous user finds it within radius and not outside it; stale and hidden thresholds behave correctly.

**M3 — Online sources**
- Fetch client (robots, rate limit, retry, breaker), JSON-LD adapter, price parser, matching, River refresh jobs, stale-while-revalidate, `docs/sources.md`.
- ✅ Fixture-based tests for every adapter; no live network in CI; any live source is enabled only with owner approval.

**M4 — AI estimation**
- Evidence gathering, sufficiency check, statistical baseline, LLM agent loop with internal tools, validation and fallback, caching, async jobs, eval harness, `docs/estimation.md`.
- ✅ Baseline eval meets committed thresholds in CI; validation rejects hallucinated evidence IDs and out-of-envelope values (tests prove it); `insufficient_data` returned when evidence is thin; a manual run against a local Ollama model is documented with results.

**M5 — Web UI**
- All pages from §8, bilingual, HTMX polling for estimates, map views, accessibility pass.
- ✅ Golden template tests in both languages; core search works with JS disabled; e2e drives the main flows through the HTML endpoints.

**M6 — Production hardening**
- Prod overlay (HPA, PDB, NetworkPolicies, Gateway API + cert-manager, CNPG 3 instances with backups), observability (metrics, dashboards as JSON in repo, OTel), load tests, rolling restart with zero errors, runbook.
- ✅ Load and rolling-restart results recorded; kubeconform and policy tests pass for both overlays.

**M7 — Release v0.1.0**
- README per §13, SECURITY.md, a `v0.1.0` tag → `0.1.0`/`0.1`/`0`/`latest` on Docker Hub, release notes generated from Conventional Commits.
- ✅ A fresh clone can run `make kind-up` and get a working app with demo data; the published `0.1.0` image verifies with cosign.

---

## 15. Explicit non-goals for v1
Payments, checkout, delivery, user (shopper) accounts, native mobile apps, recommendation feeds, vendor subscription billing, and scraping any site without a compliance decision recorded in `docs/sources.md`.

---

## 16. Final report format (for every milestone hand-off)
1. **Done**: bullet list mapped to that milestone's acceptance criteria.
2. **Evidence**: commands you ran and the key output (test counts, coverage, image digest, CI run link).
3. **Decisions**: new ADRs, one line each.
4. **Known gaps and risks**: honest and specific.
5. **Needs from owner**: credentials, approvals, answers.
6. **Next**: the first three tasks of the next milestone.

Start with M0. Before writing code, reply with: (a) your understanding of the product in five sentences, (b) the versions you intend to pin and where you checked them, (c) the questions from §2.3 you need answered now.
