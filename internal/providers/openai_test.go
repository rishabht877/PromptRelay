package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rishabht877/PromptRelay/internal/types"
)

func TestOpenAIChat_TranslatesRequestAndParsesResponse(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"cmpl-1","object":"chat.completion","model":"gpt-4o-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	}))
	defer srv.Close()

	p := NewOpenAI("openai", srv.URL, "sk-test", srv.Client())
	temp := 0.2
	maxTok := 128
	resp, err := p.Chat(context.Background(), "gpt-4o-mini", types.ChatRequest{
		Messages:    []types.Message{{Role: "user", Content: "hey"}},
		Temperature: &temp,
		MaxTokens:   &maxTok,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Outbound translation.
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth = %q, want Bearer sk-test", gotAuth)
	}
	if gotBody["model"] != "gpt-4o-mini" {
		t.Errorf("body.model = %v", gotBody["model"])
	}
	if gotBody["temperature"] != 0.2 {
		t.Errorf("body.temperature = %v, want 0.2", gotBody["temperature"])
	}
	if gotBody["max_tokens"] != float64(128) {
		t.Errorf("body.max_tokens = %v, want 128", gotBody["max_tokens"])
	}

	// Inbound parsing.
	if resp.Provider != "openai" {
		t.Errorf("provider = %q", resp.Provider)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hi" {
		t.Errorf("choices = %+v", resp.Choices)
	}
	if resp.Usage.PromptTokens != 5 || resp.Usage.CompletionTokens != 2 || resp.Usage.TotalTokens != 7 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestOpenAIChat_Non2xxIsTerminalProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer srv.Close()

	p := NewOpenAI("openai", srv.URL, "", srv.Client())
	_, err := p.Chat(context.Background(), "gpt-4o-mini", types.ChatRequest{
		Messages: []types.Message{{Role: "user", Content: "x"}},
	})
	var pe *types.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("want *types.ProviderError, got %T: %v", err, err)
	}
	if pe.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", pe.StatusCode)
	}
	if pe.Retryable() {
		t.Error("401 should be terminal (not retryable)")
	}
}
