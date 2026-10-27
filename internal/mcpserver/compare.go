package mcpserver

import (
	"context"
	"fmt"
	"math"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rishabht877/PromptRelay/internal/cache"
	"github.com/rishabht877/PromptRelay/internal/types"
)

type compareInput struct {
	PromptA string `json:"prompt_a" jsonschema:"the first prompt"`
	PromptB string `json:"prompt_b" jsonschema:"the second prompt"`
	Route   string `json:"route,omitempty" jsonschema:"a single route to check; omit to check every route"`
}

type compareOutput struct {
	Similarity float64        `json:"similarity" jsonschema:"cosine similarity of the two embedded prompts, -1 to 1"`
	ExactMatch bool           `json:"exact_match" jsonschema:"true if the prompts are identical after the gateway's normalization"`
	Routes     []routeVerdict `json:"routes"`
}

type routeVerdict struct {
	Route      string  `json:"route"`
	WouldReuse bool    `json:"would_reuse"`
	Reason     string  `json:"reason"`
	Threshold  float64 `json:"threshold,omitempty"`
}

func (s *Server) comparePrompts(ctx context.Context, _ *mcp.CallToolRequest, in compareInput) (*mcp.CallToolResult, compareOutput, error) {
	if err := checkPrompt("prompt_a", in.PromptA); err != nil {
		return nil, compareOutput{}, err
	}
	if err := checkPrompt("prompt_b", in.PromptB); err != nil {
		return nil, compareOutput{}, err
	}
	routes := s.routeNames()
	if in.Route != "" {
		if _, ok := s.cfg.Routes[in.Route]; !ok {
			return nil, compareOutput{}, fmt.Errorf("unknown route %q; configured routes: %v", in.Route, routes)
		}
		routes = []string{in.Route}
	}

	// Embed exactly what the gateway embeds: the normalized, role-prefixed text.
	na := cache.Normalize([]types.Message{{Role: "user", Content: in.PromptA}})
	nb := cache.Normalize([]types.Message{{Role: "user", Content: in.PromptB}})
	ea, err := s.embedder.Embed(ctx, na)
	if err != nil {
		return nil, compareOutput{}, fmt.Errorf("embedding prompt_a: %w", err)
	}
	eb, err := s.embedder.Embed(ctx, nb)
	if err != nil {
		return nil, compareOutput{}, fmt.Errorf("embedding prompt_b: %w", err)
	}

	out := compareOutput{Similarity: round4(cosine(ea, eb)), ExactMatch: na == nb}
	for _, name := range routes {
		out.Routes = append(out.Routes, s.verdict(name, out.Similarity, out.ExactMatch))
	}
	return nil, out, nil
}

// verdict applies one route's cache policy to the measurement. It mirrors the order
// the gateway uses: opt-in, then exact hash, then (only if allowed) similarity.
func (s *Server) verdict(route string, sim float64, exact bool) routeVerdict {
	c := s.cfg.Routes[route].Cache
	v := routeVerdict{Route: route}
	switch {
	case !c.Enabled:
		v.Reason = "caching is off for this route"
	case exact:
		v.WouldReuse = true
		v.Reason = "exact-hash match (only when temperature <= max_temperature and sampling params match)"
	case !c.SemanticEnabled():
		v.Reason = "exact-only route: a different prompt never reuses an answer"
	case sim >= c.Threshold:
		v.WouldReuse, v.Threshold = true, c.Threshold
		v.Reason = "similarity is at or above this route's threshold; the answer would be reused even if the prompts mean different things"
	default:
		v.Threshold = c.Threshold
		v.Reason = "similarity is below this route's threshold"
	}
	return v
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func round4(f float64) float64 { return math.Round(f*1e4) / 1e4 }

func checkPrompt(field, v string) error {
	if len(v) == 0 {
		return fmt.Errorf("%s is required", field)
	}
	if len([]rune(v)) > MaxPromptChars {
		return fmt.Errorf("%s is %d characters; the limit is %d", field, len([]rune(v)), MaxPromptChars)
	}
	return nil
}
