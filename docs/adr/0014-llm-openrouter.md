# 0014. LLM estimates through an OpenAI-compatible API (OpenRouter free model in dev)

- Status: accepted
- Date: 2026-11-10

## Context
The brief describes an optional LLM refinement (local Ollama as the manual test target) and requires owner approval before using a paid LLM or sending data to third parties. The owner supplied a free OpenRouter key and asked for the AI model to be enabled.

## Decision
- `internal/llm` speaks the OpenAI chat-completions protocol, so Ollama, OpenRouter or any compatible server works by config (`LLM_BASE_URL`, `LLM_MODEL`, `LLM_API_KEY`).
- The kind dev overlay uses `nvidia/nemotron-3-ultra-550b-a55b:free` on OpenRouter. Candidates rejected during testing: `google/gemma-4-31b-it:free` (rate-limited, HTTP 429) and `nemotron-3.5-lightning` (leaked Chinese characters into Bangla output).
- What leaves the cluster: product names and the evidence prices (demo and public online prices) plus area names. No user data.
- Validation (schema, cited evidence IDs exist, values inside the statistical envelope, Bangla explanation present, no CJK) with one retry, then the statistical fallback. Prod ships with `LLM_ENABLED=false`.

## Consequences
- Free models are slow (~40 s per estimate) and rate-limited; the estimate job has a 6-minute timeout and the UI polls.
- CI never calls an LLM; tests use recorded responses (`internal/llm` fake).
