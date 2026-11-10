-- +goose Up
CREATE TABLE online_sources (
    id                   text PRIMARY KEY CHECK (id ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    name                 text NOT NULL CHECK (name <> ''),
    base_url             text NOT NULL DEFAULT '',
    access_method        text NOT NULL CHECK (access_method IN ('official_api', 'affiliate_feed', 'structured_data', 'html', 'demo')),
    compliance_status    text NOT NULL CHECK (compliance_status IN ('approved', 'pending_review', 'forbidden', 'demo')),
    -- Mirrors the feature flag (SOURCES_ENABLED) of the last process to start.
    enabled              boolean NOT NULL DEFAULT false,
    last_success_at      timestamptz,
    last_failure_at      timestamptz,
    last_error           text NOT NULL DEFAULT '',
    consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    -- A source that is forbidden or still under review can never be enabled.
    CHECK (NOT enabled OR compliance_status IN ('approved', 'demo'))
);

CREATE TABLE offers (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id        text   NOT NULL REFERENCES online_sources (id),
    product_id       bigint NOT NULL REFERENCES products (id),
    url              text   NOT NULL CHECK (url ~ '^https?://'),
    title            text   NOT NULL,
    price_poisha     bigint NOT NULL CHECK (price_poisha > 0),
    in_stock         boolean NOT NULL DEFAULT true,
    match_confidence real   NOT NULL CHECK (match_confidence >= 0 AND match_confidence <= 1),
    observed_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_id, product_id, url)
);
CREATE INDEX offers_product_price_idx ON offers (product_id, price_poisha, id);

-- One row per product that shoppers have searched online: when it was last
-- asked for, when its online prices were last refreshed, and which sources
-- failed, so the UI can be honest about gaps.
CREATE TABLE product_refreshes (
    product_id        bigint PRIMARY KEY REFERENCES products (id) ON DELETE CASCADE,
    last_requested_at timestamptz NOT NULL DEFAULT now(),
    last_attempt_at   timestamptz,
    last_success_at   timestamptz,
    failed_sources    text[] NOT NULL DEFAULT '{}'
);
CREATE INDEX product_refreshes_requested_idx ON product_refreshes (last_requested_at);

CREATE TABLE estimates (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    product_id      bigint  NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    -- Geohash cell (~1 km) of the request; exact coordinates are never stored.
    cell            text    NOT NULL CHECK (cell ~ '^[0-9b-hjkmnp-z]{6}$'),
    radius_m        integer NOT NULL CHECK (radius_m > 0),
    status          text    NOT NULL CHECK (status IN ('pending', 'ready', 'insufficient_data', 'failed')),
    low_poisha      bigint,
    point_poisha    bigint,
    high_poisha     bigint,
    confidence      text CHECK (confidence IN ('low', 'medium', 'high')),
    method          text CHECK (method IN ('statistical', 'llm_assisted')),
    model_id        text NOT NULL DEFAULT '',
    fallback_reason text NOT NULL DEFAULT '',
    explanation_en  text NOT NULL DEFAULT '',
    explanation_bn  text NOT NULL DEFAULT '',
    evidence        jsonb NOT NULL DEFAULT '[]',
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    UNIQUE (product_id, cell, radius_m),
    -- A ready estimate is always a well-formed range, never a lone point.
    CHECK (status <> 'ready' OR (
        low_poisha > 0 AND low_poisha <= point_poisha AND point_poisha <= high_poisha
        AND confidence IS NOT NULL AND method IS NOT NULL))
);
CREATE INDEX estimates_expires_idx ON estimates (expires_at);

CREATE TABLE reports (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    listing_id    bigint REFERENCES listings (id) ON DELETE CASCADE,
    shop_id       bigint REFERENCES shops (id) ON DELETE CASCADE,
    reason        text   NOT NULL CHECK (reason IN ('wrong_price', 'closed', 'fake')),
    note          text   NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
    -- Keyed hash of the reporter's IP: lets us count distinct reporters
    -- without storing who they are.
    reporter_hash bytea  NOT NULL CHECK (octet_length(reporter_hash) = 32),
    status        text   NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'upheld', 'dismissed')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    resolved_at   timestamptz,
    resolved_by   bigint REFERENCES users (id),
    CHECK ((listing_id IS NULL) <> (shop_id IS NULL))
);
CREATE UNIQUE INDEX reports_listing_reporter_open_idx ON reports (listing_id, reporter_hash) WHERE listing_id IS NOT NULL AND status = 'open';
CREATE UNIQUE INDEX reports_shop_reporter_open_idx ON reports (shop_id, reporter_hash) WHERE shop_id IS NOT NULL AND status = 'open';
CREATE INDEX reports_open_idx ON reports (created_at) WHERE status = 'open';

-- Aggregate search counts by coarse (~1 km) cell. No exact coordinates.
CREATE TABLE search_stats (
    day      date   NOT NULL,
    cell     text   NOT NULL,
    mode     text   NOT NULL CHECK (mode IN ('online', 'local', 'estimate', 'all')),
    searches bigint NOT NULL DEFAULT 0 CHECK (searches >= 0),
    PRIMARY KEY (day, cell, mode)
);

-- +goose Down
DROP TABLE search_stats;
DROP TABLE reports;
DROP TABLE estimates;
DROP TABLE product_refreshes;
DROP TABLE offers;
DROP TABLE online_sources;
