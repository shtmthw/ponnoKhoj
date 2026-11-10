-- name: UpsertCategory :one
INSERT INTO categories (parent_id, slug, name_en, name_bn)
VALUES (sqlc.narg(parent_id), @slug, @name_en, @name_bn)
ON CONFLICT (slug) DO UPDATE
SET parent_id = EXCLUDED.parent_id, name_en = EXCLUDED.name_en, name_bn = EXCLUDED.name_bn
RETURNING id;

-- name: ListCategories :many
SELECT id, parent_id, slug, name_en, name_bn FROM categories ORDER BY name_en;

-- name: GetCategory :one
SELECT id, parent_id, slug, name_en, name_bn FROM categories WHERE id = @id;

-- name: InsertProduct :one
INSERT INTO products (category_id, name_en, name_bn, brand, model, unit, quantity, search_key, status, proposed_by, is_demo)
VALUES (@category_id, @name_en, @name_bn, @brand, @model, @unit, @quantity, @search_key, @status, sqlc.narg(proposed_by), @is_demo)
RETURNING id;

-- name: FindProductByIdentity :one
-- Used by the seeder to stay idempotent.
SELECT id FROM products
WHERE name_en = @name_en AND brand = @brand AND model = @model AND unit = @unit AND quantity = @quantity
ORDER BY id LIMIT 1;

-- name: InsertProductAlias :exec
INSERT INTO product_aliases (product_id, alias, alias_key)
VALUES (@product_id, @alias, @alias_key)
ON CONFLICT (product_id, alias_key) DO NOTHING;

-- name: GetProduct :one
SELECT p.id, p.category_id, p.name_en, p.name_bn, p.brand, p.model, p.unit, p.quantity, p.status, p.is_demo,
       c.slug AS category_slug, c.name_en AS category_en, c.name_bn AS category_bn
FROM products p
JOIN categories c ON c.id = p.category_id
WHERE p.id = @id;

-- name: SearchProducts :many
-- Fuzzy match on the ASCII search key and on aliases. "<%" is pg_trgm's
-- word-similarity operator; its threshold is set per connection (see
-- store.Open). The two branches of the UNION are deliberately separate
-- statements over one table each, so each can use its own GIN trigram index
-- (an OR across the two tables would force a scan of every product).
WITH matched AS (
    SELECT p.id FROM products p WHERE @key::text <% p.search_key
    UNION
    SELECT a.product_id FROM product_aliases a WHERE @key::text <% a.alias_key
)
SELECT p.id, p.category_id, p.name_en, p.name_bn, p.brand, p.model, p.unit, p.quantity, p.is_demo,
       GREATEST(
           word_similarity(@key::text, p.search_key),
           COALESCE((SELECT max(word_similarity(@key::text, a.alias_key)) FROM product_aliases a WHERE a.product_id = p.id), 0)
       )::float8 AS similarity
FROM matched m
JOIN products p ON p.id = m.id
WHERE p.status = 'active'
  AND (sqlc.narg(category_id)::bigint IS NULL OR p.category_id = sqlc.narg(category_id)::bigint)
ORDER BY similarity DESC, p.id
LIMIT @max_results::int;

-- name: ListActiveProducts :many
SELECT p.id, p.category_id, p.name_en, p.name_bn, p.brand, p.model, p.unit, p.quantity, c.name_en AS category_en
FROM products p
JOIN categories c ON c.id = p.category_id
WHERE p.status = 'active'
ORDER BY c.name_en, p.name_en, p.id;

-- name: ListProposedProducts :many
SELECT p.id, p.category_id, p.name_en, p.name_bn, p.brand, p.model, p.unit, p.quantity, p.created_at,
       c.name_en AS category_en, COALESCE(u.email, '') AS proposer_email
FROM products p
JOIN categories c ON c.id = p.category_id
LEFT JOIN users u ON u.id = p.proposed_by
WHERE p.status = 'proposed'
ORDER BY p.created_at, p.id
LIMIT @max_results::int;

-- name: SetProductStatus :execrows
UPDATE products SET status = @status, updated_at = now()
WHERE id = @id AND status = 'proposed';

-- name: CountProducts :one
SELECT count(*) FROM products WHERE status = 'active';
