// Package server wires the HTTP surface: the OpenAI-compatible completions
// endpoint, health, and the Prometheus scrape endpoint.
package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// New builds the HTTP server. The completions handler is mounted at the
// OpenAI-compatible path so callers can point an existing OpenAI SDK at this base
// URL unchanged.
func New(addr string, h *Handler) *http.Server {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", promhttp.Handler())
	r.Post("/v1/chat/completions", h.ChatCompletions)

	return &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}
}
