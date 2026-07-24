// Package types holds the canonical, provider-agnostic request/response shapes.
//
// The gateway speaks an OpenAI-compatible schema at its edge so callers can point
// an existing OpenAI SDK at it unchanged. Every provider adapter translates these
// canonical types to/from its native wire format.
package types

// Message is a single chat message. Content is a plain string in v1 (no multi-part
// content blocks yet).
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the canonical inbound request. Model is a logical route name
// (see config.Route), not necessarily a concrete provider model id.
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	User        string    `json:"user,omitempty"`
}

// EffectiveTemperature returns the request temperature, defaulting to 1.0 when the
// caller omits it (matching OpenAI's default). The default is deliberately high so
// an unspecified request is treated as non-deterministic and skips the cache.
func (r ChatRequest) EffectiveTemperature() float64 {
	if r.Temperature == nil {
		return 1.0
	}
	return *r.Temperature
}

// Usage carries token accounting, always sourced from provider-reported counts.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Choice is one completion candidate.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// ChatResponse is the canonical outbound response (OpenAI-compatible on the wire).
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`

	// Provider is the concrete provider that served the response. Internal only —
	// excluded from the client-facing JSON.
	Provider string `json:"-"`
}
