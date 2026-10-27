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

## MCP server (learning project)

`cmd/promptrelay-mcp` is a small [Model Context Protocol](https://modelcontextprotocol.io)
server over stdio, added after the gateway was built as a way to learn MCP. It is a
learning project, not production software. It uses the official Go SDK
(`modelcontextprotocol/go-sdk` v1.8.0).

| Piece | Kind | What it does |
|---|---|---|
| `routes://config` | resource | The providers, route fallback chains and cache policy. Shows the *name* of each API key env var, never its value. |
| `compare_prompts` | tool, read-only | Embeds two prompts with the same normalization and embedding model the gateway uses, and reports cosine similarity plus whether each route's cache policy would reuse an answer. A prediction from the config: it does not read Redis or call a model. |
| `chat` | tool, not read-only | Sends one prompt to the **running gateway** over its HTTP API. It calls paid providers when the cache misses, so prompts are capped at 4000 characters and `max_tokens` at 512. |

```bash
make build-mcp                                    # or: go build -o bin/promptrelay-mcp ./cmd/promptrelay-mcp
PROMPTRELAY_CONFIG=configs/config.yaml PROMPTRELAY_URL=http://localhost:8080 ./bin/promptrelay-mcp
```

`chat` needs the gateway running; `compare_prompts` needs the local Ollama embedding
model. Logs go to stderr because stdout carries the protocol.

What is and isn't verified: the handlers are tested through a real MCP client over an
in-memory transport (annotations, secret redaction, input caps, error handling), and the
built binary was driven over stdio with the `2025-11-25` initialize handshake. It has
not been tested against the newer `2026-07-28` stateless flow or against a particular
MCP client application, and there is no authentication, since stdio assumes the client
launched it.

## Not in v1 (next steps)

- **Streaming** (`stream: true`) — rejected for now; it interacts awkwardly with
  full-response caching and token accounting.
- **Eval harness** — automated regression scoring of model outputs against a fixed
  dataset, gated in CI.
- **Helm chart / Kubernetes deploy.**
- **Circuit breaking and per-provider rate limiting; caller API-key auth.**
