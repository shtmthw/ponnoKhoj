# 0005. Gateway API with Envoy Gateway

- Status: accepted
- Date: 2026-11-10

## Context
The brief forbids the Ingress API (ingress-nginx was retired in March 2026) and asks for a maintained Gateway API implementation, TLS via cert-manager, and implementation-neutral manifests.

## Options
1. Envoy Gateway. 2. Traefik. 3. Cilium (requires replacing the CNI on kind).

## Decision
Envoy Gateway v1.10.0. The app manifests (`deploy/k8s/base/gateway.yaml`) use only standard `Gateway` + `HTTPRoute`; the only Envoy-specific object is the `GatewayClass` in `deploy/kind/gatewayclass.yaml`. We install the Gateway API CRDs bundled with the Envoy Gateway release (v1.6.1) rather than a newer upstream bundle, so the controller and CRDs are the combination upstream tests.

## Consequences
- Swapping implementations means changing the GatewayClass only.
- cert-manager issues the TLS certificate: self-signed issuer on kind, ACME in prod (placeholder domain until the owner provides one).
