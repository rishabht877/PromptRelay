// Command gateway is the PromptRelay LLM gateway: one OpenAI-compatible API in front
// of multiple providers with routing, provider fallback, a semantic cache, and
// Prometheus cost/latency observability.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/rishabht877/PromptRelay/internal/cache"
	"github.com/rishabht877/PromptRelay/internal/config"
	"github.com/rishabht877/PromptRelay/internal/embed"
	"github.com/rishabht877/PromptRelay/internal/metrics"
	"github.com/rishabht877/PromptRelay/internal/providers"
	"github.com/rishabht877/PromptRelay/internal/router"
	"github.com/rishabht877/PromptRelay/internal/server"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	configPath := os.Getenv("PROMPTRELAY_CONFIG")
	if configPath == "" {
		configPath = "configs/config.yaml"
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Error("load config", "err", err)
		os.Exit(1)
	}

	metrics.MustRegister()

	reg := buildProviders(cfg, log)
	rtr := router.New(reg, cfg.Routes)

	// Embedding + cache are optional dependencies: if the embedding model or Redis is
	// unavailable the gateway still serves requests, just without caching.
	embedder := embed.NewOllama(cfg.Embedding.BaseURL, cfg.Embedding.Model, cfg.Embedding.Dimension, nil)
	var semCache *cache.Cache
	if c, ok := buildCache(cfg, log); ok {
		semCache = c
	}

	h := server.NewHandler(rtr, semCache, embedder, cfg.Pricing, cfg.Server.RequestTimeout.Duration, log)
	srv := server.New(cfg.Server.Addr, h)

	// Graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("gateway listening", "addr", cfg.Server.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
	}
}

func buildProviders(cfg *config.Config, log *slog.Logger) providers.Registry {
	reg := providers.Registry{}
	for name, p := range cfg.Providers {
		switch p.Type {
		case "openai":
			reg[name] = providers.NewOpenAI(name, p.BaseURL, apiKey(p.APIKeyEnv, name, log), nil)
		case "anthropic":
			reg[name] = providers.NewAnthropic(name, p.BaseURL, apiKey(p.APIKeyEnv, name, log), nil)
		case "ollama":
			reg[name] = providers.NewOllama(name, p.BaseURL, nil)
		default:
			log.Warn("unknown provider type; skipping", "provider", name, "type", p.Type)
		}
	}
	return reg
}

func apiKey(envVar, provider string, log *slog.Logger) string {
	if envVar == "" {
		return ""
	}
	key := os.Getenv(envVar)
	if key == "" {
		log.Warn("provider api key not set; requests to it will fail over", "provider", provider, "env", envVar)
	}
	return key
}

// buildCache connects to Redis and ensures the vector index. On any failure it logs
// and returns ok=false so the gateway runs without a cache instead of refusing to
// start.
func buildCache(cfg *config.Config, log *slog.Logger) (*cache.Cache, bool) {
	if cfg.Redis.Addr == "" {
		return nil, false
	}
	rdb := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Warn("redis unavailable; caching disabled", "addr", cfg.Redis.Addr, "err", err)
		return nil, false
	}
	c := cache.New(rdb, cfg.Redis.Index, cfg.Embedding.Dimension)
	if err := c.EnsureIndex(ctx); err != nil {
		log.Warn("could not create vector index; caching disabled", "err", err)
		return nil, false
	}
	log.Info("semantic cache ready", "index", cfg.Redis.Index, "dim", cfg.Embedding.Dimension)
	return c, true
}
