# 0009. Secrets: secretGenerator in dev, External Secrets in prod

- Status: accepted
- Date: 2026-11-10

## Decision
- Nothing secret is committed. `.env*` files are git-ignored (except `.env.example`).
- Dev (kind): `devctl dev-secrets` writes `deploy/k8s/overlays/dev/.env.dev` with random `APP_SECRET` and demo passwords; a Kustomize `secretGenerator` turns it into `ponnokhoj-secrets`. Optional keys (e.g. `LLM_API_KEY`) are added there by hand. Compose uses `deploy/compose/.env` the same way (`devctl compose-env`).
- Prod: External Secrets Operator syncs `ponnokhoj-secrets` and the backup credentials from the owner's secret manager through a `ClusterSecretStore` named `ponnokhoj` (`deploy/k8s/overlays/prod/externalsecret.yaml`). A policy test fails if prod renders a `Secret`.
- The database password is CloudNativePG's generated `ponnokhoj-db-app` Secret.

## Options considered
Sealed Secrets (encrypted secrets in git) was the alternative; External Secrets keeps rotation in the secret manager and needs nothing committed.

## Consequences
The owner must install External Secrets Operator and define the store before the first prod deploy (runbook).
