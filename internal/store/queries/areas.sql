-- name: UpsertArea :one
INSERT INTO areas (kind, parent_id, name_en, name_bn, centroid, metadata, source, external_id)
VALUES (
    @kind, sqlc.narg(parent_id), @name_en, @name_bn,
    CASE WHEN sqlc.narg(lat)::float8 IS NULL OR sqlc.narg(lng)::float8 IS NULL THEN NULL
         ELSE pk_point(sqlc.narg(lat)::float8, sqlc.narg(lng)::float8) END,
    @metadata, @source, @external_id)
ON CONFLICT (source, kind, external_id) DO UPDATE
SET parent_id = EXCLUDED.parent_id, name_en = EXCLUDED.name_en, name_bn = EXCLUDED.name_bn,
    centroid = EXCLUDED.centroid, metadata = EXCLUDED.metadata
RETURNING id;

-- name: CountAreas :one
SELECT count(*) FROM areas;

-- name: ListDistricts :many
-- Districts are the areas that carry a centroid in the seed dataset, so they
-- are what a shopper can pick as a search location.
SELECT a.id, a.name_en, a.name_bn,
       ST_Y(a.centroid::geometry)::float8 AS lat, ST_X(a.centroid::geometry)::float8 AS lng
FROM areas a
WHERE a.kind = 'district' AND a.centroid IS NOT NULL
ORDER BY a.name_en;

-- name: ListUpazilas :many
SELECT u.id, u.name_en, u.name_bn, d.id AS district_id, d.name_en AS district_en, d.name_bn AS district_bn
FROM areas u
JOIN areas d ON d.id = u.parent_id
WHERE u.kind = 'upazila'
ORDER BY d.name_en, u.name_en;

-- name: GetArea :one
SELECT a.id, a.kind, a.parent_id, a.name_en, a.name_bn, a.metadata,
       (a.centroid IS NOT NULL)::boolean AS has_centroid,
       COALESCE(ST_Y(a.centroid::geometry), 0)::float8 AS lat,
       COALESCE(ST_X(a.centroid::geometry), 0)::float8 AS lng,
       COALESCE(p.name_en, '') AS parent_en, COALESCE(p.name_bn, '') AS parent_bn
FROM areas a
LEFT JOIN areas p ON p.id = a.parent_id
WHERE a.id = @id;

-- name: GetAreaByName :one
SELECT a.id FROM areas a WHERE a.kind = @kind AND a.name_en = @name_en ORDER BY a.id LIMIT 1;

-- name: NearestDistrict :one
-- The district whose centroid is closest to a point: a coarse description of
-- where a search happened, used as context for estimates.
SELECT a.id, a.name_en, a.name_bn,
       ST_Distance(a.centroid, pk_point(@lat, @lng))::float8 AS distance_m
FROM areas a
WHERE a.kind = 'district' AND a.centroid IS NOT NULL
ORDER BY a.centroid <-> pk_point(@lat, @lng)
LIMIT 1;

-- name: ListUpazilasByDistrict :many
SELECT id, name_en, name_bn FROM areas
WHERE kind = 'upazila' AND parent_id = @district_id
ORDER BY name_en;

-- name: AreaShopCentroid :one
-- The mean position of the verified shops registered in an area (and, for a
-- district, its upazilas). The seed dataset has no upazila coordinates, so
-- this is how an upazila-scoped search gets a point for estimates and maps.
SELECT count(*)::int AS shops,
       COALESCE(avg(ST_Y(s.location::geometry)), 0)::float8 AS lat,
       COALESCE(avg(ST_X(s.location::geometry)), 0)::float8 AS lng
FROM shops s
JOIN areas a ON a.id = s.area_id
WHERE s.status = 'verified' AND NOT s.auto_hidden
  AND (s.area_id = @area_id OR a.parent_id = @area_id);
