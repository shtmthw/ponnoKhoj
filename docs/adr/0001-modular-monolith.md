# 0001. One binary, modular monolith

- Status: accepted
- Date: 2026-11-10

## Context
The brief asks for a modular monolith with `serve` and `worker` sharing internal packages, and no microservices. The product has three price modes (local, online, estimate), one database and a small team (agents plus one owner).

## Options
1. One binary with subcommands (`serve`, `worker`, `migrate`, `seed`, `healthcheck`, `version`, `config check`, `estimate eval`).
2. Separate services for search, sources and estimation.

## Decision
Option 1. `cmd/ponnokhoj` dispatches subcommands; domain logic lives in `internal/` packages (`pricefinder`, `sources`, `estimate`, `vendor`, `moderation`, `store`, ...). `serve` and `worker` run as separate Deployments from the same image, so they scale independently.

## Consequences
- One image to build, scan, sign and deploy; one migration history.
- Package boundaries are enforced by review, not by the network. `internal/app` is the only place that wires everything together.
- Revisit if one workload needs a different language runtime or release cadence.
