# AI price estimation

When no nearby shop lists a product, ponnoKhoj can estimate what it probably costs in that area. The rule: **an estimate is built only from evidence the system retrieved. With no evidence there is no estimate**, and the answer is `insufficient_data` rather than a guess. Every estimate is a range (low, point, high) with a confidence level and the disclaimer that the real price may be higher or lower.

Code: `internal/estimate` (pipeline), `internal/llm` (client), `internal/jobs` (async job), `internal/web/templates/estimate.html` (UI, polled with HTMX).

## Pipeline

1. **Gather evidence** (deterministic, `evidence.go`). Sources, in decreasing trust, with their base weights:

   | Type | What | Weight |
   |---|---|---|
   | `local_listing` | same product, in expanding rings up to 25 km | 1.0 |
   | `other_area_listing` | same product, further away in the same division | 0.5 |
   | `price_history` | a superseded price of the same product nearby | 0.5 |
   | `online_offer` | same product online, scaled by the learned online-to-offline ratio | 0.6 (0.35 if no ratio could be learned) |
   | `comparable_listing` | similar product in the same category, rescaled by size | 0.3 |

   Weights decay with distance (e-folding 10 km) and age (half-life 14 days). Listings older than 30 days, offers older than 7 days and history older than 90 days are ignored. Evidence IDs are stable: a type letter plus the row ID (e.g. `L186`).
2. **Sufficiency check.** The pipeline needs at least `ESTIMATE_MIN_EVIDENCE` (default 3) independent items, and at least one fresher than 7 days. Otherwise it returns `insufficient_data`.
3. **Statistical baseline** (`baseline.go`, always computed):
   - point: the weighted median;
   - range: the weighted interquartile spread, widened when the effective sample size is small, with a minimum width;
   - confidence: from evidence count, agreement and freshness.
4. **Optional LLM refinement** (`agent.go`, only when `LLM_ENABLED=true`). The model gets four read-only tools that serve only the evidence already gathered: `get_local_listings`, `get_online_offers`, `get_comparables`, `get_area_info`. It has no network or write access. Shop and product names are passed as delimited data fields, never as instructions. It must answer with one JSON object: low/point/high in taka, confidence, the evidence IDs it used, and an explanation in English and Bangla.
5. **Validation** (`validate.go`). The model's answer is rejected unless all of these hold:
   - the schema is valid and low ≤ point ≤ high;
   - every cited ID exists in the gathered evidence;
   - all values fall inside the envelope [`ESTIMATE_ENVELOPE_LOW` × cheapest evidence, `ESTIMATE_ENVELOPE_HIGH` × dearest evidence] (0.5× and 1.5×);
   - the Bangla explanation actually contains Bangla, and no CJK characters appear.

   On rejection the model gets one retry with the validation error. After that, the **statistical baseline is returned** with `fallback_reason` recorded.
6. **Cache.** Results are cached per product, ~1 km cell (or area) and radius bucket for `ESTIMATE_TTL` (24 h).

Location scopes ([ADR 0012](adr/0012-location-scopes.md)):
- point: the user's radius;
- upazila: a 5 km evidence radius around the upazila's representative point;
- district: a 10 km radius around the district point.

## Evaluation harness

`ponnokhoj estimate eval` (or `make eval`) backtests the estimator. For each listing in a fixture dataset it hides the listing, estimates its price from the rest through the same gathering code (in-memory store), and compares.

### Statistical baseline: CI gate

Dataset: `test/fixtures/eval/dataset.json`. Thresholds (committed, `test/fixtures/eval/thresholds.json`): MAPE ≤ 7%, range coverage ≥ 80%, estimated share ≥ 95%.

Run on 2026-11-10:

```
statistical baseline:
  held-out listings: 562, estimated: 562 (100.0%)
  MAPE: 5.29%
  median absolute error: ৳7.00
  range coverage: 85.8%
baseline meets the committed thresholds
```

### LLM-assisted: manual runs

The LLM path is not run in CI. CI never calls a model; unit tests replay recorded responses, including hallucinated evidence IDs and out-of-envelope values, which validation must reject. Manual checks:

- `make llm-live` runs the estimator against the configured `LLM_BASE_URL` / `LLM_MODEL`.
- The kind deployment uses OpenRouter's free `nvidia/nemotron-3-ultra-550b-a55b:free` ([ADR 0014](adr/0014-llm-openrouter.md)).

Recorded run, 2026-11-10, on kind:

| Product | Place | Result | Method | Time |
|---|---|---|---|---|
| Nazirshail Rice 5 kg | Chattogram district | ৳440 – ৳490, point ৳465 | `llm_assisted`; cited L186, L206, L224, L246; explanations in English and Bangla, amounts in taka | ~43 s |

Models tried and rejected:
- `google/gemma-4-31b-it:free`: rate-limited (HTTP 429) on most calls.
- `nemotron-3.5-lightning`: put Chinese characters ("之间") into the Bangla explanation. This led to the Bangla/CJK validation rule.

**Not yet measured:** a full LLM backtest (MAPE / coverage for the LLM path on the fixture dataset). The free tier's rate limits make a 562-item run impractical. Do it with a local Ollama model or a paid key (owner approval needed) and add the numbers here.

## Turning the LLM off

Set `LLM_ENABLED=false` and restart the worker. Estimates then come from the statistical baseline only. See the [runbook](runbook.md#turn-off-llm-estimation).
