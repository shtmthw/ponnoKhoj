# 0008. Vendored Leaflet + HTMX and one glue script

- Status: accepted
- Date: 2026-11-10

## Context
Pages are server-rendered (`html/template`) and core search must work with JavaScript disabled. Maps (pick a location, show shops) need Leaflet; estimates poll with HTMX. The CSP is strict: `script-src 'self'`, `style-src 'self'`, no inline code.

## Decision
Vendor HTMX 2.0.11 and Leaflet 1.9.4 under `internal/web/static/vendor` with their licenses, plus one hand-written `app.js` (~150 lines) that connects them: map init from `data-*` attributes, the location picker, the area/point toggle, applying `data-pos` positions (no inline styles under the CSP). A test fails if any other script appears.

## Consequences
- No bundler or npm in the build; the image stays a single static binary.
- Map tiles come from OpenStreetMap (`MAP_TILE_URL`, allowed in `img-src`), with attribution on every map. Note: the tile template is passed as `data-map-tiles`; an attribute name containing "url" makes `html/template` percent-encode `{z}/{x}/{y}` (a regression test covers this).
