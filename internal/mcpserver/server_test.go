package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rishabht877/PromptRelay/internal/config"
)

// fakeEmbedder returns a preset vector per normalized text, so tests control
// similarity exactly instead of depending on a real model.
type fakeEmbedder struct {
	vecs map[string][]float32
	err  error
}

func (f fakeEmbedder) Dimension() int { return 2 }
func (f fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	if v, ok := f.vecs[text]; ok {
		return v, nil
	}
	return []float32{0, 1}, nil
}

func testConfig() *config.Config {
	off := false
	return &config.Config{
		Providers: map[string]config.Provider{
			"openai": {Type: "openai", BaseURL: "https://user:hunter2@api.example.com/v1", APIKeyEnv: "TEST_OPENAI_KEY"},
			"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
		},
		Routes: map[string]config.Route{
			"off": {Fallback: []config.Target{{Provider: "ollama", Model: "m"}}},
			"exact": {
				Fallback: []config.Target{{Provider: "openai", Model: "gpt"}, {Provider: "ollama", Model: "m"}},
				Cache:    config.Cache{Enabled: true, Semantic: &off},
			},
			"faq": {
				Fallback: []config.Target{{Provider: "ollama", Model: "m"}},
				Cache:    config.Cache{Enabled: true, Threshold: 0.9, TTL: config.Duration{Duration: time.Hour}},
			},
		},
	}
}

// connect starts the MCP server and returns a real client session wired to it.
func connect(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := s.MCP("test").Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func newServer(emb fakeEmbedder, gatewayURL string) *Server {
	return New(testConfig(), emb, gatewayURL, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("protocol error calling %s: %v", tool, err)
	}
	return res
}

func decode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return out
}

func errText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestToolAnnotations(t *testing.T) {
	cs := connect(t, newServer(fakeEmbedder{}, "http://unused"))
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		by[tl.Name] = tl
	}
	if len(by) != 2 {
		t.Fatalf("want exactly 2 tools, got %d", len(by))
	}
	if a := by["compare_prompts"].Annotations; !a.ReadOnlyHint {
		t.Error("compare_prompts must be marked read-only")
	}
	c := by["chat"].Annotations
	if c.ReadOnlyHint {
		t.Error("chat must NOT be marked read-only: it calls paid providers")
	}
	if c.OpenWorldHint == nil || !*c.OpenWorldHint {
		t.Error("chat should be marked open-world (talks to external providers)")
	}
}

func TestConfigResource_ShowsEnvNamesNeverValues(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY", "sk-super-secret-value")
	cs := connect(t, newServer(fakeEmbedder{}, "http://unused"))

	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: ConfigURI})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Contents[0].Text
	if !strings.Contains(text, "TEST_OPENAI_KEY") {
		t.Error("expected the env var NAME in the resource")
	}
	for _, leaked := range []string{"sk-super-secret-value", "hunter2"} {
		if strings.Contains(text, leaked) {
			t.Errorf("resource leaked %q", leaked)
		}
	}
	var v configView
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatal(err)
	}
	if got := v.Routes["faq"].Cache; !got.Enabled || !got.Semantic || got.Threshold != 0.9 || got.TTL != "1h0m0s" {
		t.Errorf("faq cache view wrong: %+v", got)
	}
	if v.Routes["exact"].Cache.Semantic {
		t.Error("exact route should report semantic=false")
	}
	if len(v.Routes["exact"].Fallback) != 2 || v.Routes["exact"].Fallback[0].Provider != "openai" {
		t.Errorf("fallback chain wrong: %+v", v.Routes["exact"].Fallback)
	}

	if _, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "routes://nope"}); err == nil {
		t.Error("unknown resource URI should be an error")
	}
}

func TestComparePrompts(t *testing.T) {
	norm := func(s string) string { return "user: " + s }
	emb := fakeEmbedder{vecs: map[string][]float32{
		norm("a"):     {1, 0},
		norm("close"): {0.95, 0.31225}, // cosine ~0.95 with "a"
		norm("far"):   {0, 1},
	}}
	cs := connect(t, newServer(emb, "http://unused"))

	t.Run("near but different: semantic route reuses, exact-only and off do not", func(t *testing.T) {
		out := decode[compareOutput](t, call(t, cs, "compare_prompts", map[string]any{"prompt_a": "a", "prompt_b": "close"}))
		if out.Similarity < 0.949 || out.Similarity > 0.951 || out.ExactMatch {
			t.Fatalf("similarity/exact wrong: %+v", out)
		}
		got := map[string]routeVerdict{}
		for _, r := range out.Routes {
			got[r.Route] = r
		}
		if len(got) != 3 {
			t.Fatalf("want all 3 routes, got %d", len(got))
		}
		if !got["faq"].WouldReuse {
			t.Errorf("faq (threshold 0.9) should reuse at 0.95: %+v", got["faq"])
		}
		if got["exact"].WouldReuse || got["off"].WouldReuse {
			t.Errorf("exact-only and off routes must not reuse a merely similar prompt: %+v %+v", got["exact"], got["off"])
		}
	})

	t.Run("below threshold", func(t *testing.T) {
		out := decode[compareOutput](t, call(t, cs, "compare_prompts", map[string]any{"prompt_a": "a", "prompt_b": "far", "route": "faq"}))
		if len(out.Routes) != 1 || out.Routes[0].WouldReuse {
			t.Errorf("want one non-reusing verdict, got %+v", out.Routes)
		}
	})

	t.Run("identical after normalization is an exact match on cache-enabled routes only", func(t *testing.T) {
		out := decode[compareOutput](t, call(t, cs, "compare_prompts", map[string]any{"prompt_a": "a", "prompt_b": "  a  "}))
		if !out.ExactMatch {
			t.Fatal("whitespace-only difference should be an exact match")
		}
		for _, r := range out.Routes {
			want := r.Route != "off"
			if r.WouldReuse != want {
				t.Errorf("route %s: WouldReuse=%v, want %v", r.Route, r.WouldReuse, want)
			}
		}
	})

	t.Run("bad input becomes a tool error, not a crash", func(t *testing.T) {
		cases := map[string]map[string]any{
			"unknown route": {"prompt_a": "a", "prompt_b": "b", "route": "nope"},
			"empty prompt":  {"prompt_a": "", "prompt_b": "b"},
			"too long":      {"prompt_a": strings.Repeat("x", MaxPromptChars+1), "prompt_b": "b"},
		}
		for name, args := range cases {
			res := call(t, cs, "compare_prompts", args)
			if !res.IsError {
				t.Errorf("%s: want IsError", name)
			}
		}
	})

	t.Run("embedder failure becomes a tool error", func(t *testing.T) {
		bad := connect(t, newServer(fakeEmbedder{err: errors.New("ollama down")}, "http://unused"))
		res := call(t, bad, "compare_prompts", map[string]any{"prompt_a": "a", "prompt_b": "b"})
		if !res.IsError || !strings.Contains(errText(res), "ollama down") {
			t.Errorf("want IsError mentioning the cause, got %q", errText(res))
		}
	})
}

func TestCosine(t *testing.T) {
	if got := cosine([]float32{1, 0}, []float32{1, 0}); got < 0.9999 {
		t.Errorf("identical vectors: %v", got)
	}
	if got := cosine([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Errorf("orthogonal vectors: %v", got)
	}
	if got := cosine([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("zero vector must not produce NaN: %v", got)
	}
}

// gateway is a stand-in for a running PromptRelay gateway. It records the request
// it received so tests can check exactly what chat sent.
func gateway(t *testing.T, status int, body string, calls *atomic.Int32, got *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(got)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const okBody = `{"model":"claude-haiku-4-5","choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`

func TestChat_Success_SendsExpectedRequest(t *testing.T) {
	var calls atomic.Int32
	var got map[string]any
	gw := gateway(t, 200, okBody, &calls, &got)
	cs := connect(t, newServer(fakeEmbedder{}, gw.URL))

	out := decode[chatOutput](t, call(t, cs, "chat", map[string]any{"route": "faq", "prompt": "ping"}))
	if out.Answer != "pong" || out.Model != "claude-haiku-4-5" || out.FinishReason != "stop" ||
		out.PromptTokens != 7 || out.CompletionTokens != 2 {
		t.Errorf("output wrong: %+v", out)
	}
	if got["model"] != "faq" {
		t.Errorf("route must be sent as the model field, got %v", got["model"])
	}
	if got["max_tokens"] != float64(DefaultMaxTokens) {
		t.Errorf("default max_tokens: got %v", got["max_tokens"])
	}
	if got["temperature"] != float64(0) {
		t.Errorf("default temperature must be 0 (cache-eligible), got %v", got["temperature"])
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "ping" || msgs[0].(map[string]any)["role"] != "user" {
		t.Errorf("messages wrong: %v", msgs)
	}
}

func TestChat_RespectsCallerTemperatureAndMaxTokens(t *testing.T) {
	var calls atomic.Int32
	var got map[string]any
	gw := gateway(t, 200, okBody, &calls, &got)
	cs := connect(t, newServer(fakeEmbedder{}, gw.URL))
	call(t, cs, "chat", map[string]any{"route": "faq", "prompt": "x", "max_tokens": MaxTokensCap, "temperature": 0.7})
	if got["max_tokens"] != float64(MaxTokensCap) || got["temperature"] != 0.7 {
		t.Errorf("got max_tokens=%v temperature=%v", got["max_tokens"], got["temperature"])
	}
}

func TestChat_RejectsBadInputWithoutCallingTheGateway(t *testing.T) {
	var calls atomic.Int32
	var got map[string]any
	gw := gateway(t, 200, okBody, &calls, &got)
	cs := connect(t, newServer(fakeEmbedder{}, gw.URL))

	cases := map[string]map[string]any{
		"unknown route":       {"route": "nope", "prompt": "hi"},
		"empty prompt":        {"route": "faq", "prompt": ""},
		"prompt over cap":     {"route": "faq", "prompt": strings.Repeat("é", MaxPromptChars+1)},
		"max_tokens over cap": {"route": "faq", "prompt": "hi", "max_tokens": MaxTokensCap + 1},
		"negative max_tokens": {"route": "faq", "prompt": "hi", "max_tokens": -5},
	}
	for name, args := range cases {
		if res := call(t, cs, "chat", args); !res.IsError {
			t.Errorf("%s: want IsError, got success", name)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("a rejected request must never reach a paid provider; gateway saw %d calls", calls.Load())
	}
	// The cap is on characters, not bytes: exactly MaxPromptChars of a 2-byte rune is fine.
	if res := call(t, cs, "chat", map[string]any{"route": "faq", "prompt": strings.Repeat("é", MaxPromptChars)}); res.IsError {
		t.Errorf("prompt at the limit should pass: %s", errText(res))
	}
}

func TestChat_GatewayFailuresAreToolErrors(t *testing.T) {
	var calls atomic.Int32
	var got map[string]any
	gw := gateway(t, 502, `{"error":{"message":"all providers failed"}}`, &calls, &got)
	cs := connect(t, newServer(fakeEmbedder{}, gw.URL))
	res := call(t, cs, "chat", map[string]any{"route": "faq", "prompt": "hi"})
	if !res.IsError || !strings.Contains(errText(res), "502") || !strings.Contains(errText(res), "all providers failed") {
		t.Errorf("want IsError with status and body, got %q", errText(res))
	}

	empty := gateway(t, 200, `{"choices":[]}`, &calls, &got)
	cs2 := connect(t, newServer(fakeEmbedder{}, empty.URL))
	if res := call(t, cs2, "chat", map[string]any{"route": "faq", "prompt": "hi"}); !res.IsError {
		t.Error("a 200 with no choices should be an error")
	}

	down := httptest.NewServer(http.NotFoundHandler())
	url := down.URL
	down.Close()
	cs3 := connect(t, newServer(fakeEmbedder{}, url))
	res = call(t, cs3, "chat", map[string]any{"route": "faq", "prompt": "hi"})
	if !res.IsError || !strings.Contains(errText(res), "is it running") {
		t.Errorf("unreachable gateway: want a helpful IsError, got %q", errText(res))
	}
}
