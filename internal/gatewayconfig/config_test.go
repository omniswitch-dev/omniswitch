package gatewayconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseYAMLConfig(t *testing.T) {
	threshold := 0.88
	data := []byte(`apiVersion: sentinel.dev/v1
kind: GatewayConfig
gateway:
  listen: ":9090"
  data_dir: "./data"
  auth: true
  cache_threshold: 0.88
  cache_ttl: 2h
  shadow_provider: anthropic
providers:
  - name: openai-prod
    type: openai
    api_key_env: OPENAI_PROD_KEY
mcp:
  enabled: true
  policy: policies/mcp.yaml
  upstream: http://127.0.0.1:9000/mcp
routes:
  logical-model:
    max_retries: 2
    variants:
      - name: openai-primary
        provider: openai
        model: gpt-4o-mini
        weight: 90
      - name: anthropic-canary
        provider: anthropic
        model: claude-3-5-haiku-20241022
        weight: 10
  fallback-model:
    provider: openai
    fallbacks:
      - anthropic
`)

	cfg, err := Parse(data, ".yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Gateway.Listen != ":9090" || cfg.Gateway.DataDir != "./data" {
		t.Fatalf("gateway settings = %+v, want listen/data_dir", cfg.Gateway)
	}
	if cfg.Gateway.Auth == nil || !*cfg.Gateway.Auth {
		t.Fatalf("auth = %v, want true", cfg.Gateway.Auth)
	}
	if cfg.Gateway.CacheThreshold == nil || *cfg.Gateway.CacheThreshold != threshold {
		t.Fatalf("cache threshold = %v, want %.2f", cfg.Gateway.CacheThreshold, threshold)
	}
	if cfg.Gateway.CacheTTL == nil || cfg.Gateway.CacheTTL.Duration != 2*time.Hour {
		t.Fatalf("cache ttl = %v, want 2h", cfg.Gateway.CacheTTL)
	}
	if cfg.MCP.Policy != "policies/mcp.yaml" || cfg.MCP.Upstream != "http://127.0.0.1:9000/mcp" {
		t.Fatalf("mcp = %+v, want configured values", cfg.MCP)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "openai-prod" || cfg.Providers[0].APIKeyEnv != "OPENAI_PROD_KEY" {
		t.Fatalf("providers = %+v, want configured provider account", cfg.Providers)
	}
	route := cfg.Routes["logical-model"]
	if route.MaxRetries != 2 || len(route.Variants) != 2 || route.Variants[0].Weight != 90 {
		t.Fatalf("route = %+v, want weighted variants", route)
	}
	if cfg.Routes["fallback-model"].Fallbacks[0] != "anthropic" {
		t.Fatalf("fallback route = %+v, want anthropic fallback", cfg.Routes["fallback-model"])
	}
}

func TestParseJSONConfig(t *testing.T) {
	data := []byte(`{
		"apiVersion": "sentinel.dev/v1",
		"kind": "GatewayConfig",
		"gateway": {"cache_ttl": "30m"},
		"routes": {"logical": {"provider": "openai"}}
	}`)
	cfg, err := Parse(data, ".json")
	if err != nil {
		t.Fatalf("Parse(json) error = %v", err)
	}
	if cfg.Gateway.CacheTTL == nil || cfg.Gateway.CacheTTL.Duration != 30*time.Minute {
		t.Fatalf("json cache ttl = %v, want 30m", cfg.Gateway.CacheTTL)
	}
	if cfg.Routes["logical"].Provider != "openai" {
		t.Fatalf("json route = %+v, want openai", cfg.Routes["logical"])
	}
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte(`routes: {logical: {provider: openai}}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if cfg.Routes["logical"].Provider != "openai" {
		t.Fatalf("route = %+v, want openai", cfg.Routes["logical"])
	}
}

func TestValidateRejectsInvalidRoute(t *testing.T) {
	_, err := Parse([]byte(`routes: {logical: {variants: [{provider: openai, weight: 0}]}}`), ".yaml")
	if err == nil || !strings.Contains(err.Error(), "weight must be positive") {
		t.Fatalf("Parse() error = %v, want weight validation", err)
	}
}

func TestValidateRejectsInvalidCacheThreshold(t *testing.T) {
	_, err := Parse([]byte(`gateway: {cache_threshold: 1.5}`), ".yaml")
	if err == nil || !strings.Contains(err.Error(), "cache_threshold") {
		t.Fatalf("Parse() error = %v, want cache threshold validation", err)
	}
}
