// Package config loads and validates PromptRelay's YAML configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from a Go duration string ("60s",
// "24h") in YAML.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

// Config is the whole gateway configuration.
type Config struct {
	Server    Server              `yaml:"server"`
	Redis     Redis               `yaml:"redis"`
	Embedding Embedding           `yaml:"embedding"`
	Providers map[string]Provider `yaml:"providers"`
	Routes    map[string]Route    `yaml:"routes"`
	Pricing   map[string]Price    `yaml:"pricing"`
}

type Server struct {
	Addr           string   `yaml:"addr"`
	RequestTimeout Duration `yaml:"request_timeout"`
}

type Redis struct {
	Addr  string `yaml:"addr"`
	Index string `yaml:"index"`
}

// Embedding points at a local Ollama serving the embedding model used for the
// semantic cache. Dimension must match the model's output width (all-minilm = 384).
type Embedding struct {
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	Dimension int    `yaml:"dimension"`
}

// Provider describes one upstream. Type selects the adapter; APIKeyEnv names the
// environment variable holding the key (never the key itself).
type Provider struct {
	Type      string `yaml:"type"` // openai | anthropic | ollama
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
}

// Route maps a logical model name to an ordered fallback chain plus its cache policy.
type Route struct {
	Fallback []Target `yaml:"fallback"`
	Cache    Cache    `yaml:"cache"`
}

type Target struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// Cache is per-route. It is OFF by default; a route must opt in, and even then only
// low-temperature (<= MaxTemperature) requests are eligible.
type Cache struct {
	Enabled        bool     `yaml:"enabled"`
	Threshold      float64  `yaml:"threshold"`
	TTL            Duration `yaml:"ttl"`
	MaxTemperature float64  `yaml:"max_temperature"`
}

type Price struct {
	InputPer1M  float64 `yaml:"input_per_1m"`
	OutputPer1M float64 `yaml:"output_per_1m"`
}

// Load reads, parses, applies defaults to, and validates the config at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.RequestTimeout.Duration == 0 {
		c.Server.RequestTimeout.Duration = 60 * time.Second
	}
	if c.Embedding.Dimension == 0 {
		c.Embedding.Dimension = 384
	}
}

func (c *Config) validate() error {
	if len(c.Providers) == 0 {
		return fmt.Errorf("config: no providers defined")
	}
	if len(c.Routes) == 0 {
		return fmt.Errorf("config: no routes defined")
	}
	for name, r := range c.Routes {
		if len(r.Fallback) == 0 {
			return fmt.Errorf("config: route %q has an empty fallback chain", name)
		}
		for _, t := range r.Fallback {
			if _, ok := c.Providers[t.Provider]; !ok {
				return fmt.Errorf("config: route %q references unknown provider %q", name, t.Provider)
			}
		}
		if r.Cache.Enabled && r.Cache.Threshold <= 0 {
			return fmt.Errorf("config: route %q has caching enabled but no positive threshold", name)
		}
	}
	return nil
}
