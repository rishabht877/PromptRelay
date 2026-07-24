// Package providers defines the Provider abstraction and one adapter per upstream.
// Adapters translate the canonical types.ChatRequest/ChatResponse to and from each
// provider's native wire format and normalize failures into *types.ProviderError.
package providers

import (
	"context"

	"github.com/rishabht877/PromptRelay/internal/types"
)

// Provider is a single upstream LLM backend.
type Provider interface {
	// Name is the provider's identifier as used in config and metrics labels.
	Name() string
	// Chat runs a completion. model is the concrete provider model id (from the
	// route target), which may differ from the logical req.Model.
	Chat(ctx context.Context, model string, req types.ChatRequest) (types.ChatResponse, error)
}

// Registry maps provider name -> Provider.
type Registry map[string]Provider
