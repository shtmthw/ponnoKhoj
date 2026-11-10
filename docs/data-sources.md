# Data sources

## Administrative areas (divisions, districts, upazilas)

- **Dataset:** [nuhil/bangladesh-geocode](https://github.com/nuhil/bangladesh-geocode), commit `5622f68bd07a98e076edcf8100bf0db6a75b9854`.
- **Licence:** MIT. The licence text is shipped at `internal/seed/data/LICENSE.bangladesh-geocode`.
- **What we use:** 8 divisions, 64 districts and 494 upazilas, with English and Bangla names and parent links. District latitude/longitude is used for map centres and as the fallback point of a district.
- **Transformation:** `internal/seed/data/areas.json` is a trimmed copy (ids, names, parents, district coordinates). `ponnokhoj seed` loads it idempotently.

### Known gaps

- **No upazila coordinates.** The dataset has none, and we don't make them up. An upazila's map centre and estimate point is the mean of its registered shops' positions, falling back to the district centroid (see [ADR 0012](adr/0012-location-scopes.md)).
- **No metropolitan thanas.** City thanas (e.g. Dhaka's Dhanmondi, Chattogram's Panchlaish) are not in the dataset. Shops in cities register at district level or in the nearest listed upazila, and shoppers use "exact location" for neighbourhood searches. A cited source with a compatible licence would fix this.
- **No wards or neighbourhoods.**

## Categories

`internal/seed/seed.go`: 16 categories (groceries, household, electronics subcategories), written for ponnoKhoj and not taken from a third-party taxonomy.

## Reference catalog (real products)

`internal/seed/data/catalog.json` lists 20 real electronics products (routers, mice, monitors, power banks, USB drives, RAM, earbuds, speakers). Each entry has the brand and model as printed by the manufacturer, and the public product-page URLs at Star Tech and Tech Land. Prices are **not** stored in the seed; they come from the live pages under the policy in [sources.md](sources.md).

## Demo data

`ponnokhoj seed --demo` (dev and CI only; refused when `PONNOKHOJ_ENV=prod`) creates:
- 30 everyday products;
- 26 fictional shops ("Demo Store CTG-11", "Demo address 1 (fictional)") around real district/upazila positions in Chattogram and Dhaka;
- 539 listings with generated prices.

Every row is flagged `is_demo` and shown with a "Demo" badge. Demo prices are synthetic and not observations of real shops.

## Maps

Map tiles: © [OpenStreetMap](https://www.openstreetmap.org/copyright) contributors, served by `tile.openstreetmap.org` (configurable via `MAP_TILE_URL`). Every map shows the attribution. Heavy production traffic should move to a tile provider or self-hosted tiles, per the [OSM tile usage policy](https://operations.osmfoundation.org/policies/tiles/).
