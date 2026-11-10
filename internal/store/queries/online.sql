-- name: UpsertSource :exec
INSERT INTO online_sources (id, name, base_url, access_method, compliance_status, enabled)
VALUES (@id, @name, @base_url, @access_method, @compliance_status, @enabled)
ON CONFLICT (id) DO UPDATE
SET name = EXCLUDED.name, base_url = EXCLUDED.base_url, access_method = EXCLUDED.access_method,
    compliance_status = EXCLUDED.compliance_status, enabled = EXCLUDED.enabled, updated_at = now();

-- name: ListSources :many
SELECT id, name, base_url, access_method, compliance_status, enabled, last_success_at, last_failure_at,
       last_error, consecutive_failures
FROM online_sources ORDER BY id;

-- name: RecordSourceSuccess :exec
UPDATE online_sources
SET last_success_at = now(), consecutive_failures = 0, last_error = '', updated_at = now()
WHERE id = @id;

-- name: RecordSourceFailure :exec
UPDATE online_sources
SET last_failure_at = now(), consecutive_failures = consecutive_failures + 1, last_error = @last_error, updated_at = now()
WHERE id = @id;

-- name: UpsertOffer :exec
INSERT INTO offers (source_id, product_id, url, title, price_poisha, in_stock, match_confidence, observed_at)
VALUES (@source_id, @product_id, @url, @title, @price_poisha, @in_stock, @match_confidence, @observed_at)
ON CONFLICT (source_id, product_id, url) DO UPDATE
SET title = EXCLUDED.title, price_poisha = EXCLUDED.price_poisha, in_stock = EXCLUDED.in_stock,
    match_confidence = EXCLUDED.match_confidence, observed_at = EXCLUDED.observed_at;

-- name: ListOffersByProduct :many
-- Online mode: cached offers for a product from currently enabled sources,
-- cheapest first.
SELECT o.id, o.source_id, src.name AS source_name, src.compliance_status, o.url, o.title, o.price_poisha,
       o.in_stock, o.match_confidence, o.observed_at
FROM offers o
JOIN online_sources src ON src.id = o.source_id
WHERE o.product_id = @product_id AND src.enabled AND o.observed_at >= @observed_since
ORDER BY o.price_poisha, o.id
LIMIT @max_results::int;

-- name: TouchProductRequest :one
-- Records that a shopper asked for this product's online prices and returns
-- the refresh state, so the caller can decide whether the cache is stale.
INSERT INTO product_refreshes (product_id) VALUES (@product_id)
ON CONFLICT (product_id) DO UPDATE SET last_requested_at = now()
RETURNING last_attempt_at, last_success_at, failed_sources;

-- name: GetProductRefresh :one
SELECT product_id, last_requested_at, last_attempt_at, last_success_at, failed_sources
FROM product_refreshes WHERE product_id = @product_id;

-- name: RecordProductRefresh :exec
INSERT INTO product_refreshes (product_id, last_attempt_at, last_success_at, failed_sources)
VALUES (@product_id, now(), CASE WHEN @succeeded::boolean THEN now() ELSE NULL END, @failed_sources)
ON CONFLICT (product_id) DO UPDATE
SET last_attempt_at = now(),
    last_success_at = CASE WHEN @succeeded::boolean THEN now() ELSE product_refreshes.last_success_at END,
    failed_sources = EXCLUDED.failed_sources;

-- name: ListProductsNeedingRefresh :many
-- Scheduled refresh: products shoppers asked for recently whose online
-- prices are stale. Products nobody asks for any more are left alone.
SELECT product_id FROM product_refreshes
WHERE last_requested_at >= @requested_since
  AND (last_attempt_at IS NULL OR last_attempt_at < @stale_before)
ORDER BY last_attempt_at NULLS FIRST, product_id
LIMIT @max_results::int;

-- name: EnsureSource :exec
-- Seeding: make sure a source row exists without touching its flags (the
-- web and worker processes own "enabled", from SOURCES_ENABLED).
INSERT INTO online_sources (id, name, base_url, access_method, compliance_status, enabled)
VALUES (@id, @name, @base_url, @access_method, @compliance_status, false)
ON CONFLICT (id) DO UPDATE
SET name = EXCLUDED.name, base_url = EXCLUDED.base_url, access_method = EXCLUDED.access_method,
    compliance_status = EXCLUDED.compliance_status, updated_at = now();

-- name: UpsertProductSourceURL :exec
INSERT INTO product_source_urls (product_id, source_id, url)
VALUES (@product_id, @source_id, @url)
ON CONFLICT (source_id, url) DO UPDATE SET product_id = EXCLUDED.product_id;

-- name: ListProductSourceURLs :many
SELECT url FROM product_source_urls
WHERE product_id = @product_id AND source_id = @source_id
ORDER BY id;

-- name: ListProductsWithSourceURLs :many
-- Products that some enabled online source can be asked about.
SELECT DISTINCT psu.product_id FROM product_source_urls psu
JOIN online_sources src ON src.id = psu.source_id
WHERE src.enabled
ORDER BY psu.product_id;

-- name: CountProductSourceURLs :one
-- How many enabled online sources have a page for this product.
SELECT count(DISTINCT psu.source_id) FROM product_source_urls psu
JOIN online_sources src ON src.id = psu.source_id
WHERE psu.product_id = @product_id AND src.enabled;
