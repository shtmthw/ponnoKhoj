# 0011. Online sources: product pages + structured data only, per-store review

- Status: accepted
- Date: 2026-11-10

## Context
Compliance comes first (brief §5): each source needs a recorded ToS/robots review; none may be enabled if its terms forbid automated access; unclear ones need owner approval. The owner approved adding "the legit online stores".

## Decision
- Read only individual **product pages** that robots.txt allows, never search or listing pages; extract the schema.org `Product`/`Offer` (JSON-LD for Tech Land, microdata for Star Tech).
- A curated reference catalog (`internal/seed/data/catalog.json`) maps each product to its page URLs; the worker refreshes those pages. There is no crawling or discovery.
- The polite fetcher checks robots.txt on every request, waits 5 s between requests per host, sends `User-Agent: ponnoKhojBot (+contact URL)`, honors `Retry-After`, and trips a circuit breaker after repeated failures.
- Decisions per store are in `docs/sources.md`: Star Tech and Tech Land approved; Ryans forbidden (its terms ban scrapers); Ultra Technology pending (terms page unavailable during review).

## Consequences
- Coverage is limited to curated products (electronics); groceries have no compliant online source yet.
- Page-structure changes show up as `skipped` offers in logs and in `make sources-smoke`.
