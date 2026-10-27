// Command promptrelay-mcp is a small MCP server (stdio) over PromptRelay. It is a
// learning project: it needs a running gateway for the chat tool and a local Ollama
// for compare_prompts.
//
// stdout belongs to the MCP protocol. Every log line goes to stderr.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rishabht877/PromptRelay/internal/config"
	"github.com/rishabht877/PromptRelay/internal/embed"
	"github.com/rishabht877/PromptRelay/internal/mcpserver"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("promptrelay-mcp exited", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfgPath := os.Getenv("PROMPTRELAY_CONFIG")
	if cfgPath == "" {
		cfgPath = "configs/config.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	gatewayURL := os.Getenv("PROMPTRELAY_URL")
	if gatewayURL == "" {
		gatewayURL = "http://localhost" + cfg.Server.Addr // Addr is ":8080" by default
	}

	embedder := embed.NewOllama(cfg.Embedding.BaseURL, cfg.Embedding.Model, cfg.Embedding.Dimension,
		&http.Client{Timeout: 30 * time.Second})
	srv := mcpserver.New(cfg, embedder, gatewayURL, nil, log).MCP("0.1.0")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	log.Info("promptrelay-mcp ready", "config", cfgPath, "gateway", gatewayURL)
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}
