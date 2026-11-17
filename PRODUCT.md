# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

- **Shoppers in Bangladesh**, on mid-range Android phones over decent 4G. Two situations carry equal weight: checking a fair
  price for everyday goods (rice, oil, groceries) and which nearby shop has it before a trip out, and researching a bigger
  purchase (electronics, appliances) by comparing online prices with nearby shops. Shoppers never need an account.
- **Shop owners (vendors)** with no online store, who list their shop (name, address, map pin) and their prices one by one or by
  CSV upload, so nearby shoppers walk in.
- **Admins**, who verify shops, handle reports, review proposed products and unusual prices, and watch the health of each online
  source.

## Product Purpose

Find the best price for a product (*ponno*) in Bangladesh from online shops **and** from local offline shops near you. When
nobody nearby lists a product, give a price range built only from real evidence, or no estimate at all.

Current stage: a **showcase**. ponnoKhoj is a fully working, usable demonstration of human + AI-agent engineering. There are no
plans yet to recruit shops or users. Success means every flow works end to end and holds up to close inspection; it does not
mean traction, and none should be implied.

## Positioning

Local shops that have no online presence list their own prices, side by side with online shop prices. When neither exists, the
AI estimate is a **range** with a confidence level, a plain-language explanation in both languages, the model used and every
piece of evidence it relied on. No evidence, no guess: insufficient data returns no estimate instead of a number.

## Operating Context

- Search takes free text in Bangla, English or both (Bangla digits, units such as `১ কেজি` / `1kg`), a price source (online,
  local, estimate, all) and a location: a district, an upazila/thana, or an exact point with a radius (default 3 km, max 25 km).
  Only a rounded (~1 km) location is ever stored.
- Results show freshness: listings are fresh up to 7 days, stale up to 30, hidden after that. Online offers show when they were
  observed. Responses say which sources failed.
- Estimates run as background jobs; the page polls for the result.
- Online sources: Star Tech and Tech Land BD, electronics only (20 curated products). No compliant grocery source yet.
- Shops start pending and appear in search only after an admin verifies them. Demo shops are fictional and labelled "Demo".
- Shop pages give the map, address, price list and a directions link. Maps are Leaflet on OpenStreetMap.

## Capabilities and Constraints

- Server-rendered Go (`html/template`) with HTMX. The only JavaScript is vendored HTMX and Leaflet plus one small map script
  (`internal/web/static/app.js`). No Node build step, no SPA framework.
- Core search works without JavaScript; HTMX only enhances it.
- Phone first: every page must work on a small screen.
- Bilingual: every string lives in `internal/web/catalogs/en.json` and `bn.json`; numbers and currency format per locale. The
  currency is BDT (৳); money is stored as integer poisha.
- Not in scope for v1: payments, checkout, delivery, shopper accounts, native apps, recommendation feeds, vendor billing.
- Known gap: the result tabs scroll sideways on very narrow screens instead of wrapping.

## Brand Commitments

- Name: **ponnoKhoj (পণ্যখোঁজ)**. The ৳ sign is the logo mark.
- Credited as a human + AI agents project: Matthew (anon14none) sets direction and reviews; AI agents help build it.
- Voice, as established in the copy: plain, direct and honest about limits ("No evidence, no guess.", "Shoppers don't need an
  account.", "AI estimates are ranges, not quotes").

## Evidence on Hand

- Screenshots: `docs/screenshots/` (home, AI estimate in English and Bangla, local shops, online prices, shop, all sources on a
  map, vendor dashboard and prices, admin).
- Estimator backtest (`docs/estimation.md`, run 2026-11-10): 562 held-out listings, MAPE 5.29%, range coverage 85.8%.
- No real shops, users, testimonials, press or usage figures exist. Demo data is fictional and must stay labelled as demo.

## Product Principles

1. **Honest about gaps.** Show stale prices, failed sources and insufficient evidence plainly rather than hiding them or
   filling them in.
2. **Local shops are first-class.** A vendor-entered price from a shop with no website carries the same weight as an online offer.
3. **Two languages, both native.** Bangla is never a translation afterthought; mixed-script names, prices and addresses must
   read well.
4. **Works for everyone, on a phone.** No-JavaScript, keyboard and small-screen use are requirements, not fallbacks.
5. **Showcase means real.** Every flow works end to end; nothing fake is presented as real.

## Accessibility & Inclusion

Labelled controls, sufficient contrast and full keyboard navigation (from the build brief). Bangla script must render correctly
at every size, and core search must keep working without JavaScript.
