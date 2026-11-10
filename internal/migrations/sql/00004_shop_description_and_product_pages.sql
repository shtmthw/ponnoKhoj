-- +goose Up
-- What a shop is about (what it sells, how big it is), written by its owner
-- so admins can judge a pending shop before verifying it. Additive: the
-- previous release keeps working (expand/contract).
ALTER TABLE shops
    ADD COLUMN description text NOT NULL DEFAULT ''
        CHECK (char_length(description) <= 1000);

-- Where a catalog product is sold online. Online sources whose robots.txt
-- forbids their search pages are read through their product pages only
-- (the "sitemap + structured data" access method, docs/sources.md), so each
-- product lists its page at each such store.
CREATE TABLE product_source_urls (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    product_id bigint NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    source_id  text   NOT NULL REFERENCES online_sources (id),
    url        text   NOT NULL CHECK (url ~ '^https://'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_id, url),
    UNIQUE (product_id, source_id, url)
);
CREATE INDEX product_source_urls_product_idx ON product_source_urls (product_id, source_id);

-- +goose Down
DROP TABLE product_source_urls;
ALTER TABLE shops DROP COLUMN description;
