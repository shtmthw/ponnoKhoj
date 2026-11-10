# JSON API (`/api/v1`)

Read-only except for requesting an estimate. No authentication. All responses are JSON (`application/json; charset=utf-8`). Prices are always returned both as integer **poisha** (1 taka = 100 poisha) and as a decimal string in taka:

```json
"price": { "poisha": 43800, "bdt": "438.00" }
```

Demo rows carry `"is_demo": true`. An estimate is never presented as a price: estimate objects carry `"is_estimate": true` and a bilingual `disclaimer`.

## Errors

```json
{ "error": { "code": "bad_location", "message": "give lat and lng, an upazila_id, or a district_id", "request_id": "…" } }
```

| HTTP | `code` | When |
|---|---|---|
| 400 | `missing_query` | no `q` (and no `product_id` for search) |
| 400 | `bad_mode` | `mode` is not `online`, `local`, `estimate` or `all` |
| 400 | `bad_location` | malformed `lat`/`lng`, unknown `upazila_id`/`district_id`, or an upazila outside the given district |
| 400 | `bad_parameter` | non-integer or negative ids, or `radius_m` above `SEARCH_MAX_RADIUS_M` |
| 400 | `bad_cursor` | a tampered or expired pagination cursor |
| 400 | `bad_body` | invalid JSON body for `POST /estimates` |
| 404 | `not_found`, `product_not_found`, `shop_not_found` | unknown route or id |
| 429 | `rate_limited` | the per-IP limit was exceeded (`RATE_LIMIT_*_PER_MIN`); retry later |
| 500 | `internal` | logged with the `request_id` |

## Location parameters

Search and estimates take **one** of the following (checked in this order):

| Parameters | Scope | Meaning |
|---|---|---|
| `lat`, `lng` (+ optional `radius_m`) | `point` | shops within `radius_m` metres (default `SEARCH_DEFAULT_RADIUS_M`, max `SEARCH_MAX_RADIUS_M`); results include `distance_m` |
| `upazila_id` (optional `district_id`, which must be its parent) | `upazila` | only shops registered in that upazila |
| `district_id` | `district` | every shop in the district, including all its upazilas |

The response's `scope` object says which applied, e.g. `{"kind":"district","area_id":16,"area_en":"Chattogram","area_bn":"চট্টগ্রাম"}`.

## `GET /api/v1/search`

| Param | |
|---|---|
| `q` | free text in English or Bangla (`miniket rice 5kg`, `মিনিকেট চাল`); or |
| `product_id` | an exact product |
| `mode` | `all` (default when empty), `local`, `online` or `estimate` |
| location | see above (required for local results and estimates) |
| `category_id` | optional filter |
| `cursor` | from `local.next_cursor` |

Response (abridged):

```json
{
  "query": "miniket rice 5kg", "normalized_query": "miniket rice 5000g", "mode": "all",
  "products": [{ "id": 21, "name_en": "Miniket Rice 5 kg", "name_bn": "মিনিকেট চাল ৫ কেজি", "unit": "g", "quantity": 5000, "size": "5 kg", "category_id": 2, "is_demo": true, "score": 1.25 }],
  "local": {
    "needs_location": false,
    "scope": { "kind": "point", "radius_m": 3000, "lat": 22.335, "lng": 91.834 },
    "radius_m": 3000, "total": 6, "next_cursor": "…",
    "listings": [{ "listing_id": 186, "price": {"poisha": 38100, "bdt": "381.00"}, "in_stock": true, "price_under_review": false,
                   "updated_at": "…", "freshness": "stale", "shop_id": 11, "shop_name": "Demo Store CTG-11", "shop_address": "…",
                   "lat": 22.34, "lng": 91.83, "distance_m": 660, "area_en": "Chattogram", "area_bn": "চট্টগ্রাম",
                   "directions_url": "https://www.openstreetmap.org/directions?…", "is_demo": true }]
  },
  "online": { "sources_enabled": 2, "covered": true, "refreshing": false, "last_refreshed_at": "…", "failed_sources": [],
              "offers": [{ "source_id": "startech", "source_name": "Star Tech", "is_demo": false, "url": "https://www.startech.com.bd/…",
                           "title": "…", "price": {"poisha": 353000, "bdt": "3530.00"}, "in_stock": true, "observed_at": "…" }] },
  "estimate": { "state": "none" }
}
```

- Local listings are sorted by price, then by id. `freshness` is `fresh`, `stale` (shown with a warning) or omitted when hidden (too old).
- `online.covered=false` means no enabled source carries this product. `refreshing=true` means a background refresh was queued and the offers shown are cached (stale-while-revalidate).
- `upazila`/`district` results have no `distance_m`.

## `GET /api/v1/products?q=…`

Up to 20 product candidates for a query (same matching as search): `{"products": [Product…]}`.

## `GET /api/v1/districts`

All 64 districts: `{"districts": [{"id", "name_en", "name_bn", "lat", "lng"}]}`.

## `GET /api/v1/shops/{id}`

A verified shop and its visible listings: `id, name, address, phone, opening_hours, area_en, area_bn, lat, lng, is_demo, directions_url, listings[]`.

## Estimates

`GET /api/v1/estimates?product_id=…&<location>[&radius_m=…]` returns the cached estimate for that product and place, if one exists. `POST /api/v1/estimates` with a JSON body starts one (asynchronously) if needed:

```json
{ "product_id": 23, "district_id": 16 }
{ "product_id": 23, "lat": 22.335, "lng": 91.834, "radius_m": 3000 }
```

Both return an estimate object. `state` is one of:

| `state` | Meaning |
|---|---|
| `none` | nothing requested yet (GET only) |
| `pending` | queued or running; poll the GET every few seconds |
| `ready` | `low`, `point`, `high` (Money), `confidence` (`low`/`medium`/`high`), `method` (`statistical` or `llm_assisted`), `model_id`, `fallback_reason`, `explanation_en`, `explanation_bn`, and `evidence` (the items used, with stable IDs) |
| `insufficient_data` | too little evidence; no range is given |
| `failed` | the job failed; try again later |

Every estimate object has `"is_estimate": true` and `disclaimer: {"en": "…", "bn": "…"}`.

## Health and metrics (not under `/api`)

`GET /livez`, `GET /readyz`; Prometheus `GET /metrics` on the separate metrics port (`METRICS_ADDR`, default `:9090`).
