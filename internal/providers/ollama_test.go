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

func TestOllamaChat_TranslatesOptionsAndParsesUsage(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"model":"llama3.2","message":{"role":"assistant","content":"2 + 2 = 4"},
			"done":true,"done_reason":"stop","prompt_eval_count":12,"eval_count":5}`)
	}))
	defer srv.Close()

	p := NewOllama("ollama", srv.URL, srv.Client())
	temp := 0.2
	maxTok := 64
	resp, err := p.Chat(context.Background(), "llama3.2", types.ChatRequest{
		Messages:    []types.Message{{Role: "user", Content: "what is 2+2"}},
		Temperature: &temp,
		MaxTokens:   &maxTok,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Outbound translation: non-streaming, params mapped into `options`.
	if gotPath != "/api/chat" {
		t.Errorf("path = %q, want /api/chat", gotPath)
	}
	if gotBody["stream"] != false {
		t.Errorf("body.stream = %v, want false", gotBody["stream"])
	}
	opts, ok := gotBody["options"].(map[string]any)
	if !ok {
		t.Fatalf("body.options missing/mistyped: %v", gotBody["options"])
	}
	if opts["temperature"] != 0.2 {
		t.Errorf("options.temperature = %v, want 0.2", opts["temperature"])
	}
	if opts["num_predict"] != float64(64) {
		t.Errorf("options.num_predict = %v, want 64 (from max_tokens)", opts["num_predict"])
	}

	// Inbound parsing: usage from prompt_eval_count / eval_count.
	if resp.Choices[0].Message.Content != "2 + 2 = 4" {
		t.Errorf("content = %q", resp.Choices[0].Message.Content)
	}
	if resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 5 || resp.Usage.TotalTokens != 17 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Provider != "ollama" {
		t.Errorf("provider = %q", resp.Provider)
	}
}
