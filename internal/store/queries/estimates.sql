-- name: GetEstimate :one
SELECT id, product_id, cell, radius_m, status, low_poisha, point_poisha, high_poisha, confidence, method,
       model_id, fallback_reason, explanation_en, explanation_bn, evidence, created_at, expires_at
FROM estimates
WHERE product_id = @product_id AND cell = @cell AND radius_m = @radius_m;

-- name: BeginEstimate :one
-- Creates a pending estimate, or resets one that has expired or failed.
-- Returns no row when a live (pending or unexpired) estimate already exists.
INSERT INTO estimates (product_id, cell, radius_m, status, expires_at)
VALUES (@product_id, @cell, @radius_m, 'pending', @expires_at)
ON CONFLICT (product_id, cell, radius_m) DO UPDATE
SET status = 'pending', low_poisha = NULL, point_poisha = NULL, high_poisha = NULL, confidence = NULL,
    method = NULL, model_id = '', fallback_reason = '', explanation_en = '', explanation_bn = '',
    evidence = '[]', created_at = now(), expires_at = EXCLUDED.expires_at
WHERE estimates.expires_at <= now() OR estimates.status = 'failed'
RETURNING id;

-- name: SaveEstimate :exec
UPDATE estimates
SET status = @status, low_poisha = sqlc.narg(low_poisha), point_poisha = sqlc.narg(point_poisha),
    high_poisha = sqlc.narg(high_poisha), confidence = sqlc.narg(confidence), method = sqlc.narg(method),
    model_id = @model_id, fallback_reason = @fallback_reason, explanation_en = @explanation_en,
    explanation_bn = @explanation_bn, evidence = @evidence, created_at = now(), expires_at = @expires_at
WHERE product_id = @product_id AND cell = @cell AND radius_m = @radius_m;

-- name: DeleteExpiredEstimates :execrows
DELETE FROM estimates WHERE expires_at <= now() - interval '1 day';

-- name: EvidenceListings :many
-- Trustworthy listings of one product between two distances from a point.
-- Unreviewed anomaly-flagged listings are never evidence.
SELECT l.id, l.shop_id, l.price_poisha, l.updated_at,
       ST_Distance(s.location, pk_point(@lat, @lng))::float8 AS distance_m
FROM listings l
JOIN shops s ON s.id = l.shop_id
WHERE l.product_id = @product_id
  AND s.status = 'verified' AND NOT s.auto_hidden
  AND NOT l.auto_hidden AND NOT l.anomaly_flagged
  AND l.updated_at >= @visible_since
  AND ST_DWithin(s.location, pk_point(@lat, @lng), @max_m::float8)
  AND NOT ST_DWithin(s.location, pk_point(@lat, @lng), @min_m::float8)
ORDER BY distance_m, l.id
LIMIT @max_results::int;

-- name: EvidenceOtherAreaListings :many
-- The same product further away, in districts of the same division as the
-- search location's nearest district. The seed dataset has no urban/rural
-- metadata, so "similar areas" is approximated by administrative proximity.
SELECT l.id, l.shop_id, l.price_poisha, l.updated_at,
       ST_Distance(s.location, pk_point(@lat, @lng))::float8 AS distance_m
FROM listings l
JOIN shops s ON s.id = l.shop_id
JOIN areas shop_area ON shop_area.id = s.area_id
JOIN areas shop_district ON shop_district.id = CASE WHEN shop_area.kind = 'district' THEN shop_area.id ELSE shop_area.parent_id END
WHERE l.product_id = @product_id
  AND s.status = 'verified' AND NOT s.auto_hidden
  AND NOT l.auto_hidden AND NOT l.anomaly_flagged
  AND l.updated_at >= @visible_since
  AND shop_district.parent_id = @division_id
  AND NOT ST_DWithin(s.location, pk_point(@lat, @lng), @min_m::float8)
ORDER BY distance_m, l.id
LIMIT @max_results::int;

-- name: EvidenceOffers :many
SELECT o.id, o.price_poisha, o.observed_at, o.source_id
FROM offers o
JOIN online_sources src ON src.id = o.source_id
WHERE o.product_id = @product_id AND o.observed_at >= @observed_since AND src.enabled
  AND o.match_confidence >= @min_confidence::real
ORDER BY o.observed_at DESC, o.id
LIMIT @max_results::int;

-- name: EvidenceComparables :many
-- Local listings of other products in the same category with the same unit
-- and a similar size. The caller rescales each price to the target size.
SELECT l.id, l.shop_id, l.price_poisha, l.updated_at, p.quantity,
       ST_Distance(s.location, pk_point(@lat, @lng))::float8 AS distance_m
FROM listings l
JOIN shops s ON s.id = l.shop_id
JOIN products p ON p.id = l.product_id
WHERE p.category_id = @category_id AND p.unit = @unit AND p.id <> @product_id AND p.status = 'active'
  AND p.quantity BETWEEN @min_quantity::bigint AND @max_quantity::bigint
  AND s.status = 'verified' AND NOT s.auto_hidden
  AND NOT l.auto_hidden AND NOT l.anomaly_flagged
  AND l.updated_at >= @visible_since
  AND ST_DWithin(s.location, pk_point(@lat, @lng), @radius_m::float8)
ORDER BY distance_m, l.id
LIMIT @max_results::int;

-- name: EvidenceHistory :many
-- Past prices of the product at nearby shops (superseded listing prices).
SELECT h.id, h.shop_id, h.price_poisha, h.recorded_at,
       ST_Distance(s.location, pk_point(@lat, @lng))::float8 AS distance_m
FROM listing_price_history h
JOIN shops s ON s.id = h.shop_id
JOIN listings l ON l.id = h.listing_id
WHERE h.product_id = @product_id
  AND s.status = 'verified' AND NOT s.auto_hidden AND NOT l.auto_hidden
  AND h.recorded_at >= @recorded_since
  AND h.price_poisha <> l.price_poisha
  AND ST_DWithin(s.location, pk_point(@lat, @lng), @radius_m::float8)
ORDER BY h.recorded_at DESC, h.id
LIMIT @max_results::int;

-- name: OnlineOfflinePairs :many
-- Products that have both trustworthy local listings and online offers, with
-- the median of each. The online->offline adjustment ratio is learned from
-- these pairs (per category near the point, then globally).
WITH local_prices AS (
    SELECT l.product_id,
           percentile_cont(0.5) WITHIN GROUP (ORDER BY l.price_poisha)::float8 AS median_local,
           bool_or(ST_DWithin(s.location, pk_point(@lat, @lng), @radius_m::float8))::boolean AS near
    FROM listings l
    JOIN shops s ON s.id = l.shop_id
    WHERE s.status = 'verified' AND NOT s.auto_hidden AND NOT l.auto_hidden AND NOT l.anomaly_flagged
      AND l.updated_at >= @visible_since
    GROUP BY l.product_id
), online_prices AS (
    SELECT o.product_id,
           percentile_cont(0.5) WITHIN GROUP (ORDER BY o.price_poisha)::float8 AS median_online
    FROM offers o
    JOIN online_sources src ON src.id = o.source_id
    WHERE src.enabled AND o.observed_at >= @observed_since
    GROUP BY o.product_id
)
SELECT p.id AS product_id, p.category_id, lp.median_local, op.median_online, lp.near
FROM local_prices lp
JOIN online_prices op ON op.product_id = lp.product_id
JOIN products p ON p.id = lp.product_id
WHERE p.id <> @exclude_product_id
ORDER BY p.id;
