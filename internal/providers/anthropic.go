package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rishabht877/PromptRelay/internal/types"
)

const (
	anthropicVersion     = "2023-06-01"
	anthropicDefaultTok  = 1024 // Anthropic requires max_tokens; used when caller omits it.
	anthropicMessagePath = "/messages"
)

// Anthropic adapts the Anthropic Messages API. It differs from OpenAI in three ways
// this adapter handles: system prompts live in a top-level `system` field (not in
// messages), `max_tokens` is required, and the response content is an array of
// typed blocks rather than a single string.
type Anthropic struct {
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewAnthropic(name, baseURL, apiKey string, client *http.Client) *Anthropic {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Anthropic{name: name, baseURL: baseURL, apiKey: apiKey, client: client}
}

func (a *Anthropic) Name() string { return a.name }

func (a *Anthropic) Chat(ctx context.Context, model string, req types.ChatRequest) (types.ChatResponse, error) {
	system, msgs := splitSystem(req.Messages)

	maxTokens := anthropicDefaultTok
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}
	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   msgs,
	}
	if system != "" {
		body["system"] = system
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		body["stop_sequences"] = req.Stop
	}

	headers := map[string]string{
		"x-api-key":         a.apiKey,
		"anthropic-version": anthropicVersion,
	}
	status, data, err := postJSON(ctx, a.client, a.baseURL+anthropicMessagePath, headers, body)
	if err != nil {
		return types.ChatResponse{}, &types.ProviderError{Provider: a.name, Message: err.Error(), Err: err}
	}
	if status < 200 || status >= 300 {
		return types.ChatResponse{}, &types.ProviderError{Provider: a.name, StatusCode: status, Message: truncate(data, 512)}
	}

	var ar struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &ar); err != nil {
		return types.ChatResponse{}, &types.ProviderError{Provider: a.name, Message: "decode response: " + err.Error(), Err: err}
	}

	var text strings.Builder
	for _, block := range ar.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}

	return types.ChatResponse{
		ID:      ar.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   ar.Model,
		Choices: []types.Choice{{
			Index:        0,
			Message:      types.Message{Role: "assistant", Content: text.String()},
			FinishReason: mapAnthropicStop(ar.StopReason),
		}},
		Usage: types.Usage{
			PromptTokens:     ar.Usage.InputTokens,
			CompletionTokens: ar.Usage.OutputTokens,
			TotalTokens:      ar.Usage.InputTokens + ar.Usage.OutputTokens,
		},
		Provider: a.name,
	}, nil
}

// splitSystem pulls system messages out into a single concatenated system string
// and returns the remaining user/assistant messages in order.
func splitSystem(in []types.Message) (system string, rest []types.Message) {
	var sys []string
	for _, m := range in {
		if m.Role == "system" {
			sys = append(sys, m.Content)
			continue
		}
		rest = append(rest, m)
	}
	return strings.Join(sys, "\n\n"), rest
}

func mapAnthropicStop(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	default:
		return reason
	}
}
