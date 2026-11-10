-- name: CreateReport :one
INSERT INTO reports (listing_id, shop_id, reason, note, reporter_hash)
VALUES (sqlc.narg(listing_id), sqlc.narg(shop_id), @reason, @note, @reporter_hash)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: CountOpenListingReports :one
SELECT count(DISTINCT reporter_hash) FROM reports WHERE listing_id = @listing_id AND status = 'open';

-- name: CountOpenShopReports :one
SELECT count(DISTINCT reporter_hash) FROM reports WHERE shop_id = @shop_id AND status = 'open';

-- name: ListOpenReports :many
SELECT r.id, r.listing_id, r.shop_id, r.reason, r.note, r.created_at,
       COALESCE(s.id, ls.id) AS target_shop_id,
       COALESCE(s.name, ls.name) AS target_shop_name,
       COALESCE(p.name_en, '') AS product_en,
       COALESCE(l.price_poisha, 0) AS price_poisha
FROM reports r
LEFT JOIN shops s ON s.id = r.shop_id
LEFT JOIN listings l ON l.id = r.listing_id
LEFT JOIN shops ls ON ls.id = l.shop_id
LEFT JOIN products p ON p.id = l.product_id
WHERE r.status = 'open'
ORDER BY r.created_at, r.id
LIMIT @max_results::int;

-- name: GetReport :one
SELECT id, listing_id, shop_id, reason, status FROM reports WHERE id = @id;

-- name: ResolveReportsForListing :execrows
UPDATE reports SET status = @status, resolved_at = now(), resolved_by = @resolved_by
WHERE listing_id = @listing_id AND status = 'open';

-- name: ResolveReportsForShop :execrows
UPDATE reports SET status = @status, resolved_at = now(), resolved_by = @resolved_by
WHERE shop_id = @shop_id AND status = 'open';

-- name: InsertAudit :exec
INSERT INTO audit_log (actor_id, action, entity_type, entity_id, detail)
VALUES (sqlc.narg(actor_id), @action, @entity_type, @entity_id, @detail);

-- name: ListAudit :many
SELECT a.id, a.actor_id, COALESCE(u.email, '') AS actor_email, a.action, a.entity_type, a.entity_id, a.detail, a.created_at
FROM audit_log a
LEFT JOIN users u ON u.id = a.actor_id
ORDER BY a.created_at DESC, a.id DESC
LIMIT @max_results::int;

-- name: BumpSearchStat :exec
-- Adds an aggregated count (pricefinder flushes in-memory counters), so the
-- per-day row is written once per flush, not once per search.
INSERT INTO search_stats (day, cell, mode, searches)
VALUES (CURRENT_DATE, @cell, @mode, @searches::bigint)
ON CONFLICT (day, cell, mode) DO UPDATE SET searches = search_stats.searches + EXCLUDED.searches;
