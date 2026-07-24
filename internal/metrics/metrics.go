// Package metrics defines PromptRelay's Prometheus collectors. Latency is exported
// as a histogram so p50/p99 are computed in Grafana via histogram_quantile rather
// than being baked (and averaged away) in the app.
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	// Requests counts completed requests by outcome. cache is one of
	// exact_hit|semantic_hit|miss|disabled; status is ok|error.
	Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "promptrelay_requests_total",
		Help: "Total chat completion requests.",
	}, []string{"provider", "model", "status", "cache"})

	// Tokens accumulates provider-reported token counts. type is prompt|completion.
	Tokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "promptrelay_tokens_total",
		Help: "Provider-reported tokens processed.",
	}, []string{"provider", "model", "type"})

	// Cost accumulates estimated USD, computed from the configurable pricing table.
	Cost = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "promptrelay_cost_usd_total",
		Help: "Estimated request cost in USD (tokens x configured per-model rate).",
	}, []string{"provider", "model"})

	// Duration is end-to-end request latency in seconds, labeled by the provider
	// that ultimately served the response.
	Duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "promptrelay_request_duration_seconds",
		Help:    "End-to-end request latency in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"provider", "model"})

	// Fallbacks counts each time the router moved from one provider to the next.
	Fallbacks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "promptrelay_fallbacks_total",
		Help: "Provider fallbacks by from/to/reason.",
	}, []string{"from", "to", "reason"})

	// CacheLookups counts cache outcomes. result is exact_hit|semantic_hit|miss.
	CacheLookups = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "promptrelay_cache_lookups_total",
		Help: "Semantic cache lookup outcomes.",
	}, []string{"result"})
)

// MustRegister registers all collectors on the default registry.
func MustRegister() {
	prometheus.MustRegister(Requests, Tokens, Cost, Duration, Fallbacks, CacheLookups)
}
