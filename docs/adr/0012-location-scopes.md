# 0012. Three location scopes: point radius, upazila, district

- Status: accepted
- Date: 2026-11-10

## Context
The owner asked: with only a district, show every shop in the district and say so; with an upazila, show only that upazila's shops; with an exact location, let the user change the radius.

## Decision
`pricefinder.Place` resolves to one of:
- **point**: `ST_DWithin(location, point, radius)` (GiST index), radius chips 1/3/5/10/25 km, results sorted by price with distance shown;
- **upazila**: shops whose `area_id` is that upazila;
- **district**: shops whose area is the district or any of its upazilas.

Area scopes ignore the radius and show a banner naming the area. The seeded dataset has no upazila coordinates, so an upazila's representative point (used for the estimate cell and map centre) is the mean of its shops' positions, falling back to the district centroid; the estimate evidence radius is 5 km for an upazila and 10 km for a district.

## Consequences
- Vendors register a shop in a district or an upazila; picking the upazila is encouraged.
- City thanas (metro areas) are missing from the dataset (`docs/data-sources.md`).
