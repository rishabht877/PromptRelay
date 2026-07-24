# Design decisions

Technical rationale for the choices that aren't obvious from the code.

## OpenAI-compatible edge

The gateway accepts the OpenAI chat-completions schema and translates outbound to
each provider. This means any existing OpenAI client works by changing only the base
URL — no new SDK, no client rewrite. Adapters absorb the provider differences
(Anthropic's top-level `system` field and required `max_tokens`; Ollama's `/api/chat`
shape and `prompt_eval_count`/`eval_count` usage fields).

## Fallback keyed on error class, not blanket retry

The router falls over to the next provider only for errors that could plausibly
succeed elsewhere: transport failures/timeouts (status 0), 429s, and 5xx. A 4xx is
terminal and returns immediately — a malformed request or bad key fails identically
on every provider, so retrying just adds latency and spend. See
`types.ProviderError.Retryable`.

## Local embeddings for the cache, not a hosted embedding API

The semantic cache embeds prompts with a local model (`all-MiniLM-L6-v2`) served by
the same Ollama that acts as a fallback provider. Calling a hosted embedding API on
every lookup would add a network round-trip and per-call cost to the exact path the
cache exists to make cheap and fast — self-defeating. Local keeps the whole
cache-lookup path in-cluster and free per request.

## Two-tier cache, off by default

Exact-hash first (free, exact), semantic KNN second (only on an exact miss). Caching
is opt-in per route and gated to low-temperature requests, because embedding
similarity is not answer equality (small edits, negation) and non-deterministic
requests must not reuse a stored answer. The high default similarity threshold (0.95)
is a guard, not a guarantee (see next).

## Answer-sensitive routes cache exact-hash only (`semantic: false`)

The similarity threshold cannot, by itself, make semantic reuse correct. Embedding
cosine similarity tracks surface/topic closeness, not answer-equality: a meaning-flip
near-duplicate ("what is 2+2" vs "what is 2+3") and a meaning-preserving paraphrase
("what is the capital of France" vs "which city is the capital of France") land only
about 0.05 apart in cosine, so no global threshold cleanly separates "safe to reuse"
from "must not reuse." The route-level control is therefore the real boundary: set
`cache.semantic: false` on answer-sensitive routes so they use the exact-hash tier
only — identical prompts still dedup safely, and similar-but-different prompts always
get a fresh answer. The semantic (vector KNN) tier defaults on and is meant for
genuinely paraphrase-tolerant content (FAQs, docs Q&A).

## Redis COSINE metric → no manual normalization

The vector index uses Redis's `COSINE` distance, which is scale-invariant, so
embeddings don't need to be L2-normalized before storage or query. Similarity is
derived as `1 - distance`. The index dimension is fixed at creation to the embedding
model's width (384); changing the embedding model requires dropping and rebuilding
the index.

## Optional dependencies degrade, they don't block startup

If Redis or the embedding model is unavailable, the gateway logs a warning and runs
as a plain routing proxy rather than refusing to boot. Cache and embedding failures
at request time are treated as misses, never as request failures.

## Latency as a histogram, quantiles in Grafana

`promptrelay_request_duration_seconds` is a histogram; p50/p99 are computed at query
time via `histogram_quantile`. Averaging latency in the app would hide the tail,
which is the part that matters.
