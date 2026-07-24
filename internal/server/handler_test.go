package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rishabht877/PromptRelay/internal/config"
	"github.com/rishabht877/PromptRelay/internal/providers"
	"github.com/rishabht877/PromptRelay/internal/router"
	"github.com/rishabht877/PromptRelay/internal/types"
)

type stubProvider struct {
	fn func() (types.ChatResponse, error)
}

func (s *stubProvider) Name() string { return "p1" }
func (s *stubProvider) Chat(context.Context, string, types.ChatRequest) (types.ChatResponse, error) {
	return s.fn()
}

func testHandler(fn func() (types.ChatResponse, error)) *Handler {
	reg := providers.Registry{"p1": &stubProvider{fn: fn}}
	routes := map[string]config.Route{
		"default": {Fallback: []config.Target{{Provider: "p1", Model: "m1"}}},
	}
	rtr := router.New(reg, routes)
	// nil cache + nil embedder => plain proxy path.
	return NewHandler(rtr, nil, nil, nil, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func do(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ChatCompletions(rec, req)
	return rec
}

func TestChat_OK(t *testing.T) {
	h := testHandler(func() (types.ChatResponse, error) {
		return types.ChatResponse{
			ID:       "resp-1",
			Provider: "p1",
			Model:    "m1",
			Choices:  []types.Choice{{Message: types.Message{Role: "assistant", Content: "hi there"}}},
			Usage:    types.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		}, nil
	})
	rec := do(h, `{"model":"default","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out types.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "hi there" {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestChat_RejectsStreaming(t *testing.T) {
	h := testHandler(func() (types.ChatResponse, error) { return types.ChatResponse{}, nil })
	rec := do(h, `{"model":"default","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for streaming, got %d", rec.Code)
	}
}

func TestChat_UnknownModel(t *testing.T) {
	h := testHandler(func() (types.ChatResponse, error) { return types.ChatResponse{}, nil })
	rec := do(h, `{"model":"ghost","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown model, got %d", rec.Code)
	}
}

func TestChat_MalformedJSON(t *testing.T) {
	h := testHandler(func() (types.ChatResponse, error) { return types.ChatResponse{}, nil })
	rec := do(h, `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed JSON, got %d", rec.Code)
	}
}

func TestChat_MissingFields(t *testing.T) {
	h := testHandler(func() (types.ChatResponse, error) { return types.ChatResponse{}, nil })
	rec := do(h, `{"model":"default"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing messages, got %d", rec.Code)
	}
}
