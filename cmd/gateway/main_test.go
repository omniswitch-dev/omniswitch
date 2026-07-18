package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestEnvFloat(t *testing.T) {
	t.Setenv("OMNISWITCH_TEST_FLOAT", "0.83")
	if got := envFloat("OMNISWITCH_TEST_FLOAT", 0.95); got != 0.83 {
		t.Fatalf("envFloat() = %v, want 0.83", got)
	}
	if got := envFloat("OMNISWITCH_TEST_MISSING", 0.95); got != 0.95 {
		t.Fatalf("envFloat(missing) = %v, want fallback", got)
	}
}

func TestEnvDuration(t *testing.T) {
	t.Setenv("OMNISWITCH_TEST_DURATION", "2h")
	if got := envDuration("OMNISWITCH_TEST_DURATION", 0); got.String() != "2h0m0s" {
		t.Fatalf("envDuration() = %s, want 2h0m0s", got)
	}
}

func TestEnvBool(t *testing.T) {
	t.Setenv("OMNISWITCH_TEST_BOOL", "true")
	if !envBool("OMNISWITCH_TEST_BOOL", false) {
		t.Fatalf("envBool() = false, want true")
	}
	if !envBool("OMNISWITCH_TEST_MISSING_BOOL", true) {
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
	if err := os.WriteFile(path, []byte(`apiVersion: omniswitch.dev/v1
kind: GatewayConfig
gateway:
  listen: ":9090"
  data_dir: ./sentinel-data
  auth: true
  cache_threshold: 0.72
  cache_ttl: 15m
  shadow_provider: anthropic
rate_limit:
  requests: 42
  window: 30s
  prefix: test:quota
  fail_open: true
identity:
  oidc:
    jwks_url: https://issuer.example.test/keys
    issuer: https://issuer.example.test/
    audience: omniswitch
    role_claim: roles
    workspace_claim: workspace
    organization_claim: organization
    cache_ttl: 10m
observability:
  otel_enabled: true
  otlp_endpoint: http://localhost:4318/v1/traces
  service_name: sentinel-test
  insecure: true
  timeout: 2s
  headers:
    x-api-key: otel-secret
mcp:
  enabled: false
  policy: policies/custom.yaml
  upstream: http://127.0.0.1:9000/mcp
providers:
  - name: openai-prod
    type: openai
    api_key_env: OPENAI_PROD_KEY
  - name: local-llm
    type: custom
    base_url: http://localhost:11434/v1
    models: [llama3.2]
routes:
  logical:
    provider: openai
    fallbacks: [anthropic]
    max_retries: 2
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("OMNISWITCH_CONFIG", path)

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
	if settings.rateLimitRequests != 42 || settings.rateLimitWindow != 30*time.Second || settings.rateLimitPrefix != "test:quota" || !settings.rateLimitFailOpen {
		t.Fatalf("rate limit settings = %+v, want configured values", settings)
	}
	if !settings.authEnabled || settings.oidcJWKSURL != "https://issuer.example.test/keys" || settings.oidcIssuer != "https://issuer.example.test/" || settings.oidcAudience != "omniswitch" || settings.oidcRoleClaim != "roles" || settings.oidcWorkspaceClaim != "workspace" || settings.oidcOrganizationClaim != "organization" || settings.oidcCacheTTL != 10*time.Minute {
		t.Fatalf("OIDC settings = %+v, want configured identity", settings)
	}
	if !settings.otelEnabled || settings.otelEndpoint != "http://localhost:4318/v1/traces" || settings.otelServiceName != "sentinel-test" || !settings.otelInsecure || settings.otelTimeout != 2*time.Second {
		t.Fatalf("otel settings = %+v, want configured telemetry", settings)
	}
	if settings.otelHeaders["x-api-key"] != "otel-secret" {
		t.Fatalf("otel headers = %+v, want configured header", settings.otelHeaders)
	}
	if settings.routes["logical"].Provider != "openai" || settings.routes["logical"].Fallbacks[0] != "anthropic" || settings.routes["logical"].MaxRetries != 2 {
		t.Fatalf("route = %+v, want configured fallback route", settings.routes["logical"])
	}
	if len(settings.providerAccounts) != 2 || settings.providerAccounts[0].Name != "openai-prod" || settings.providerAccounts[1].BaseURL == "" {
		t.Fatalf("provider accounts = %+v, want built-in and custom accounts", settings.providerAccounts)
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
	t.Setenv("OMNISWITCH_CONFIG", path)
	t.Setenv("OMNISWITCH_LISTEN", ":7070")
	t.Setenv("OMNISWITCH_CACHE_THRESHOLD", "0.44")
	t.Setenv("OMNISWITCH_MCP_ENABLED", "true")
	t.Setenv("OMNISWITCH_RATE_LIMIT_REQUESTS", "80")
	t.Setenv("OMNISWITCH_RATE_LIMIT_WINDOW", "10s")
	t.Setenv("OMNISWITCH_RATE_LIMIT_PREFIX", "override:quota")
	t.Setenv("OMNISWITCH_RATE_LIMIT_FAIL_OPEN", "true")
	t.Setenv("OMNISWITCH_OIDC_JWKS_URL", "https://override.example.test/keys")
	t.Setenv("OMNISWITCH_OIDC_AUDIENCE", "override-audience")
	t.Setenv("OMNISWITCH_AB_TEST", "logical=anthropic:claude-3-5-haiku-20241022:100")

	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatalf("loadRuntimeSettings() error = %v", err)
	}
	if settings.listenAddr != ":7070" || settings.cacheThreshold != 0.44 || !settings.mcpEnabled {
		t.Fatalf("settings = %+v, want env overrides", settings)
	}
	if settings.rateLimitRequests != 80 || settings.rateLimitWindow != 10*time.Second || settings.rateLimitPrefix != "override:quota" || !settings.rateLimitFailOpen {
		t.Fatalf("rate limit settings = %+v, want env overrides", settings)
	}
	if !settings.authEnabled || settings.oidcJWKSURL != "https://override.example.test/keys" || settings.oidcAudience != "override-audience" {
		t.Fatalf("OIDC settings = %+v, want env overrides", settings)
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

func TestRegisterCustomProviderAccountExpandsHeaders(t *testing.T) {
	t.Setenv("CUSTOM_PROVIDER_KEY", "secret-from-env")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "secret-from-env" {
			t.Fatalf("api-key header = %q, want expanded env secret", r.Header.Get("api-key"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("Authorization header = %q, want empty when api-key header is configured", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(provider.ChatResponse{
			ID:      "chat_custom",
			Object:  "chat.completion",
			Created: 1,
			Model:   "gpt-4o",
			Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	registry := provider.NewRegistry()
	registerProviderAccount(registry, gatewayconfig.ProviderAccount{
		Name:      "azure",
		Type:      "custom",
		APIKeyEnv: "CUSTOM_PROVIDER_KEY",
		BaseURL:   server.URL + "/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21",
		Models:    []string{"gpt-4o"},
		ExtraHeaders: map[string]string{
			"api-key": "${CUSTOM_PROVIDER_KEY}",
		},
	})
	registered, err := registry.Get("azure")
	if err != nil {
		t.Fatalf("registry.Get(custom) error = %v", err)
	}
	resp, _, err := registered.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    "gpt-4o",
		Messages: []provider.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.ID != "chat_custom" {
		t.Fatalf("response ID = %q, want chat_custom", resp.ID)
	}
}

func TestRuntimeConfigSummaryRedactsSensitiveConfig(t *testing.T) {
	forwardBearer := true
	settings := runtimeSettings{
		configPath:        "config.yaml",
		listenAddr:        ":8080",
		authEnabled:       true,
		cacheThreshold:    0.95,
		cacheTTL:          time.Hour,
		cacheScope:        "api_key",
		rateLimitRequests: 120,
		rateLimitWindow:   time.Minute,
		rateLimitRedisURL: "redis://redis-secret.example:6379/0",
		rateLimitPrefix:   "omniswitch:ratelimit",
		oidcJWKSURL:       "https://issuer.example/keys",
		oidcIssuer:        "https://issuer.example/",
		oidcAudience:      "omniswitch",
		authorizationRules: []gateway.AuthorizationRule{{
			Name: "secret-rule", Effect: "deny", When: `claims["top_secret"] == "secret-value"`,
		}},
		guardrailConfig: guardrail.Config{
			Actions: map[string]string{"pii": "redact"},
			Rules:   []guardrail.Rule{{Name: "secret-marker", Stage: "output", Pattern: "super-secret-pattern", Action: "deny"}},
			Webhooks: []guardrail.Webhook{{
				Name: "managed", URL: "https://guardrail-secret.example/check", Stage: "input", Action: "deny", Headers: map[string]string{"authorization": "Bearer webhook-secret"}, Timeout: 3 * time.Second,
			}},
		},
		mcpEnabled: true,
		mcpTargets: []gatewayconfig.MCPTarget{{
			Name: "github", Upstream: "https://mcp-secret.example/mcp", Headers: map[string]string{"x-api-key": "target-secret"}, ForwardBearerToken: &forwardBearer,
		}},
		routes: map[string]router.Route{"logical": {Provider: "@openai-prod", Variants: []router.Variant{{Provider: "@openai-prod", Model: "gpt-4o-mini", Weight: 100}}}},
	}
	registry := provider.NewRegistry()

	payload, err := json.Marshal(runtimeConfigSummary(settings, registry))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	body := string(payload)
	for _, forbidden := range []string{
		"webhook-secret",
		"target-secret",
		"mcp-secret.example",
		"guardrail-secret.example",
		"super-secret-pattern",
		"secret-value",
		"redis-secret.example",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("runtime config summary leaked %q in %s", forbidden, body)
		}
	}
	for _, want := range []string{`"backend":"redis"`, `"rule_count":1`, `"webhook_count":1`, `"forward_bearer_token":true`, `"model":"logical"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("runtime config summary = %s, want %s", body, want)
		}
	}
}

func TestParseHeaderListAndExpansion(t *testing.T) {
	t.Setenv("HEADER_SECRET", "resolved")
	headers := parseHeaderList("x-api-key=${HEADER_SECRET},x-team=ai,bad")
	expanded := expandHeaderValues(headers)
	if expanded["x-api-key"] != "resolved" || expanded["x-team"] != "ai" {
		t.Fatalf("expanded headers = %+v, want env and literal values", expanded)
	}
	if _, ok := expanded["bad"]; ok {
		t.Fatalf("expanded headers = %+v, want malformed entry ignored", expanded)
	}
}

func TestEnsureBootstrapKey(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	settings := runtimeSettings{authEnabled: true, bootstrapAPIKey: "sk-sentinel-bootstrap-secret"}
	if err := ensureBootstrapKey(context.Background(), st, settings); err != nil {
		t.Fatalf("ensureBootstrapKey() error = %v", err)
	}
	key, err := st.GetAPIKeyByHash(context.Background(), sha256Hex(settings.bootstrapAPIKey))
	if err != nil {
		t.Fatalf("bootstrap key lookup error = %v", err)
	}
	if key.ID != "bootstrap-admin" || key.Role != "owner" || !key.Enabled {
		t.Fatalf("bootstrap key = %+v, want enabled owner", key)
	}

	empty, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New(empty) error = %v", err)
	}
	defer empty.Close()
	if err := ensureBootstrapKey(context.Background(), empty, runtimeSettings{authEnabled: true}); err == nil {
		t.Fatal("ensureBootstrapKey() error = nil, want missing bootstrap key error")
	}
}

func sha256Hex(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func clearGatewayEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"OMNISWITCH_CONFIG",
		"OMNISWITCH_LISTEN",
		"OMNISWITCH_DATA",
		"OMNISWITCH_AUTH",
		"OMNISWITCH_CACHE_THRESHOLD",
		"OMNISWITCH_CACHE_TTL",
		"OMNISWITCH_CACHE_SCOPE",
		"OMNISWITCH_LOG_PAYLOADS",
		"OMNISWITCH_CORS_ORIGINS",
		"OMNISWITCH_GUARDRAIL_STREAM_BUFFER",
		"OMNISWITCH_MAX_REQUEST_BYTES",
		"OMNISWITCH_READ_HEADER_TIMEOUT",
		"OMNISWITCH_READ_TIMEOUT",
		"OMNISWITCH_WRITE_TIMEOUT",
		"OMNISWITCH_IDLE_TIMEOUT",
		"OMNISWITCH_CIRCUIT_BREAKER_FAILURES",
		"OMNISWITCH_CIRCUIT_BREAKER_COOLDOWN",
		"OMNISWITCH_RATE_LIMIT_REQUESTS",
		"OMNISWITCH_RATE_LIMIT_WINDOW",
		"OMNISWITCH_RATE_LIMIT_REDIS_URL",
		"OMNISWITCH_RATE_LIMIT_PREFIX",
		"OMNISWITCH_RATE_LIMIT_FAIL_OPEN",
		"OMNISWITCH_OIDC_JWKS_URL",
		"OMNISWITCH_OIDC_ISSUER",
		"OMNISWITCH_OIDC_AUDIENCE",
		"OMNISWITCH_OIDC_ROLE_CLAIM",
		"OMNISWITCH_OIDC_WORKSPACE_CLAIM",
		"OMNISWITCH_OIDC_ORGANIZATION_CLAIM",
		"OMNISWITCH_OIDC_CACHE_TTL",
		"OMNISWITCH_SHADOW_PROVIDER",
		"OMNISWITCH_MCP_ENABLED",
		"OMNISWITCH_MCP_POLICY",
		"OMNISWITCH_MCP_UPSTREAM",
		"OMNISWITCH_AB_TEST",
		"OMNISWITCH_OTEL_ENABLED",
		"OMNISWITCH_OTEL_ENDPOINT",
		"OMNISWITCH_OTEL_SERVICE_NAME",
		"OMNISWITCH_OTEL_HEADERS",
		"OMNISWITCH_OTEL_INSECURE",
		"OMNISWITCH_OTEL_TIMEOUT",
		"OMNISWITCH_PROMETHEUS_ENABLED",
		"OMNISWITCH_VAULT_KEY",
		"OMNISWITCH_BOOTSTRAP_API_KEY",
		"OMNISWITCH_BOOTSTRAP_WORKSPACE",
		"OMNISWITCH_BOOTSTRAP_ROLE",
	} {
		t.Setenv(key, "")
	}
}
