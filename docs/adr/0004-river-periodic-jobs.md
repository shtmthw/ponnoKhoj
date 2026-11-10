# 0004. River for background work, periodic jobs instead of CronJobs

- Status: accepted
- Date: 2026-11-10

## Context
Online refreshes, estimates and maintenance run in the background. The brief allows only Postgres as a stateful dependency and prefers River periodic jobs over Kubernetes CronJobs.

## Decision
River (`riverqueue/river`) in the `worker`. Job kinds: `refresh_offers` (per product, unique while pending), `estimate` (per product/place cell, 6 min timeout to fit slow free LLMs) and `maintenance` (periodic every 15 minutes, run on start: expires sessions and cached estimates, queues refreshes for offers older than `OFFER_CACHE_TTL`).

## Consequences
- No CronJob manifests to keep in sync with the code; scheduling is tested in Go.
- Searches never wait on a shop: they return cached offers and enqueue a refresh (stale-while-revalidate).
