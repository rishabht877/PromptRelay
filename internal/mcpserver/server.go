// Package mcpserver exposes PromptRelay to MCP clients over a thin layer: one
// resource and two tools. It holds no routing or caching logic of its own.
//
//   - routes://config    reads the loaded config (env var names only, never values)
//   - compare_prompts    embeds two prompts and applies each route's cache policy
//   - chat               forwards a prompt to the running gateway's HTTP API
//
// A client connects over stdio, so stdout carries the protocol. Everything this
// package logs goes through the *slog.Logger it is given, which must write to stderr.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rishabht877/PromptRelay/internal/config"
	"github.com/rishabht877/PromptRelay/internal/embed"
)

const (
	// ConfigURI is the one resource this server publishes.
	ConfigURI = "routes://config"

	// Limits on the chat tool. It calls paid providers, so inputs are bounded here
	// instead of trusting whatever the model sends.
	MaxPromptChars   = 4000
	MaxTokensCap     = 512
	DefaultMaxTokens = 256
)

// Server wires the config, an embedder and the gateway's address into MCP handlers.
type Server struct {
	cfg        *config.Config
	embedder   embed.Embedder
	gatewayURL string
	httpClient *http.Client
	log        *slog.Logger
}

// New returns a Server. gatewayURL is the base URL of a running PromptRelay
// gateway, e.g. http://localhost:8080.
func New(cfg *config.Config, embedder embed.Embedder, gatewayURL string, httpClient *http.Client, log *slog.Logger) *Server {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 90 * time.Second}
	}
	return &Server{
		cfg:        cfg,
		embedder:   embedder,
		gatewayURL: strings.TrimRight(gatewayURL, "/"),
		httpClient: httpClient,
		log:        log,
	}
}

// MCP builds the protocol server with every tool and resource registered.
func (s *Server) MCP(version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "promptrelay", Version: version}, nil)

	srv.AddResource(&mcp.Resource{
		URI:         ConfigURI,
		Name:        "routes-config",
		Title:       "PromptRelay routes and cache policy",
		Description: "Read-only. Providers (with the NAME of the env var holding each API key, never its value), route fallback chains, and per-route cache policy.",
		MIMEType:    "application/json",
	}, s.readConfig)

	notDestructive := false
	openWorld := true
	closedWorld := false

	mcp.AddTool(srv, &mcp.Tool{
		Name: "compare_prompts",
		Description: "Embed two prompts and report their cosine similarity, plus whether each route's cache " +
			"would reuse one prompt's answer for the other. This is a prediction from the config: it does " +
			"not read the cache or call any model. Similarity measures topic closeness, not meaning, so " +
			"a high score does not mean the answers are the same (e.g. 'miles to km' vs 'km to miles').",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Compare two prompts",
			ReadOnlyHint:    true,
			DestructiveHint: &notDestructive,
			IdempotentHint:  true,
			OpenWorldHint:   &closedWorld,
		},
	}, s.comparePrompts)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "chat",
		Description: fmt.Sprintf("Send one prompt through a PromptRelay route and return the answer. This calls a "+
			"paid model provider (unless the gateway cache answers it), so use it sparingly. "+
			"Prompt is limited to %d characters and max_tokens to %d.", MaxPromptChars, MaxTokensCap),
		Annotations: &mcp.ToolAnnotations{
			Title:           "Chat through a route",
			ReadOnlyHint:    false,
			DestructiveHint: &notDestructive,
			IdempotentHint:  false,
			OpenWorldHint:   &openWorld,
		},
	}, s.chat)

	return srv
}

// routeNames returns the configured route names in a stable order.
func (s *Server) routeNames() []string {
	names := make([]string, 0, len(s.cfg.Routes))
	for n := range s.cfg.Routes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// --- routes://config -------------------------------------------------------

type configView struct {
	Providers map[string]providerView `json:"providers"`
	Routes    map[string]routeView    `json:"routes"`
}

type providerView struct {
	Type      string `json:"type"`
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env,omitempty"` // the variable's name, never its value
}

type routeView struct {
	Fallback []targetView `json:"fallback"`
	Cache    cacheView    `json:"cache"`
}

type targetView struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type cacheView struct {
	Enabled        bool    `json:"enabled"`
	Semantic       bool    `json:"semantic"`
	Threshold      float64 `json:"threshold,omitempty"`
	TTL            string  `json:"ttl,omitempty"`
	MaxTemperature float64 `json:"max_temperature"`
}

func (s *Server) buildConfigView() configView {
	v := configView{
		Providers: make(map[string]providerView, len(s.cfg.Providers)),
		Routes:    make(map[string]routeView, len(s.cfg.Routes)),
	}
	for name, p := range s.cfg.Providers {
		v.Providers[name] = providerView{Type: p.Type, BaseURL: stripUserinfo(p.BaseURL), APIKeyEnv: p.APIKeyEnv}
	}
	for name, r := range s.cfg.Routes {
		rv := routeView{Cache: cacheView{
			Enabled:        r.Cache.Enabled,
			Semantic:       r.Cache.SemanticEnabled(),
			Threshold:      r.Cache.Threshold,
			MaxTemperature: r.Cache.MaxTemperature,
		}}
		if r.Cache.TTL.Duration > 0 {
			rv.Cache.TTL = r.Cache.TTL.String()
		}
		for _, t := range r.Fallback {
			rv.Fallback = append(rv.Fallback, targetView{Provider: t.Provider, Model: t.Model})
		}
		v.Routes[name] = rv
	}
	return v
}

func (s *Server) readConfig(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if req.Params.URI != ConfigURI {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	b, err := json.MarshalIndent(s.buildConfigView(), "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
		{URI: ConfigURI, MIMEType: "application/json", Text: string(b)},
	}}, nil
}

// stripUserinfo drops any user:password@ from a URL so credentials embedded in a
// base_url can never be published through the resource.
func stripUserinfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}
