# 0016. An offline `ci` overlay for the e2e gate

- Status: accepted
- Date: 2026-11-10

## Context
The dev overlay now reads real stores and calls OpenRouter (owner's request). Gate jobs must need no secrets and must not touch the live network (brief §5, §11.1).

## Decision
`deploy/k8s/overlays/ci` = dev + `SOURCES_ENABLED=demoshop`, `DEMO_SHOP_ENABLED=true`, `LLM_ENABLED=false`, and a NetworkPolicy allowing worker → web:8080 for the demo shop. CI runs `make kind-up KIND_OVERLAY=ci`; `make ci` does the same locally. A policy test pins these values.

## Consequences
- The e2e estimate test validates the statistical path in the cluster; the LLM path (agent loop, validation, fallback) is covered by unit tests with recorded responses rather than by an in-cluster fake LLM server. Brief §11.2 mentions "the fake LLM" in e2e; recorded here as a deviation for the owner to accept or reject.
