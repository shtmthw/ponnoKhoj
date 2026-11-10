-- name: UpsertListing :one
INSERT INTO listings (shop_id, product_id, price_poisha, unit, in_stock, anomaly_flagged)
VALUES (@shop_id, @product_id, @price_poisha, @unit, @in_stock, @anomaly_flagged)
ON CONFLICT (shop_id, product_id) DO UPDATE
SET price_poisha = EXCLUDED.price_poisha, unit = EXCLUDED.unit, in_stock = EXCLUDED.in_stock,
    anomaly_flagged = EXCLUDED.anomaly_flagged,
    anomaly_reviewed_at = CASE WHEN EXCLUDED.price_poisha = listings.price_poisha THEN listings.anomaly_reviewed_at ELSE NULL END,
    updated_at = now()
RETURNING id;

-- name: InsertPriceHistory :exec
-- recorded_at defaults to now(), which inside the listing's transaction is
-- exactly the listing's updated_at.
INSERT INTO listing_price_history (listing_id, shop_id, product_id, price_poisha, recorded_at)
VALUES (@listing_id, @shop_id, @product_id, @price_poisha, COALESCE(sqlc.narg(recorded_at)::timestamptz, now()));

-- name: GetListingByShopProduct :one
SELECT id, price_poisha FROM listings WHERE shop_id = @shop_id AND product_id = @product_id;

-- name: SetListingUpdatedAt :exec
-- Seeder and tests only: backdates a listing to exercise freshness states.
UPDATE listings SET updated_at = @updated_at WHERE id = @id;

-- name: GetListing :one
SELECT l.id, l.shop_id, l.product_id, l.price_poisha, l.unit, l.in_stock, l.anomaly_flagged, l.anomaly_reviewed_at,
       l.auto_hidden, l.updated_at, s.owner_id
FROM listings l
JOIN shops s ON s.id = l.shop_id
WHERE l.id = @id;

-- name: ListListingsByShop :many
SELECT l.id, l.product_id, l.price_poisha, l.in_stock, l.anomaly_flagged, l.auto_hidden, l.updated_at,
       p.name_en, p.name_bn, p.brand, p.unit, p.quantity, p.status AS product_status
FROM listings l
JOIN products p ON p.id = l.product_id
WHERE l.shop_id = @shop_id
ORDER BY p.name_en, l.id;

-- name: DeleteListing :execrows
DELETE FROM listings l USING shops s
WHERE l.id = @id AND s.id = l.shop_id AND s.owner_id = @owner_id;

-- name: LocalComparablePrices :many
-- Prices of the same product at other trustworthy shops near a point. Feeds
-- the price anomaly guard's local median.
SELECT l.price_poisha
FROM listings l
JOIN shops s ON s.id = l.shop_id
WHERE l.product_id = @product_id
  AND l.shop_id <> @exclude_shop_id
  AND s.status = 'verified' AND NOT s.auto_hidden
  AND NOT l.auto_hidden AND NOT l.anomaly_flagged
  AND l.updated_at >= @visible_since
  AND ST_DWithin(s.location, pk_point(@lat, @lng), @radius_m::float8)
ORDER BY l.price_poisha;

-- name: SearchLocalListings :many
-- Local mode around an exact point: listings of one product at verified
-- shops within a radius, cheapest first with distance as the tiebreaker.
-- Keyset-paginated on (price, distance, id); pass (0, -1, 0) for page one.
SELECT l.id AS listing_id, l.price_poisha, l.in_stock, l.anomaly_flagged, l.updated_at,
       s.id AS shop_id, s.name AS shop_name, s.address AS shop_address, s.phone AS shop_phone, s.is_demo,
       ST_Y(s.location::geometry)::float8 AS lat, ST_X(s.location::geometry)::float8 AS lng,
       ST_Distance(s.location, pk_point(@lat, @lng))::float8 AS distance_m,
       a.name_en AS area_en, a.name_bn AS area_bn,
       CASE WHEN a.kind = 'upazila' THEN COALESCE(pa.name_en, '') ELSE '' END AS parent_area_en,
       CASE WHEN a.kind = 'upazila' THEN COALESCE(pa.name_bn, '') ELSE '' END AS parent_area_bn
FROM listings l
JOIN shops s ON s.id = l.shop_id
JOIN areas a ON a.id = s.area_id
LEFT JOIN areas pa ON pa.id = a.parent_id
WHERE l.product_id = @product_id
  AND s.status = 'verified' AND NOT s.auto_hidden AND NOT l.auto_hidden
  AND l.updated_at >= @visible_since
  AND ST_DWithin(s.location, pk_point(@lat, @lng), @radius_m::float8)
  AND (l.price_poisha, ST_Distance(s.location, pk_point(@lat, @lng)), l.id)
      > (@after_price::bigint, @after_distance::float8, @after_id::bigint)
ORDER BY l.price_poisha, distance_m, l.id
LIMIT @page_size::int;

-- name: CountLocalListings :one
SELECT count(*)
FROM listings l
JOIN shops s ON s.id = l.shop_id
WHERE l.product_id = @product_id
  AND s.status = 'verified' AND NOT s.auto_hidden AND NOT l.auto_hidden
  AND l.updated_at >= @visible_since
  AND ST_DWithin(s.location, pk_point(@lat, @lng), @radius_m::float8);

-- name: SearchAreaListings :many
-- Local mode by area: listings of one product at verified shops registered
-- in an area. For a district that is the district itself plus all of its
-- upazilas; for an upazila, the upazila (upazilas have no child areas).
-- Cheapest first. Keyset-paginated on (price, id); pass (0, 0) for page one.
SELECT l.id AS listing_id, l.price_poisha, l.in_stock, l.anomaly_flagged, l.updated_at,
       s.id AS shop_id, s.name AS shop_name, s.address AS shop_address, s.phone AS shop_phone, s.is_demo,
       ST_Y(s.location::geometry)::float8 AS lat, ST_X(s.location::geometry)::float8 AS lng,
       a.name_en AS area_en, a.name_bn AS area_bn,
       CASE WHEN a.kind = 'upazila' THEN COALESCE(pa.name_en, '') ELSE '' END AS parent_area_en,
       CASE WHEN a.kind = 'upazila' THEN COALESCE(pa.name_bn, '') ELSE '' END AS parent_area_bn
FROM listings l
JOIN shops s ON s.id = l.shop_id
JOIN areas a ON a.id = s.area_id
LEFT JOIN areas pa ON pa.id = a.parent_id
WHERE l.product_id = @product_id
  AND s.status = 'verified' AND NOT s.auto_hidden AND NOT l.auto_hidden
  AND l.updated_at >= @visible_since
  AND (s.area_id = @area_id OR a.parent_id = @area_id)
  AND (l.price_poisha, l.id) > (@after_price::bigint, @after_id::bigint)
ORDER BY l.price_poisha, l.id
LIMIT @page_size::int;

-- name: CountAreaListings :one
SELECT count(*)
FROM listings l
JOIN shops s ON s.id = l.shop_id
JOIN areas a ON a.id = s.area_id
WHERE l.product_id = @product_id
  AND s.status = 'verified' AND NOT s.auto_hidden AND NOT l.auto_hidden
  AND l.updated_at >= @visible_since
  AND (s.area_id = @area_id OR a.parent_id = @area_id);

-- name: ListShopListingsPublic :many
-- Shop detail page: what a shopper may see.
SELECT l.id, l.product_id, l.price_poisha, l.in_stock, l.anomaly_flagged, l.updated_at,
       p.name_en, p.name_bn, p.brand, p.unit, p.quantity
FROM listings l
JOIN products p ON p.id = l.product_id
WHERE l.shop_id = @shop_id AND NOT l.auto_hidden AND p.status = 'active'
  AND l.updated_at >= @visible_since
ORDER BY p.name_en, l.id;

-- name: ListFlaggedListings :many
SELECT l.id, l.price_poisha, l.updated_at, l.shop_id, s.name AS shop_name,
       p.id AS product_id, p.name_en, p.name_bn
FROM listings l
JOIN shops s ON s.id = l.shop_id
JOIN products p ON p.id = l.product_id
WHERE l.anomaly_flagged
ORDER BY l.updated_at, l.id
LIMIT @max_results::int;

-- name: ReviewListingAnomaly :execrows
-- Approving clears the flag; rejecting hides the listing.
UPDATE listings
SET anomaly_flagged = false, anomaly_reviewed_at = now(), auto_hidden = (auto_hidden OR @hide::boolean)
WHERE id = @id AND anomaly_flagged;

-- name: SetListingAutoHidden :exec
UPDATE listings SET auto_hidden = @auto_hidden WHERE id = @id;

-- name: CountListings :one
SELECT count(*) FROM listings;
