# Progress

Last updated: 2026-11-10. The milestones are from [agentPlan.md](../agentPlan.md) §14. Decisions are recorded in [adr/](adr/README.md).

Work is pushed to branch `development`. A hand-built multi-arch image (amd64, arm64; SBOM and provenance) is on Docker Hub: `docker.io/matthew552/ponnokhoj:sha-c6ed479` / `:development`, digest `sha256:58dcb89a5f130438f342d94bf5012fe800115a3127f34547d366c5c9963b1bf7`. It is not cosign-signed; signed images come from CI once the variable and secret below are set. The GitHub Actions pipeline runs on PRs and on `main`; until a PR is opened, its gates have only been run locally through the same Makefile targets.

## Milestones

| Milestone | Status | Notes |
|---|---|---|
| M0 Walking skeleton | Done locally | Binary, Dockerfile, Makefile, `ci.yml` with every gate plus the publish job, kind + e2e. Not yet accepted: needs a PR run on GitHub and the Docker Hub variable/secret for the signed `sha-…` image. |
| M1 Data foundation | Done | CNPG on kind; migrations 00001–00004 tested up → down → up; sqlc; areas/categories/demo seed; normalization fuzzed; EXPLAIN shows index use ([architecture.md](architecture.md)). |
| M2 Vendors and local search | Done | Vendor/admin auth, shops with map pin and description, verification, listings, CSV upload, freshness, anomaly flags, reports, audit log, `/api/v1` local search with point / upazila / district scopes. |
| M3 Online sources | Done | Polite fetcher (robots, 5 s/host, retry, breaker), JSON-LD + microdata parsing, matching with variant checks, River refresh jobs, stale-while-revalidate. Star Tech and Tech Land are live on kind; reviews are in [sources.md](sources.md). |
| M4 AI estimation | Done | Evidence → sufficiency → baseline → LLM agent with read-only tools → validation → fallback; cache; async jobs; eval harness. Baseline: MAPE 5.29%, coverage 85.8% ([estimation.md](estimation.md)). |
| M5 Web UI | Done | All pages, bilingual (bn default, en), HTMX estimate polling, Leaflet maps, works without JS for core search; golden tests in both languages. |
| M6 Production hardening | Done | Prod overlay (HPA, PDB, NetworkPolicies, Gateway API + cert-manager, CNPG ×3 with Barman Cloud backups, External Secrets); metrics; tracing; runbook; load and rolling-restart results. Grafana dashboards dropped by the owner. |
| M7 Release v0.1.0 | Not started | README and SECURITY.md are done. Tagging, publishing and cosign verification need the owner's Docker Hub setup. |

## Evidence (local runs, 2026-11-10)

| Gate | Result |
|---|---|
| `make lint` | 0 issues (both modules) |
| `make test` | unit tests pass (race, shuffled) |
| `make test-integration` | 19 tests pass against PostGIS 17-3.6 |
| `make coverage` | merged overall 85.1% (≥ 70%); per-file floors met |
| `make vuln` | no reachable vulnerabilities; one unreachable (GO-2026-5932, x/crypto openpgp) |
| `make manifests` | dev, ci and prod pass `kubeconform -strict` with CRD schemas; policy tests pass |
| `make dockerfile-lint` | clean |
| `make image-check` | `version` OK; image filesystem 28.1 MB (budget 40 MB); Trivy clean |
| `make eval` | MAPE 5.29%, median abs. error ৳7, coverage 85.8%, 562/562 estimated |
| `make e2e` | 5/5 pass on kind; rolling restart 1,451 ok / 0 failed |
| `make load` | 50/s: 100% ok, p95 4 ms; 200/s: 100% ok, p95 5 ms ([architecture.md](architecture.md#load-and-rolling-restart)) |
| `make sources-smoke` | Star Tech and Tech Land pages parse; Archer C6 ৳3,530 at both, match 0.90 |

## Known gaps

- **CI never ran on GitHub.** `ci.yml` was checked for YAML validity and job wiring only; actionlint could not be run in this environment. Expect a first-run fix or two.
- **Publishing is skipped** until the owner sets `DOCKERHUB_USERNAME` (repository variable) and `DOCKERHUB_TOKEN` (secret). The optional real-cluster deploy job from §11.3 is not implemented (off by default anyway).
- **e2e in CI uses statistical estimates.** The offline `ci` overlay has no LLM, and the LLM path is covered by recorded-response unit tests instead ([ADR 0016](adr/0016-offline-ci-overlay.md)). This deviates from §11.2; owner to accept or reject.
- **No LLM backtest numbers.** Free-tier rate limits make a full run impractical. One manual run is recorded.
- **Areas:** no metropolitan thanas and no upazila coordinates in the open dataset ([data-sources.md](data-sources.md)).
- **Online coverage:** 20 curated electronics products from two shops. No compliant grocery source yet. Ultra Technology is pending review.
- **Rate limits are per pod** (in memory). With N web replicas the effective limit is up to N× ([ADR 0007](adr/0007-in-memory-rate-limits.md)). On kind every client shares one Docker IP, so the dev overlay raises the search limit.
- **Mobile:** the four result tabs scroll sideways on very narrow screens instead of wrapping.

## Needs from owner

1. Review the `development` branch and open a PR to `main`.
2. Docker Hub: set the repository variable `DOCKERHUB_USERNAME=matthew552` and store a personal access token as the `DOCKERHUB_TOKEN` secret, so CI publishes signed images.
3. Security contact for SECURITY.md (GitHub private reporting is the current default).
4. Production: domain, cluster and kubectl context, object-storage bucket and credentials for backups, secret manager for External Secrets.
5. Decide on Ultra Technology (pending review), and whether to accept ADR 0016.

## Next

1. Push to a branch, open a PR, and fix whatever the first GitHub Actions run finds.
2. Configure Docker Hub, merge, and verify the signed multi-arch `sha-…` image (completes M0 acceptance).
3. Tag `v0.1.0`.
