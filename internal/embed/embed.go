// Package embed produces sentence embeddings for the semantic cache.
//
// The embedding model (all-minilm = all-MiniLM-L6-v2, 384-dim) is served locally by
// the same Ollama instance that acts as a fallback chat provider. Running it locally
// is deliberate: calling a hosted embedding API on every cache lookup would add cost
// and latency to the exact path the cache exists to make cheap and fast.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Embedder turns text into a vector.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Dimension() int
}

// Ollama calls Ollama's /api/embeddings endpoint.
type Ollama struct {
	baseURL string
	model   string
	dim     int
	client  *http.Client
}

func NewOllama(baseURL, model string, dim int, client *http.Client) *Ollama {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Ollama{baseURL: baseURL, model: model, dim: dim, client: client}
}

func (o *Ollama) Dimension() int { return o.dim }

func (o *Ollama) Embed(ctx context.Context, text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": o.model, "prompt": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embed: http %d: %s", resp.StatusCode, string(data))
	}

	var out struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("embed decode: %w", err)
	}
	if len(out.Embedding) == 0 {
		return nil, fmt.Errorf("embed: empty vector (is model %q pulled?)", o.model)
	}
	if len(out.Embedding) != o.dim {
		// A dimension mismatch means config and model disagree; the Redis vector
		// index was created for o.dim and a search with the wrong width will fail.
		return nil, fmt.Errorf("embed: model returned %d dims, config expects %d", len(out.Embedding), o.dim)
	}
	return out.Embedding, nil
}
