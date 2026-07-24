// Package router selects a provider for a logical model and fails over along the
// route's ordered fallback chain when an attempt fails retryably.
package router

import (
	"context"
	"errors"
	"fmt"

	"github.com/rishabht877/PromptRelay/internal/config"
	"github.com/rishabht877/PromptRelay/internal/metrics"
	"github.com/rishabht877/PromptRelay/internal/providers"
	"github.com/rishabht877/PromptRelay/internal/types"
)

// ErrUnknownRoute is returned when a request names a model with no configured route.
var ErrUnknownRoute = errors.New("unknown route")

// Router resolves a logical model to a provider and handles fallback.
type Router struct {
	providers providers.Registry
	routes    map[string]config.Route
}

func New(reg providers.Registry, routes map[string]config.Route) *Router {
	return &Router{providers: reg, routes: routes}
}

// Result reports which concrete provider/model served a successful request.
type Result struct {
	Response types.ChatResponse
	Provider string
	Model    string
}

// Route returns the route config for a logical model, or false if unknown.
func (r *Router) Route(model string) (config.Route, bool) {
	route, ok := r.routes[model]
	return route, ok
}

// Dispatch tries each target in the route's fallback chain in order. It returns on
// the first success or the first terminal (non-retryable) error. A retryable error
// advances to the next target and increments the fallback metric.
func (r *Router) Dispatch(ctx context.Context, req types.ChatRequest) (Result, error) {
	route, ok := r.routes[req.Model]
	if !ok {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownRoute, req.Model)
	}

	var lastErr error
	for i, target := range route.Fallback {
		if err := ctx.Err(); err != nil {
			return Result{}, err // client gone or overall deadline exceeded
		}

		p, ok := r.providers[target.Provider]
		if !ok {
			lastErr = fmt.Errorf("provider %q not registered", target.Provider)
			continue
		}

		resp, err := p.Chat(ctx, target.Model, req)
		if err == nil {
			return Result{Response: resp, Provider: target.Provider, Model: target.Model}, nil
		}
		lastErr = err

		var provErr *types.ProviderError
		if errors.As(err, &provErr) && !provErr.Retryable() {
			// Terminal (e.g. 4xx): identical failure on every provider, so stop.
			return Result{}, err
		}

		// Retryable: record the hop to the next target, if any, then continue.
		if i+1 < len(route.Fallback) {
			reason := "unknown"
			if provErr != nil {
				reason = provErr.Reason()
			}
			metrics.Fallbacks.WithLabelValues(target.Provider, route.Fallback[i+1].Provider, reason).Inc()
		}
	}

	return Result{}, fmt.Errorf("all providers failed for route %q: %w", req.Model, lastErr)
}
