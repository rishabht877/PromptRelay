package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rishabht877/PromptRelay/internal/types"
)

// OpenAI adapts the OpenAI Chat Completions API. Because the gateway's canonical
// schema is OpenAI-shaped, this adapter is close to a pass-through.
type OpenAI struct {
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewOpenAI(name, baseURL, apiKey string, client *http.Client) *OpenAI {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &OpenAI{name: name, baseURL: baseURL, apiKey: apiKey, client: client}
}

func (o *OpenAI) Name() string { return o.name }

func (o *OpenAI) Chat(ctx context.Context, model string, req types.ChatRequest) (types.ChatResponse, error) {
	body := map[string]any{
		"model":    model,
		"messages": req.Messages,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		body["stop"] = req.Stop
	}
	if req.User != "" {
		body["user"] = req.User
	}

	status, data, err := postJSON(ctx, o.client, o.baseURL+"/chat/completions",
		map[string]string{"Authorization": "Bearer " + o.apiKey}, body)
	if err != nil {
		return types.ChatResponse{}, &types.ProviderError{Provider: o.name, Message: err.Error(), Err: err}
	}
	if status < 200 || status >= 300 {
		return types.ChatResponse{}, &types.ProviderError{Provider: o.name, StatusCode: status, Message: truncate(data, 512)}
	}

	var out types.ChatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return types.ChatResponse{}, &types.ProviderError{Provider: o.name, Message: "decode response: " + err.Error(), Err: err}
	}
	out.Provider = o.name
	return out, nil
}
