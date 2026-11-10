# Online sources

Every online shop ponnoKhoj knows is listed here with its compliance decision. The same decisions are encoded in `internal/sources/source.go` (`Catalog`). A source can be named in `SOURCES_ENABLED` only if it is `approved` (or the fictional `demo` shop). Everything else is refused at startup. Policy and mechanics: [ADR 0011](adr/0011-online-sources-product-pages.md).

Reviewed 2026-11-10. Re-review a store whenever its terms or robots.txt change, and at least every six months.

## How ponnoKhoj reads a shop

- **Product pages only.** The reference catalog (`internal/seed/data/catalog.json`) maps each product to specific product-page URLs. The worker re-reads those pages when a cached offer is older than `OFFER_CACHE_TTL` (6 h on kind). It never crawls, follows links, or uses a shop's search.
- **Structured data.** The worker parses the schema.org `Product` / `Offer` the shop publishes for search engines, as JSON-LD or microdata (`internal/sources/jsonld.go`, `microdata.go`). It reads price, currency, availability and the canonical URL. It does not parse the visual HTML.
- **Polite fetching** (`internal/sources/fetch.go`):
  - It checks robots.txt before every request (cached).
  - At most one request per 5 s per host.
  - User-Agent: `ponnoKhojBot/1.0 (+<PUBLIC_BASE_URL>)`, so a site owner can find and contact us.
  - It honours `Retry-After` up to 30 s, and a circuit breaker opens after 5 consecutive failures.
- **Attribution.** Offers show the shop name, link to the shop's own page, and carry the disclaimer "prices may have changed; confirm on the shop's site".
- **Matching.** Page titles are matched to the catalog product, and a confidence below 0.6 (`matching.MinConfidence`) drops the offer. Variant conflicts (32 GB vs 128 GB, 10000 mAh vs 20000 mAh) cap the score at 0.2.

## Decisions

| Source | Site | Access method | robots.txt | Terms of service | Decision |
|---|---|---|---|---|---|
| `demoshop` | built into ponnoKhoj (`/demo-shop/`) | demo (JSON-LD search pages) | Only `ponnoKhojBot` allowed | Fictional data, labelled "Demo" everywhere | **demo**: dev/CI only, refused in prod |
| `startech` | Star Tech, https://www.startech.com.bd | product pages, schema.org microdata | `Allow: /`; disallows `/product/search?*` and tracking/query-string URLs; sitemap published | Terms cover sales, warranty and privacy; nothing on automated access | **approved** (owner asked for legitimate online stores; enabled on kind) |
| `techland` | Tech Land BD, https://www.techlandbd.com | product pages, schema.org JSON-LD | `Allow: /`; disallows every URL with a query string (`/*?`) | Terms cover pricing, orders and warranty; nothing on automated access | **approved** (enabled on kind) |
| `ryans` | Ryans Computers, https://www.ryans.com | n/a | `Allow: /` | **Forbids** "data mining, robots, or similar data gathering and extraction tools" and "robots, scrapers, offline readers" | **forbidden**: cannot be enabled. Would need written permission or an official feed. |
| `ultratech` | Ultra Technology, https://www.ultratech.com.bd | product pages, structured data (not built) | `Allow: /` with `Crawl-Delay: 20` | Terms page answered 503 during review | **pending review**: needs a completed ToS review and owner approval |

Looked at but not added:
- **Daraz** (www.daraz.com.bd): robots.txt disallows `/catalog/` and many query patterns; the marketplace terms need a separate review. Not reviewed.
- **Othoba** (www.othoba.com): robots.txt disallows `/search?` and catalog AJAX endpoints. Not reviewed.

Groceries have no compliant online source yet, so online prices exist only for the electronics in the reference catalog.

## Disabling a misbehaving source

Remove it from `SOURCES_ENABLED` and restart the worker (see [runbook](runbook.md#disable-a-misbehaving-source)). Cached offers expire on their own; the UI then shows "not covered".

## Checking that adapters still work

- `make sources-smoke` reads one live product page per approved source through the production fetcher. It is manual and never runs in CI.
- CI uses only committed fixtures (`test/fixtures/sources/`).
