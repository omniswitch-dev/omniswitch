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
	data := []byte(`apiVersion: omniswitch.dev/v1
kind: GatewayConfig
gateway:
  listen: ":9090"
  data_dir: "./data"
  auth: true
  cache_threshold: 0.88
  cache_ttl: 2h
  shadow_provider: anthropic
observability:
  otel_enabled: true
  otlp_endpoint: http://localhost:4318/v1/traces
  service_name: sentinel-test
  insecure: true
  timeout: 5s
  headers:
    x-api-key: test
providers:
  - name: openai-prod
    type: openai
    api_key_env: OPENAI_PROD_KEY
  - name: deepseek
    type: custom
    api_key_env: DEEPSEEK_API_KEY
    base_url: https://api.deepseek.com/v1
    models: [deepseek-chat]
    extra_headers:
      x-team: ai
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
	if cfg.Observability.OTelEnabled == nil || !*cfg.Observability.OTelEnabled || cfg.Observability.OTLPEndpoint != "http://localhost:4318/v1/traces" || cfg.Observability.ServiceName != "sentinel-test" {
		t.Fatalf("observability = %+v, want configured telemetry", cfg.Observability)
	}
	if cfg.Observability.Timeout == nil || cfg.Observability.Timeout.Duration != 5*time.Second || cfg.Observability.Headers["x-api-key"] != "test" {
		t.Fatalf("observability timeout/headers = %+v, want configured values", cfg.Observability)
	}
	if cfg.MCP.Policy != "policies/mcp.yaml" || cfg.MCP.Upstream != "http://127.0.0.1:9000/mcp" {
		t.Fatalf("mcp = %+v, want configured values", cfg.MCP)
	}
	if len(cfg.Providers) != 2 || cfg.Providers[0].Name != "openai-prod" || cfg.Providers[1].BaseURL != "https://api.deepseek.com/v1" || cfg.Providers[1].ExtraHeaders["x-team"] != "ai" {
		t.Fatalf("providers = %+v, want built-in and custom provider accounts", cfg.Providers)
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
		"apiVersion": "omniswitch.dev/v1",
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

func TestRepositoryExampleConfig(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "gateway-config.yaml")
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile(example) error = %v", err)
	}
	if cfg.Gateway.CacheScope != "api_key" || len(cfg.MCP.Targets) != 1 || cfg.Routes["fast-chat"].Timeout != "30s" {
		t.Fatalf("example config = %+v, want expanded production options", cfg)
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

func TestValidateRejectsInvalidObservabilityTimeout(t *testing.T) {
	_, err := Parse([]byte(`observability: {timeout: -1s}`), ".yaml")
	if err == nil || !strings.Contains(err.Error(), "observability.timeout") {
		t.Fatalf("Parse() error = %v, want observability timeout validation", err)
	}
}

func TestValidateRejectsUnsupportedGuardrailAction(t *testing.T) {
	_, err := Parse([]byte(`guardrails: {rules: [{name: test, pattern: test, action: retry}]}`), ".yaml")
	if err == nil || !strings.Contains(err.Error(), "action must be deny, redact, warn, or log") {
		t.Fatalf("Parse() error = %v, want guardrail action validation", err)
	}
}

func TestValidateAuthorizationRules(t *testing.T) {
	_, err := Parse([]byte(`authorization: {rules: [{when: 'role == "member"', effect: audit}]}`), ".yaml")
	if err == nil || !strings.Contains(err.Error(), "effect must be allow or deny") {
		t.Fatalf("Parse() error = %v, want authorization effect validation", err)
	}

	cfg, err := Parse([]byte(`authorization: {rules: [{name: member-only, when: 'role == "member"', effect: allow}]}`), ".yaml")
	if err != nil || len(cfg.Authorization.Rules) != 1 {
		t.Fatalf("Parse() cfg/err = %+v/%v, want authorization rule", cfg.Authorization, err)
	}
}

func TestValidateRateLimit(t *testing.T) {
	_, err := Parse([]byte(`rate_limit: {requests: 0}`), ".yaml")
	if err == nil || !strings.Contains(err.Error(), "rate_limit.requests") {
		t.Fatalf("Parse() error = %v, want rate limit request validation", err)
	}
	_, err = Parse([]byte(`rate_limit: {window: 0s}`), ".yaml")
	if err == nil || !strings.Contains(err.Error(), "rate_limit.window") {
		t.Fatalf("Parse() error = %v, want rate limit window validation", err)
	}
	cfg, err := Parse([]byte(`rate_limit: {requests: 60, window: 1m, redis_url: redis://localhost:6379/0, prefix: team:quota, fail_open: true}`), ".yaml")
	if err != nil || cfg.RateLimit.Requests == nil || *cfg.RateLimit.Requests != 60 || cfg.RateLimit.Window == nil || cfg.RateLimit.Window.Duration != time.Minute || !*cfg.RateLimit.FailOpen {
		t.Fatalf("Parse() cfg/err = %+v/%v, want valid rate limit config", cfg.RateLimit, err)
	}
}

func TestValidateOIDCIdentity(t *testing.T) {
	_, err := Parse([]byte(`identity: {oidc: {jwks_url: file:///keys.json}}`), ".yaml")
	if err == nil || !strings.Contains(err.Error(), "identity.oidc.jwks_url") {
		t.Fatalf("Parse() error = %v, want OIDC URL validation", err)
	}
	cfg, err := Parse([]byte(`identity: {oidc: {jwks_url: https://issuer.example.test/keys, issuer: https://issuer.example.test/, audience: omniswitch, cache_ttl: 10m}}`), ".yaml")
	if err != nil || cfg.Identity.OIDC.JWKSURL == "" || cfg.Identity.OIDC.CacheTTL == nil || cfg.Identity.OIDC.CacheTTL.Duration != 10*time.Minute {
		t.Fatalf("Parse() cfg/err = %+v/%v, want valid OIDC config", cfg.Identity.OIDC, err)
	}
}

func TestValidateStdioMCPTarget(t *testing.T) {
	_, err := Parse([]byte(`mcp: {targets: [{name: local, transport: stdio}]}`), ".yaml")
	if err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Fatalf("Parse() error = %v, want stdio command validation", err)
	}
	cfg, err := Parse([]byte(`mcp: {targets: [{name: local, transport: stdio, command: npx, args: ["-y", "@modelcontextprotocol/server-filesystem"], environment: {ALLOWED_DIR: "${WORKDIR}"}}]}`), ".yaml")
	if err != nil || len(cfg.MCP.Targets) != 1 || cfg.MCP.Targets[0].Command != "npx" || len(cfg.MCP.Targets[0].Args) != 2 {
		t.Fatalf("Parse() cfg/err = %+v/%v, want valid stdio target", cfg.MCP.Targets, err)
	}
}

func TestValidateGuardrailWebhook(t *testing.T) {
	_, err := Parse([]byte(`guardrails: {webhooks: [{name: managed, url: file:///policy}]}`), ".yaml")
	if err == nil || !strings.Contains(err.Error(), "guardrails.webhooks[0].url") {
		t.Fatalf("Parse() error = %v, want webhook URL validation", err)
	}
	cfg, err := Parse([]byte(`guardrails: {webhooks: [{name: managed, url: https://guardrail.example.test/check, stage: input, action: deny, timeout: 2s, fail_open: true}]}`), ".yaml")
	if err != nil || len(cfg.Guardrails.Webhooks) != 1 || cfg.Guardrails.Webhooks[0].Timeout == nil || cfg.Guardrails.Webhooks[0].Timeout.Duration != 2*time.Second || !*cfg.Guardrails.Webhooks[0].FailOpen {
		t.Fatalf("Parse() cfg/err = %+v/%v, want valid webhook", cfg.Guardrails.Webhooks, err)
	}
}
