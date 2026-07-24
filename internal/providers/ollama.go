package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rishabht877/PromptRelay/internal/types"
)

// Ollama adapts a local Ollama server's /api/chat endpoint. No auth. Token usage
// comes from Ollama's prompt_eval_count / eval_count fields.
type Ollama struct {
	name    string
	baseURL string
	client  *http.Client
}

func NewOllama(name, baseURL string, client *http.Client) *Ollama {
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second} // local models can be slow to warm
	}
	return &Ollama{name: name, baseURL: baseURL, client: client}
}

func (o *Ollama) Name() string { return o.name }

func (o *Ollama) Chat(ctx context.Context, model string, req types.ChatRequest) (types.ChatResponse, error) {
	options := map[string]any{}
	if req.Temperature != nil {
		options["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		options["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		options["num_predict"] = *req.MaxTokens
	}
	if len(req.Stop) > 0 {
		options["stop"] = req.Stop
	}

	body := map[string]any{
		"model":    model,
		"messages": req.Messages,
		"stream":   false,
	}
	if len(options) > 0 {
		body["options"] = options
	}

	status, data, err := postJSON(ctx, o.client, o.baseURL+"/api/chat", nil, body)
	if err != nil {
		return types.ChatResponse{}, &types.ProviderError{Provider: o.name, Message: err.Error(), Err: err}
	}
	if status < 200 || status >= 300 {
		return types.ChatResponse{}, &types.ProviderError{Provider: o.name, StatusCode: status, Message: truncate(data, 512)}
	}

	var or struct {
		Model           string        `json:"model"`
		Message         types.Message `json:"message"`
		Done            bool          `json:"done"`
		DoneReason      string        `json:"done_reason"`
		PromptEvalCount int           `json:"prompt_eval_count"`
		EvalCount       int           `json:"eval_count"`
	}
	if err := json.Unmarshal(data, &or); err != nil {
		return types.ChatResponse{}, &types.ProviderError{Provider: o.name, Message: "decode response: " + err.Error(), Err: err}
	}

	finish := "stop"
	if or.DoneReason == "length" {
		finish = "length"
	}

	return types.ChatResponse{
		ID:      "ollama-" + or.Model,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   or.Model,
		Choices: []types.Choice{{
			Index:        0,
			Message:      or.Message,
			FinishReason: finish,
		}},
		Usage: types.Usage{
			PromptTokens:     or.PromptEvalCount,
			CompletionTokens: or.EvalCount,
			TotalTokens:      or.PromptEvalCount + or.EvalCount,
		},
		Provider: o.name,
	}, nil
}
