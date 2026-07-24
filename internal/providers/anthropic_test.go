package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rishabht877/PromptRelay/internal/types"
)

// The Anthropic adapter is the one with real translation work: system messages move
// to a top-level field, max_tokens is required, and the response is content blocks.
// These are exactly the pieces that were never exercised against the live API, so
// they are pinned here with a mock server.
func TestAnthropicChat_ExtractsSystemJoinsBlocksMapsStop(t *testing.T) {
	var gotVersion, gotKey string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("anthropic-version")
		gotKey = r.Header.Get("x-api-key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"id":"msg_1","model":"claude-haiku-4-5",
			"content":[{"type":"text","text":"Hello"},{"type":"text","text":" world"}],
			"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":3}}`)
	}))
	defer srv.Close()

	p := NewAnthropic("anthropic", srv.URL, "key-123", srv.Client())
	resp, err := p.Chat(context.Background(), "claude-haiku-4-5", types.ChatRequest{
		Messages: []types.Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Headers.
	if gotVersion != anthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", gotVersion, anthropicVersion)
	}
	if gotKey != "key-123" {
		t.Errorf("x-api-key = %q", gotKey)
	}

	// System pulled out; messages contain only the non-system turn.
	if gotBody["system"] != "be brief" {
		t.Errorf("body.system = %v, want \"be brief\"", gotBody["system"])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("body.messages = %v, want exactly the 1 user message", gotBody["messages"])
	}
	// max_tokens defaulted because the caller omitted it (Anthropic requires it).
	if gotBody["max_tokens"] != float64(anthropicDefaultTok) {
		t.Errorf("body.max_tokens = %v, want %d", gotBody["max_tokens"], anthropicDefaultTok)
	}

	// Response: content blocks joined, stop_reason mapped, usage summed.
	if resp.Choices[0].Message.Content != "Hello world" {
		t.Errorf("content = %q, want \"Hello world\"", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop (from end_turn)", resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 3 || resp.Usage.TotalTokens != 13 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Provider != "anthropic" {
		t.Errorf("provider = %q", resp.Provider)
	}
}

func TestAnthropicChat_UsesProvidedMaxTokensAndStopSequences(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"id":"m","model":"claude-haiku-4-5","content":[{"type":"text","text":"ok"}],
			"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	p := NewAnthropic("anthropic", srv.URL, "k", srv.Client())
	maxTok := 256
	resp, err := p.Chat(context.Background(), "claude-haiku-4-5", types.ChatRequest{
		Messages:  []types.Message{{Role: "user", Content: "hi"}},
		MaxTokens: &maxTok,
		Stop:      []string{"END"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["max_tokens"] != float64(256) {
		t.Errorf("body.max_tokens = %v, want 256", gotBody["max_tokens"])
	}
	// OpenAI's `stop` maps to Anthropic's `stop_sequences`.
	stops, ok := gotBody["stop_sequences"].([]any)
	if !ok || len(stops) != 1 || stops[0] != "END" {
		t.Errorf("body.stop_sequences = %v, want [END]", gotBody["stop_sequences"])
	}
	// max_tokens stop_reason maps to "length".
	if resp.Choices[0].FinishReason != "length" {
		t.Errorf("finish_reason = %q, want length (from max_tokens)", resp.Choices[0].FinishReason)
	}
}
