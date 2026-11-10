-- +goose Up
-- PostGIS powers radius queries; pg_trgm powers fuzzy product search.
-- On CloudNativePG these are pre-created by a superuser at bootstrap (PostGIS
-- is not a trusted extension); IF NOT EXISTS makes this a no-op there.
CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- pk_point is the ONLY place that knows PostGIS wants longitude first.
-- Everything else in the system passes (latitude, longitude).
-- +goose StatementBegin
CREATE FUNCTION pk_point(lat double precision, lng double precision) RETURNS geography
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    AS $$ SELECT ST_SetSRID(ST_MakePoint(lng, lat), 4326)::geography $$;
-- +goose StatementEnd

CREATE TABLE areas (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind        text   NOT NULL CHECK (kind IN ('division', 'district', 'upazila', 'ward', 'neighborhood')),
    parent_id   bigint REFERENCES areas (id),
    name_en     text   NOT NULL CHECK (name_en <> ''),
    name_bn     text   NOT NULL CHECK (name_bn <> ''),
    centroid    geography(Point, 4326),
    metadata    jsonb  NOT NULL DEFAULT '{}',
    source      text   NOT NULL CHECK (source <> ''),
    external_id text   NOT NULL CHECK (external_id <> ''),
    UNIQUE (source, kind, external_id)
);
CREATE INDEX areas_parent_idx ON areas (parent_id);
CREATE INDEX areas_kind_idx ON areas (kind);

CREATE TABLE categories (
    id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    parent_id bigint REFERENCES categories (id),
    slug      text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    name_en   text NOT NULL CHECK (name_en <> ''),
    name_bn   text NOT NULL CHECK (name_bn <> '')
);

CREATE TABLE users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         text NOT NULL UNIQUE CHECK (email = lower(email) AND email LIKE '%_@_%'),
    password_hash text NOT NULL CHECK (password_hash <> ''),
    display_name  text NOT NULL CHECK (display_name <> ''),
    role          text NOT NULL DEFAULT 'vendor' CHECK (role IN ('vendor', 'admin')),
    failed_logins integer NOT NULL DEFAULT 0 CHECK (failed_logins >= 0),
    locked_until  timestamptz,
    is_demo       boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash bytea  NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE products (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    category_id bigint NOT NULL REFERENCES categories (id),
    name_en     text   NOT NULL CHECK (name_en <> ''),
    name_bn     text   NOT NULL CHECK (name_bn <> ''),
    brand       text   NOT NULL DEFAULT '',
    model       text   NOT NULL DEFAULT '',
    unit        text   NOT NULL CHECK (unit IN ('g', 'ml', 'pc')),
    quantity    bigint NOT NULL CHECK (quantity > 0),
    -- ASCII search key built by internal/search.ProductKey.
    search_key  text   NOT NULL CHECK (search_key <> ''),
    status      text   NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'proposed', 'rejected')),
    proposed_by bigint REFERENCES users (id),
    is_demo     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX products_search_key_trgm ON products USING gin (search_key gin_trgm_ops);
CREATE INDEX products_category_idx ON products (category_id, unit, quantity) WHERE status = 'active';
CREATE INDEX products_proposed_idx ON products (created_at) WHERE status = 'proposed';

CREATE TABLE product_aliases (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    product_id bigint NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    alias      text   NOT NULL CHECK (alias <> ''),
    -- ASCII search key built by internal/search.AliasKey.
    alias_key  text   NOT NULL CHECK (alias_key <> ''),
    UNIQUE (product_id, alias_key)
);
CREATE INDEX product_aliases_key_trgm ON product_aliases USING gin (alias_key gin_trgm_ops);

CREATE TABLE shops (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id      bigint NOT NULL REFERENCES users (id),
    name          text   NOT NULL CHECK (name <> ''),
    address       text   NOT NULL CHECK (address <> ''),
    area_id       bigint NOT NULL REFERENCES areas (id),
    location      geography(Point, 4326) NOT NULL,
    phone         text   NOT NULL DEFAULT '',
    opening_hours text   NOT NULL DEFAULT '',
    status        text   NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'verified', 'suspended')),
    -- Set when open reports pass the threshold; cleared by an admin.
    auto_hidden   boolean NOT NULL DEFAULT false,
    is_demo       boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX shops_location_gist ON shops USING gist (location);
CREATE INDEX shops_owner_idx ON shops (owner_id);
CREATE INDEX shops_pending_idx ON shops (created_at) WHERE status = 'pending';

CREATE TABLE listings (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    shop_id             bigint NOT NULL REFERENCES shops (id) ON DELETE CASCADE,
    product_id          bigint NOT NULL REFERENCES products (id),
    price_poisha        bigint NOT NULL CHECK (price_poisha > 0),
    -- Always the product's base unit in v1 (see ADR 0006).
    unit                text   NOT NULL CHECK (unit IN ('g', 'ml', 'pc')),
    in_stock            boolean NOT NULL DEFAULT true,
    -- Price anomaly guard: flagged listings stay visible (labelled) but are
    -- not estimation evidence until an admin reviews them.
    anomaly_flagged     boolean NOT NULL DEFAULT false,
    anomaly_reviewed_at timestamptz,
    auto_hidden         boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shop_id, product_id)
);
CREATE INDEX listings_product_price_idx ON listings (product_id, price_poisha, id);
CREATE INDEX listings_anomaly_idx ON listings (updated_at) WHERE anomaly_flagged;

CREATE TABLE listing_price_history (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    listing_id   bigint NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    shop_id      bigint NOT NULL REFERENCES shops (id) ON DELETE CASCADE,
    product_id   bigint NOT NULL REFERENCES products (id),
    price_poisha bigint NOT NULL CHECK (price_poisha > 0),
    recorded_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX listing_price_history_product_idx ON listing_price_history (product_id, recorded_at DESC);

CREATE TABLE audit_log (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id    bigint REFERENCES users (id),
    action      text   NOT NULL CHECK (action <> ''),
    entity_type text   NOT NULL CHECK (entity_type <> ''),
    entity_id   bigint NOT NULL,
    detail      jsonb  NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_entity_idx ON audit_log (entity_type, entity_id, created_at DESC);
CREATE INDEX audit_log_created_idx ON audit_log (created_at DESC);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE listing_price_history;
DROP TABLE listings;
DROP TABLE shops;
DROP TABLE product_aliases;
DROP TABLE products;
DROP TABLE sessions;
DROP TABLE users;
DROP TABLE categories;
DROP TABLE areas;
DROP FUNCTION pk_point(double precision, double precision);
-- Extensions are left installed: other databases objects may rely on them and
-- dropping PostGIS needs a superuser.
