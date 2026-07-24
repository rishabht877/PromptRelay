package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/rishabht877/PromptRelay/internal/cache"
	"github.com/rishabht877/PromptRelay/internal/config"
	"github.com/rishabht877/PromptRelay/internal/embed"
	"github.com/rishabht877/PromptRelay/internal/metrics"
	"github.com/rishabht877/PromptRelay/internal/router"
	"github.com/rishabht877/PromptRelay/internal/types"
)

// Handler serves /v1/chat/completions. cache and embedder may be nil (e.g. Redis or
// Ollama unavailable); the handler degrades to a plain proxy in that case.
type Handler struct {
	router   *router.Router
	cache    *cache.Cache
	embedder embed.Embedder
	pricing  map[string]config.Price
	timeout  time.Duration
	log      *slog.Logger
}

func NewHandler(r *router.Router, c *cache.Cache, e embed.Embedder, pricing map[string]config.Price, timeout time.Duration, log *slog.Logger) *Handler {
	return &Handler{router: r, cache: c, embedder: e, pricing: pricing, timeout: timeout, log: log}
}

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	var req types.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Model == "" || len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "model and messages are required")
		return
	}
	if req.Stream {
		// Streaming is out of v1: it interacts awkwardly with full-response caching
		// and token accounting. Fail loudly rather than silently ignoring it.
		writeError(w, http.StatusBadRequest, "unsupported", "streaming is not supported in v1; set stream=false")
		return
	}

	route, ok := h.router.Route(req.Model)
	if !ok {
		observe("none", req.Model, "error", "disabled", start)
		writeError(w, http.StatusNotFound, "unknown_model", "no route configured for model "+req.Model)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	// Cache is opt-in per route AND limited to low-temperature requests. A high (or
	// unspecified, defaulting to 1.0) temperature means a non-deterministic answer we
	// must not reuse.
	eligible := h.cache != nil && h.embedder != nil && route.Cache.Enabled &&
		req.EffectiveTemperature() <= route.Cache.MaxTemperature

	var exactKey string
	var embedding []float32
	if eligible {
		normalized := cache.Normalize(req.Messages)
		exactKey = cache.ExactKey(req, normalized)

		// Tier 1: exact-hash fast path.
		if resp, hit, err := h.cache.GetExact(ctx, exactKey); err != nil {
			h.log.Warn("cache exact lookup failed", "err", err)
		} else if hit {
			metrics.CacheLookups.WithLabelValues("exact_hit").Inc()
			observe("cache", resp.Model, "ok", "exact_hit", start)
			writeJSON(w, http.StatusOK, resp)
			return
		}

		// Tier 2: semantic search. If embedding fails we can neither search nor
		// store, so fall through to the provider as an uncacheable request.
		if emb, err := h.embedder.Embed(ctx, normalized); err != nil {
			h.log.Warn("embedding failed; serving uncached", "err", err)
			eligible = false
		} else {
			embedding = emb
			if resp, sim, hit, err := h.cache.SearchSemantic(ctx, req.Model, emb, route.Cache.Threshold); err != nil {
				h.log.Warn("cache semantic search failed", "err", err)
			} else if hit {
				metrics.CacheLookups.WithLabelValues("semantic_hit").Inc()
				h.log.Info("semantic cache hit", "model", req.Model, "similarity", sim)
				observe("cache", resp.Model, "ok", "semantic_hit", start)
				writeJSON(w, http.StatusOK, resp)
				return
			}
			metrics.CacheLookups.WithLabelValues("miss").Inc()
		}
	}

	// Cache miss (or disabled): dispatch to a provider with fallback.
	result, err := h.router.Dispatch(ctx, req)
	if err != nil {
		status, code := classifyDispatchError(err)
		observe("none", req.Model, "error", cacheLabel(eligible), start)
		h.log.Warn("dispatch failed", "model", req.Model, "err", err)
		writeError(w, status, code, err.Error())
		return
	}

	// Record token usage and estimated cost for the provider that served the request.
	u := result.Response.Usage
	metrics.Tokens.WithLabelValues(result.Provider, result.Model, "prompt").Add(float64(u.PromptTokens))
	metrics.Tokens.WithLabelValues(result.Provider, result.Model, "completion").Add(float64(u.CompletionTokens))
	if c := h.cost(result.Model, u); c > 0 {
		metrics.Cost.WithLabelValues(result.Provider, result.Model).Add(c)
	}
	observe(result.Provider, result.Model, "ok", cacheLabel(eligible), start)

	// Populate the cache for future requests (best effort).
	if eligible && embedding != nil {
		if err := h.cache.Store(ctx, exactKey, req.Model, embedding, result.Response, route.Cache.TTL.Duration); err != nil {
			h.log.Warn("cache store failed", "err", err)
		}
	}

	writeJSON(w, http.StatusOK, result.Response)
}

func (h *Handler) cost(model string, u types.Usage) float64 {
	p, ok := h.pricing[model]
	if !ok {
		return 0
	}
	return float64(u.PromptTokens)/1e6*p.InputPer1M + float64(u.CompletionTokens)/1e6*p.OutputPer1M
}

func cacheLabel(eligible bool) string {
	if eligible {
		return "miss"
	}
	return "disabled"
}

func observe(provider, model, status, cache string, start time.Time) {
	metrics.Requests.WithLabelValues(provider, model, status, cache).Inc()
	metrics.Duration.WithLabelValues(provider, model).Observe(time.Since(start).Seconds())
}

// classifyDispatchError maps an internal error to an HTTP status. Terminal 4xx
// provider errors propagate their status; everything else is a 502.
func classifyDispatchError(err error) (int, string) {
	if errors.Is(err, router.ErrUnknownRoute) {
		return http.StatusNotFound, "unknown_model"
	}
	var provErr *types.ProviderError
	if errors.As(err, &provErr) && provErr.StatusCode >= 400 && provErr.StatusCode < 500 {
		return provErr.StatusCode, "upstream_client_error"
	}
	return http.StatusBadGateway, "upstream_error"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"type": code, "message": message},
	})
}
