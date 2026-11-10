# 0007. In-memory per-IP rate limits

- Status: accepted
- Date: 2026-11-10

## Context
Search, estimate, login, sign-up and report endpoints need per-IP rate limits, honoring only a configured trusted-proxy list for `X-Forwarded-For`. No Redis (brief §4).

## Decision
Token buckets per client IP in each web pod (`internal/httpx/ratelimit.go`, `golang.org/x/time/rate`), with idle entries evicted. The client IP comes from `X-Forwarded-For` only when the direct peer is in `TRUSTED_PROXIES`.

## Consequences
- With N web replicas the effective limit is up to N times the configured one. Acceptable for abuse protection at this scale; revisit (e.g. a Postgres-backed counter) if limits must be exact.
