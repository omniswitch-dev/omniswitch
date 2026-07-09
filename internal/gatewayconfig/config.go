package gatewayconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sentinel/internal/router"

	"go.yaml.in/yaml/v3"
)

const (
	APIVersion = "sentinel.dev/v1"
	Kind       = "GatewayConfig"
)

type Config struct {
	APIVersion string                  `json:"apiVersion" yaml:"apiVersion"`
	Kind       string                  `json:"kind" yaml:"kind"`
	Gateway    Gateway                 `json:"gateway,omitempty" yaml:"gateway,omitempty"`
	Providers  []ProviderAccount       `json:"providers,omitempty" yaml:"providers,omitempty"`
	MCP        MCP                     `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	Routes     map[string]router.Route `json:"routes,omitempty" yaml:"routes,omitempty"`
}

type Gateway struct {
	Listen         string    `json:"listen,omitempty" yaml:"listen,omitempty"`
	DataDir        string    `json:"data_dir,omitempty" yaml:"data_dir,omitempty"`
	Auth           *bool     `json:"auth,omitempty" yaml:"auth,omitempty"`
	CacheThreshold *float64  `json:"cache_threshold,omitempty" yaml:"cache_threshold,omitempty"`
	CacheTTL       *Duration `json:"cache_ttl,omitempty" yaml:"cache_ttl,omitempty"`
	ShadowProvider string    `json:"shadow_provider,omitempty" yaml:"shadow_provider,omitempty"`
}

type MCP struct {
	Enabled  *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Policy   string `json:"policy,omitempty" yaml:"policy,omitempty"`
	Upstream string `json:"upstream,omitempty" yaml:"upstream,omitempty"`
}

type ProviderAccount struct {
	Name      string `json:"name" yaml:"name"`
	Type      string `json:"type" yaml:"type"`
	APIKeyEnv string `json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`
}

type Duration struct {
	time.Duration
}

func LoadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read gateway config: %w", err)
	}
	cfg, err := Parse(data, filepath.Ext(path))
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Parse(data []byte, ext string) (Config, error) {
	var cfg Config
	trimmed := bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	switch strings.ToLower(ext) {
	case ".json":
		if err := json.Unmarshal(trimmed, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse gateway config json: %w", err)
		}
	default:
		if err := yaml.Unmarshal(trimmed, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse gateway config yaml: %w", err)
		}
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Validate(cfg Config) error {
	if cfg.APIVersion != "" && cfg.APIVersion != APIVersion {
		return fmt.Errorf("unsupported apiVersion %q", cfg.APIVersion)
	}
	if cfg.Kind != "" && cfg.Kind != Kind {
		return fmt.Errorf("unsupported kind %q", cfg.Kind)
	}
	if cfg.Gateway.CacheThreshold != nil && (*cfg.Gateway.CacheThreshold < 0 || *cfg.Gateway.CacheThreshold > 1) {
		return fmt.Errorf("gateway.cache_threshold must be between 0 and 1")
	}
	if cfg.Gateway.CacheTTL != nil && cfg.Gateway.CacheTTL.Duration < 0 {
		return fmt.Errorf("gateway.cache_ttl must be non-negative")
	}
	for i, account := range cfg.Providers {
		if strings.TrimSpace(account.Name) == "" {
			return fmt.Errorf("providers[%d].name is required", i)
		}
		if strings.TrimSpace(account.Type) == "" {
			return fmt.Errorf("providers[%d].type is required", i)
		}
	}
	for model, route := range cfg.Routes {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("route model cannot be empty")
		}
		if route.MaxRetries < 0 {
			return fmt.Errorf("route %q max_retries must be non-negative", model)
		}
		if strings.TrimSpace(route.Provider) == "" && len(route.Variants) == 0 {
			return fmt.Errorf("route %q must set provider or variants", model)
		}
		for i, variant := range route.Variants {
			if strings.TrimSpace(variant.Provider) == "" {
				return fmt.Errorf("route %q variant %d provider is required", model, i)
			}
			if variant.Weight <= 0 {
				return fmt.Errorf("route %q variant %d weight must be positive", model, i)
			}
		}
	}
	return nil
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	parsed, err := parseDurationValue(value.Value)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		parsed, err := parseDurationValue(text)
		if err != nil {
			return err
		}
		d.Duration = parsed
		return nil
	}
	var nanos int64
	if err := json.Unmarshal(data, &nanos); err != nil {
		return fmt.Errorf("duration must be a Go duration string or nanoseconds: %w", err)
	}
	d.Duration = time.Duration(nanos)
	return nil
}

func parseDurationValue(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", value, err)
	}
	return parsed, nil
}
