# Runbook

All commands name their kubectl context explicitly. **Never rely on the current context.** On the owner's workstation the default context points at a real cluster.

- Local: `--context kind-ponnokhoj`
- Production: `--context <prod>`, after the owner has set it up

Namespace: `ponnokhoj`.

## Local environment (kind)

| Task | Command |
|---|---|
| Create everything (cluster, CNPG, cert-manager, Envoy Gateway, app) | `make kind-up` |
| Redeploy the working tree | `make deploy-dev` |
| Offline variant (demo shop, no LLM; what CI runs) | `make kind-up KIND_OVERLAY=ci` |
| Tear down | `make kind-down` |
| Stop / start without losing data | `docker stop ponnokhoj-control-plane` / `docker start ponnokhoj-control-plane` |
| Without Kubernetes (Docker Compose, http://localhost:8080) | `make dev-up` / `make dev-down` |

On Linux with Docker Desktop, the engine must be running first: `systemctl --user start docker-desktop`. Add `enable` to start it at login. The app is at http://localhost:8088 (https://localhost:8443 with a self-signed certificate). Demo logins are `admin@ponnokhoj.test` and `vendor@ponnokhoj.test`; their passwords are in `deploy/k8s/overlays/dev/.env.dev`.

## Deploy

`devctl deploy` (behind `make deploy-dev` and `make deploy-prod`) runs three phases:

1. It applies everything except the Deployments and the migration Job, then waits for the CNPG cluster to be `Ready`.
2. It applies the migration Job, named `ponnokhoj-migrate-<image tag or digest>`, and waits for it to complete. The Job runs `migrate up` (under an advisory lock), then `seed`; the dev overlay seeds with `--demo`.
3. It applies the Deployments, waits for both rollouts, and waits for the Gateway to be `Programmed`.

Production deploys by digest only:

```sh
make deploy-prod CONTEXT=<prod-context> IMAGE=docker.io/<user>/ponnokhoj@sha256:<digest>
```

Before the first production deploy:
- Install CloudNativePG, the Barman Cloud plugin, cert-manager, a Gateway API implementation (see [ADR 0005](adr/0005-gateway-api-envoy-gateway.md)), External Secrets Operator with a `ClusterSecretStore` named `ponnokhoj` ([ADR 0009](adr/0009-secrets.md)), and metrics-server (for the HPA).
- Replace the placeholders `ponnokhoj.example.com`, `REPLACE_ME-ponnokhoj-backups`.

## Roll back

```sh
kubectl --context <ctx> -n ponnokhoj rollout undo deployment/ponnokhoj-web
kubectl --context <ctx> -n ponnokhoj rollout undo deployment/ponnokhoj-worker
```

Migrations follow **expand/contract**: a release only adds tables and columns (with defaults), so the previous release keeps working against the newer schema. Rolling back the code therefore never needs a down migration. Columns are dropped one release after the code stops using them. Run `ponnokhoj migrate down` only if a migration itself is broken, and only after the rollback, from a one-off Job with the same image.

## Restore the database

Prod has continuous WAL archiving plus nightly base backups (02:00 UTC) to object storage through the Barman Cloud plugin, kept for 30 days (`deploy/k8s/overlays/prod/backup.yaml`).

1. Create a new `Cluster` (e.g. `ponnokhoj-db-restore`) with `bootstrap.recovery` pointing at the `ponnokhoj-backups` ObjectStore through the plugin. For point-in-time recovery, add `recoveryTarget.targetTime`. Follow the CloudNativePG docs for the plugin version pinned in `deploy/versions.env`.
2. Wait for it to be `Ready`, then check row counts and recent `audit_log` entries.
3. Point the app at it: switch the `DATABASE_URL` source (`ponnokhoj-db-app` Secret) to the restored cluster's app Secret, or rename the clusters, then restart web and worker.
4. Practise this on kind or a staging cluster before you need it.

On kind there are no backups. `make kind-down && make kind-up` rebuilds from seed data.

## Rotate secrets

| Secret | How |
|---|---|
| `APP_SECRET` (session and CSRF keys) | Change it in the secret manager (prod) or `.env.dev` (kind), then restart web and worker. All sessions are invalidated, so vendors and admins must log in again. |
| `LLM_API_KEY` | Change it at the provider, update the secret, and restart the worker. |
| Database password | Managed by CloudNativePG (`ponnokhoj-db-app`). Rotate it with CNPG's documented procedure, then restart web and worker. |
| Backup credentials (`ponnokhoj-backup-s3`) | Rotate them in the secret manager. External Secrets refreshes hourly. |
| `DOCKERHUB_TOKEN` (CI) | Create a new Docker Hub personal access token (read/write), update the repository secret, and revoke the old token. |

On kind, `.env.dev` is regenerated if you delete it (`bin/devctl dev-secrets`), then run `make deploy-dev`.

## Disable a misbehaving source

A shop's pages are failing, it complains, or its terms change:

1. Remove its ID from `SOURCES_ENABLED` in the overlay's `config.env` and redeploy. A quick alternative is `kubectl --context <ctx> -n ponnokhoj edit configmap` followed by `rollout restart deployment/ponnokhoj-worker deployment/ponnokhoj-web`.
2. If the compliance decision changed, update `internal/sources/source.go` (`Catalog`) and [sources.md](sources.md) in the same PR.
3. Its cached offers expire after `OFFER_CACHE_TTL`. The UI then shows the product as not covered.

The fetcher's circuit breaker already stops calling a host after repeated failures. Check `kubectl logs deploy/ponnokhoj-worker` for `online source refreshed` / `refresh failed` lines.

## Turn off LLM estimation

Set `LLM_ENABLED=false` in the overlay's `config.env`, then redeploy or restart the worker. New estimates use the statistical baseline; cached LLM estimates expire after `ESTIMATE_TTL`. For a hard stop, revoke the API key at the provider: the agent then fails, and every estimate falls back to the baseline.

## Health and observability

- `/livez` reports the process is up. `/readyz` reports the database is reachable and the server isn't draining.
- Prometheus metrics are on port 9090 at `/metrics` (never routed through the Gateway). The NetworkPolicy allows scraping from the `monitoring` namespace.
- Logs are JSON on stdout, with a `request_id` on every request.
