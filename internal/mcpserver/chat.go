package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rishabht877/PromptRelay/internal/types"
)

type chatInput struct {
	Route       string   `json:"route" jsonschema:"the route (logical model name) to use; read routes://config for the list"`
	Prompt      string   `json:"prompt" jsonschema:"the user message to send"`
	MaxTokens   int      `json:"max_tokens,omitempty" jsonschema:"maximum tokens in the answer; default 256, at most 512"`
	Temperature *float64 `json:"temperature,omitempty" jsonschema:"sampling temperature; default 0, which is also what makes a route's cache usable"`
}

type chatOutput struct {
	Answer           string `json:"answer"`
	Model            string `json:"model" jsonschema:"the concrete model that answered"`
	FinishReason     string `json:"finish_reason"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	LatencyMS        int64  `json:"latency_ms" jsonschema:"round trip to the gateway; a few milliseconds usually means a cache hit"`
}

func (s *Server) chat(ctx context.Context, _ *mcp.CallToolRequest, in chatInput) (*mcp.CallToolResult, chatOutput, error) {
	if _, ok := s.cfg.Routes[in.Route]; !ok {
		return nil, chatOutput{}, fmt.Errorf("unknown route %q; configured routes: %v", in.Route, s.routeNames())
	}
	if err := checkPrompt("prompt", in.Prompt); err != nil {
		return nil, chatOutput{}, err
	}
	maxTokens := in.MaxTokens
	switch {
	case maxTokens == 0:
		maxTokens = DefaultMaxTokens
	case maxTokens < 0 || maxTokens > MaxTokensCap:
		return nil, chatOutput{}, fmt.Errorf("max_tokens is %d; it must be between 1 and %d", maxTokens, MaxTokensCap)
	}
	temp := 0.0
	if in.Temperature != nil {
		temp = *in.Temperature
	}

	body, err := json.Marshal(types.ChatRequest{
		Model:       in.Route,
		Messages:    []types.Message{{Role: "user", Content: in.Prompt}},
		Temperature: &temp,
		MaxTokens:   &maxTokens,
	})
	if err != nil {
		return nil, chatOutput{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.gatewayURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, chatOutput{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, chatOutput{}, fmt.Errorf("could not reach the gateway at %s (is it running?): %w", s.gatewayURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, chatOutput{}, err
	}
	latency := time.Since(start)
	s.log.Info("chat", "route", in.Route, "status", resp.StatusCode, "latency_ms", latency.Milliseconds())

	if resp.StatusCode != http.StatusOK {
		return nil, chatOutput{}, fmt.Errorf("gateway returned HTTP %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	var cr types.ChatResponse
	if err := json.Unmarshal(data, &cr); err != nil || len(cr.Choices) == 0 {
		return nil, chatOutput{}, fmt.Errorf("gateway returned an unreadable response: %s", truncate(string(data), 300))
	}
	return nil, chatOutput{
		Answer:           cr.Choices[0].Message.Content,
		Model:            cr.Model,
		FinishReason:     cr.Choices[0].FinishReason,
		PromptTokens:     cr.Usage.PromptTokens,
		CompletionTokens: cr.Usage.CompletionTokens,
		LatencyMS:        latency.Milliseconds(),
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
