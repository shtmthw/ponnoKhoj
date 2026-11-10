# 0013. Raise pg_trgm operator cost so the GIN index is used

- Status: accepted
- Date: 2026-11-10

## Context
On 20k seeded products the planner chose sequential scans for `search_key %> $1` because pg_trgm declares its operator functions with `COST 1`, making the filter look nearly free. A session-level `enable_seqscan` hint did not help.

## Decision
Migration `00003` sets `COST 100` on `similarity_op`, `word_similarity_op` and `word_similarity_commutator_op` when the migrating role owns them (otherwise it logs a notice), and CloudNativePG's `postInitApplicationSQL` does the same as superuser. `store.TrigramCostApplied` lets tests assert it. Plans are recorded in `docs/architecture.md`.

## Consequences
- Bitmap index scans on `products_search_key_trgm` / `product_aliases_key_trgm`.
- On a managed Postgres where the app cannot alter extension functions, a DBA must run the same statements.
