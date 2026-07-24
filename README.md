# PromptRelay

An LLM gateway. It puts one OpenAI-compatible API in front of multiple providers
(OpenAI, Anthropic, and a local Ollama) and adds the things you actually need in
front of model calls: routing with provider fallback, a semantic response cache, and
per-model cost/latency metrics. Point an existing OpenAI SDK at it and nothing on the
client changes.

## Why

Calling one provider's SDK directly means the provider, retry behaviour, cost
tracking, and caching are all baked into every app. PromptRelay pulls that into a
single serving layer:

- **One API, many providers.** The edge speaks the OpenAI chat schema; adapters
  translate to each provider's native format (Anthropic's separate `system` field,
  Ollama's `/api/chat`), so callers don't care who serves the request.
- **Fallback that knows what's worth retrying.** A timeout, a 429, or a 5xx fails
  over to the next provider in the route. A 4xx (bad request, bad auth) returns
  immediately — it would fail identically everywhere.
- **A cache that's careful about what it reuses.** Exact repeats hit a hash; near
  matches hit a vector search — but only on routes that opt in, and only for
  low-temperature requests.
- **Observability built for model calls.** Tokens, estimated cost, and p50/p99
  latency per model and provider, in Prometheus/Grafana.

## The API

Point any OpenAI client at the gateway's base URL. `model` is a logical **route**
name (see [configs/config.yaml](configs/config.yaml)), not a provider model id.

```bash
curl localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "default",
    "messages": [{"role": "user", "content": "Say hi in five words."}]
  }'
```

The `default` route tries OpenAI first and falls back to Anthropic, then to local
Ollama. Routes, fallback chains, pricing, and per-route cache policy are all config.

## Quickstart (local)

```bash
# 1. Pull the models Ollama needs (they are not bundled). REQUIRED for the local
#    fallback route and for the semantic cache's embeddings.
docker compose -f deploy/docker-compose.yml up -d ollama
docker compose -f deploy/docker-compose.yml exec ollama ollama pull llama3.2
docker compose -f deploy/docker-compose.yml exec ollama ollama pull all-minilm

# 2. Bring up the whole stack (gateway, redis-stack, prometheus, grafana).
cp .env.example .env   # add OPENAI_API_KEY / ANTHROPIC_API_KEY if you have them
make compose-up

# 3. Call it.
curl localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"default","messages":[{"role":"user","content":"hello"}]}'
```

Without cloud keys, the `default` route falls all the way to local Ollama, so the
gateway is fully usable offline.

To run just the binary against your own Redis/Ollama: `make run`.

## How the semantic cache decides a "hit"

Caching is **off by default** and enabled per route. Even on a cache-enabled route,
only requests at or below the route's `max_temperature` (default `0.0`) are eligible —
a non-deterministic request shouldn't reuse a stored answer. Lookup is two tiers:

1. **Exact-hash fast path** — SHA-256 over the normalized messages, model, and
   answer-affecting params. Direct lookup, zero false positives, no embedding cost.
2. **Semantic path** (only on an exact miss) — embed the prompt with a local
   `all-MiniLM-L6-v2` (via Ollama) and run a Redis cosine KNN scoped to the model.
   Reuse the nearest entry only if similarity ≥ the route threshold (default `0.95`).

The threshold is deliberately high because embedding similarity is not meaning
equality: "what is 2+2" and "what is 2+3" are near-identical vectors with different
correct answers, and negation ("is X safe" vs "is X not safe") reads as similar. Since
no single threshold reliably separates a real paraphrase from a meaning-changing
near-duplicate, answer-sensitive routes should set `cache.semantic: false` to use the
**exact-hash tier only** (identical prompts still dedup; similar ones never reuse). The
exact-hash tier, the per-route semantic toggle, the high threshold, opt-in routes,
low-temperature gating, and a TTL are the guards against reusing an answer that
shouldn't be reused.

## Observability

`/metrics` exposes Prometheus counters/histograms; the bundled Grafana dashboard
("PromptRelay — Overview", at `localhost:3000`) shows request rate, p50/p99 latency,
tokens/sec, cumulative estimated cost per model, cache hit ratio, and provider
fallbacks. Cost is `tokens × a configurable per-model rate`, using provider-reported
token counts. The pricing table in config is illustrative — set it to the rates you
actually pay.

## Configuration

Everything is in [configs/config.yaml](configs/config.yaml): providers (with API keys
read from named env vars, never the file), routes and their fallback chains, the
per-route cache policy, and the pricing table. API keys are supplied via environment
only.

## Not in v1 (next steps)

- **Streaming** (`stream: true`) — rejected for now; it interacts awkwardly with
  full-response caching and token accounting.
- **Eval harness** — automated regression scoring of model outputs against a fixed
  dataset, gated in CI.
- **Helm chart / Kubernetes deploy.**
- **Circuit breaking and per-provider rate limiting; caller API-key auth.**
