-- name: CreateShop :one
INSERT INTO shops (owner_id, name, description, address, area_id, location, phone, opening_hours, status, is_demo)
VALUES (@owner_id, @name, @description, @address, @area_id, pk_point(@lat, @lng), @phone, @opening_hours, @status, @is_demo)
RETURNING id;

-- name: UpdateShop :execrows
UPDATE shops
SET name = @name, description = @description, address = @address, area_id = @area_id, location = pk_point(@lat, @lng),
    phone = @phone, opening_hours = @opening_hours, updated_at = now()
WHERE id = @id AND owner_id = @owner_id;

-- name: GetShop :one
SELECT s.id, s.owner_id, s.name, s.description, s.address, s.area_id, s.phone, s.opening_hours, s.status, s.auto_hidden, s.is_demo,
       s.created_at, s.updated_at,
       ST_Y(s.location::geometry)::float8 AS lat, ST_X(s.location::geometry)::float8 AS lng,
       a.name_en AS area_en, a.name_bn AS area_bn,
       a.kind AS area_kind,
       CASE WHEN a.kind = 'upazila' THEN COALESCE(pa.name_en, '') ELSE '' END AS parent_area_en,
       CASE WHEN a.kind = 'upazila' THEN COALESCE(pa.name_bn, '') ELSE '' END AS parent_area_bn
FROM shops s
JOIN areas a ON a.id = s.area_id
LEFT JOIN areas pa ON pa.id = a.parent_id
WHERE s.id = @id;

-- name: FindShopByOwnerAndName :one
SELECT id FROM shops WHERE owner_id = @owner_id AND name = @name ORDER BY id LIMIT 1;

-- name: ListShopsByOwner :many
SELECT s.id, s.name, s.description, s.address, s.status, s.auto_hidden, s.updated_at,
       a.name_en AS area_en, a.name_bn AS area_bn,
       (SELECT count(*) FROM listings l WHERE l.shop_id = s.id) AS listing_count
FROM shops s
JOIN areas a ON a.id = s.area_id
WHERE s.owner_id = @owner_id
ORDER BY s.created_at, s.id;

-- name: ListShopsByStatus :many
SELECT s.id, s.name, s.description, s.address, s.phone, s.opening_hours, s.status, s.auto_hidden, s.created_at,
       ST_Y(s.location::geometry)::float8 AS lat, ST_X(s.location::geometry)::float8 AS lng,
       a.name_en AS area_en, a.name_bn AS area_bn,
       CASE WHEN a.kind = 'upazila' THEN COALESCE(pa.name_en, '') ELSE '' END AS parent_area_en,
       CASE WHEN a.kind = 'upazila' THEN COALESCE(pa.name_bn, '') ELSE '' END AS parent_area_bn,
       u.email AS owner_email, u.display_name AS owner_name
FROM shops s
JOIN areas a ON a.id = s.area_id
LEFT JOIN areas pa ON pa.id = a.parent_id
JOIN users u ON u.id = s.owner_id
WHERE s.status = @status
ORDER BY s.created_at, s.id
LIMIT @max_results::int;

-- name: SetShopStatus :execrows
UPDATE shops SET status = @status, updated_at = now() WHERE id = @id;

-- name: SetShopAutoHidden :exec
UPDATE shops SET auto_hidden = @auto_hidden, updated_at = now() WHERE id = @id;

-- name: CountShops :one
SELECT count(*) FROM shops;
