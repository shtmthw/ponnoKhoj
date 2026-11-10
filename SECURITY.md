# Security policy

## Reporting a vulnerability

Please **do not open a public issue** for security problems.

Report privately through GitHub's [private vulnerability reporting](https://github.com/shtmthw/ponnoKhoj/security/advisories/new) for this repository. <!-- The owner may replace or add a dedicated security e-mail address here. -->

Include what you found, how to reproduce it, and the impact you expect. You should get an acknowledgement within 7 days. Please give us a reasonable time to fix the issue before disclosing it.

## Supported versions

Only the latest release and `main` receive security fixes.

## What is in place

- **Input and output.**
  - All SQL is parameterized (sqlc).
  - Templates use `html/template` contextual escaping, with no `template.HTML` from user input.
  - Request bodies and CSV uploads have size and row limits.
- **Headers.**
  - Strict Content-Security-Policy (`script-src 'self'`, no inline code).
  - `X-Content-Type-Options: nosniff`, `Referrer-Policy`, `frame-ancestors 'none'`.
  - HSTS in prod.
  - Cross-origin protection on every state-changing request.
- **Accounts.**
  - Vendor and admin passwords are hashed with Argon2id.
  - Sessions are server-side, in `HttpOnly`, `SameSite=Lax` cookies (`Secure` in prod).
  - Login, sign-up, report, search and estimate endpoints are rate-limited per client IP. `X-Forwarded-For` is honoured only from configured trusted proxies.
- **LLM.**
  - Shop and product text is passed to the model as delimited data, never as instructions.
  - The model has read-only tools with no network or write access.
  - Its output is validated server-side, with a statistical fallback.
- **Supply chain.**
  - Dependencies are pinned. Actions are pinned by commit SHA, and images by digest.
  - CI runs `govulncheck`, `gosec` (golangci-lint) and Trivy.
  - Images are signed with cosign (keyless) and carry an SBOM and provenance.
- **Runtime.**
  - Distroless, non-root, read-only root filesystem, all capabilities dropped, seccomp `RuntimeDefault`.
  - Default-deny NetworkPolicies.
  - Secrets are never committed (see [ADR 0009](docs/adr/0009-secrets.md)).
