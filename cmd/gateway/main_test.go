package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"sentinel/internal/gatewayconfig"
	"sentinel/internal/provider"
)

func TestEnvFloat(t *testing.T) {
	t.Setenv("SENTINEL_TEST_FLOAT", "0.83")
	if got := envFloat("SENTINEL_TEST_FLOAT", 0.95); got != 0.83 {
		t.Fatalf("envFloat() = %v, want 0.83", got)
	}
	if got := envFloat("SENTINEL_TEST_MISSING", 0.95); got != 0.95 {
		t.Fatalf("envFloat(missing) = %v, want fallback", got)
	}
}

func TestEnvDuration(t *testing.T) {
	t.Setenv("SENTINEL_TEST_DURATION", "2h")
	if got := envDuration("SENTINEL_TEST_DURATION", 0); got.String() != "2h0m0s" {
		t.Fatalf("envDuration() = %s, want 2h0m0s", got)
	}
}

func TestEnvBool(t *testing.T) {
	t.Setenv("SENTINEL_TEST_BOOL", "true")
	if !envBool("SENTINEL_TEST_BOOL", false) {
		t.Fatalf("envBool() = false, want true")
	}
	if !envBool("SENTINEL_TEST_MISSING_BOOL", true) {
		t.Fatalf("envBool(missing) = false, want fallback")
	}
}

func TestParseABConfig(t *testing.T) {
	routes := parseABConfig("logical=openai:gpt-4o-mini:90,anthropic:claude-3-5-haiku-20241022:10")
	route, ok := routes["logical"]
	if !ok {
		t.Fatalf("logical route missing")
	}
	if len(route.Variants) != 2 {
		t.Fatalf("variants = %+v, want two", route.Variants)
	}
	if route.Variants[0].Provider != "openai" || route.Variants[0].Model != "gpt-4o-mini" || route.Variants[0].Weight != 90 {
		t.Fatalf("first variant = %+v, want openai/gpt-4o-mini/90", route.Variants[0])
	}
}

func TestLoadRuntimeSettingsFromConfig(t *testing.T) {
	clearGatewayEnv(t)
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: sentinel.dev/v1
kind: GatewayConfig
gateway:
  listen: ":9090"
  data_dir: ./sentinel-data
  auth: true
  cache_threshold: 0.72
  cache_ttl: 15m
  shadow_provider: anthropic
mcp:
  enabled: false
  policy: policies/custom.yaml
  upstream: http://127.0.0.1:9000/mcp
providers:
  - name: openai-prod
    type: openai
    api_key_env: OPENAI_PROD_KEY
routes:
  logical:
    provider: openai
    fallbacks: [anthropic]
    max_retries: 2
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("SENTINEL_CONFIG", path)

	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatalf("loadRuntimeSettings() error = %v", err)
	}
	if settings.listenAddr != ":9090" || settings.dataDir != "./sentinel-data" {
		t.Fatalf("settings listen/data = %q/%q, want config values", settings.listenAddr, settings.dataDir)
	}
	if !settings.authEnabled || settings.cacheThreshold != 0.72 || settings.cacheTTL != 15*time.Minute {
		t.Fatalf("settings auth/cache = %v/%.2f/%s, want config values", settings.authEnabled, settings.cacheThreshold, settings.cacheTTL)
	}
	if settings.shadowProvider != "anthropic" || settings.mcpEnabled {
		t.Fatalf("settings shadow/mcp = %q/%v, want anthropic/false", settings.shadowProvider, settings.mcpEnabled)
	}
	if settings.routes["logical"].Provider != "openai" || settings.routes["logical"].Fallbacks[0] != "anthropic" || settings.routes["logical"].MaxRetries != 2 {
		t.Fatalf("route = %+v, want configured fallback route", settings.routes["logical"])
	}
	if len(settings.providerAccounts) != 1 || settings.providerAccounts[0].Name != "openai-prod" {
		t.Fatalf("provider accounts = %+v, want openai-prod", settings.providerAccounts)
	}
}

func TestLoadRuntimeSettingsEnvOverridesConfig(t *testing.T) {
	clearGatewayEnv(t)
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte(`gateway:
  listen: ":9090"
  cache_threshold: 0.72
mcp:
  enabled: false
routes:
  logical:
    provider: openai
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("SENTINEL_CONFIG", path)
	t.Setenv("SENTINEL_LISTEN", ":7070")
	t.Setenv("SENTINEL_CACHE_THRESHOLD", "0.44")
	t.Setenv("SENTINEL_MCP_ENABLED", "true")
	t.Setenv("SENTINEL_AB_TEST", "logical=anthropic:claude-3-5-haiku-20241022:100")

	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatalf("loadRuntimeSettings() error = %v", err)
	}
	if settings.listenAddr != ":7070" || settings.cacheThreshold != 0.44 || !settings.mcpEnabled {
		t.Fatalf("settings = %+v, want env overrides", settings)
	}
	route := settings.routes["logical"]
	if route.Provider != "openai" || len(route.Variants) != 1 || route.Variants[0].Provider != "anthropic" {
		t.Fatalf("route = %+v, want env A/B variants layered onto config route", route)
	}
}

func TestRegisterProviderAccount(t *testing.T) {
	t.Setenv("OPENAI_PROD_KEY", "test-key")
	registry := provider.NewRegistry()
	registerProviderAccount(registry, gatewayconfig.ProviderAccount{
		Name:      "openai-prod",
		Type:      "openai",
		APIKeyEnv: "OPENAI_PROD_KEY",
	})
	if _, err := registry.Get("@openai-prod"); err != nil {
		t.Fatalf("registry.Get(alias) error = %v", err)
	}
	models := registry.AllModels()
	if len(models) == 0 || models[0].Provider != "@openai-prod" {
		t.Fatalf("models = %+v, want alias models", models)
	}
}

func clearGatewayEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"SENTINEL_CONFIG",
		"SENTINEL_LISTEN",
		"SENTINEL_DATA",
		"SENTINEL_AUTH",
		"SENTINEL_CACHE_THRESHOLD",
		"SENTINEL_CACHE_TTL",
		"SENTINEL_SHADOW_PROVIDER",
		"SENTINEL_MCP_ENABLED",
		"SENTINEL_MCP_POLICY",
		"SENTINEL_MCP_UPSTREAM",
		"SENTINEL_AB_TEST",
	} {
		t.Setenv(key, "")
	}
}
