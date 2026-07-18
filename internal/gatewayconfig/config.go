package gatewayconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/router"

	"go.yaml.in/yaml/v3"
)

const (
	APIVersion = "omniswitch.dev/v1"
	Kind       = "GatewayConfig"
)

type Config struct {
	APIVersion    string                  `json:"apiVersion" yaml:"apiVersion"`
	Kind          string                  `json:"kind" yaml:"kind"`
	Gateway       Gateway                 `json:"gateway,omitempty" yaml:"gateway,omitempty"`
	Observability Observability           `json:"observability,omitempty" yaml:"observability,omitempty"`
	Providers     []ProviderAccount       `json:"providers,omitempty" yaml:"providers,omitempty"`
	Identity      Identity                `json:"identity,omitempty" yaml:"identity,omitempty"`
	Authorization Authorization           `json:"authorization,omitempty" yaml:"authorization,omitempty"`
	RateLimit     RateLimit               `json:"rate_limit,omitempty" yaml:"rate_limit,omitempty"`
	Guardrails    Guardrails              `json:"guardrails,omitempty" yaml:"guardrails,omitempty"`
	MCP           MCP                     `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	Routes        map[string]router.Route `json:"routes,omitempty" yaml:"routes,omitempty"`
}

// Identity configures external workload identity in addition to local API
// keys. OIDC JWTs are validated against the issuer's JWKS endpoint.
type Identity struct {
	OIDC OIDC `json:"oidc,omitempty" yaml:"oidc,omitempty"`
}

type OIDC struct {
	JWKSURL           string    `json:"jwks_url,omitempty" yaml:"jwks_url,omitempty"`
	Issuer            string    `json:"issuer,omitempty" yaml:"issuer,omitempty"`
	Audience          string    `json:"audience,omitempty" yaml:"audience,omitempty"`
	RoleClaim         string    `json:"role_claim,omitempty" yaml:"role_claim,omitempty"`
	WorkspaceClaim    string    `json:"workspace_claim,omitempty" yaml:"workspace_claim,omitempty"`
	OrganizationClaim string    `json:"organization_claim,omitempty" yaml:"organization_claim,omitempty"`
	CacheTTL          *Duration `json:"cache_ttl,omitempty" yaml:"cache_ttl,omitempty"`
}

// Authorization configures CEL authorization after API-key authentication.
// If one or more allow rules are present, every request must match an allow
// rule and must not match a deny rule.
type Authorization struct {
	Rules []AuthorizationRule `json:"rules,omitempty" yaml:"rules,omitempty"`
}

type AuthorizationRule struct {
	Name    string `json:"name,omitempty" yaml:"name,omitempty"`
	When    string `json:"when" yaml:"when"`
	Effect  string `json:"effect" yaml:"effect"`
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
}

// RateLimit configures a request quota. When RedisURL is omitted, OmniSwitch
// uses its local sliding-window backend.
type RateLimit struct {
	Requests *int      `json:"requests,omitempty" yaml:"requests,omitempty"`
	Window   *Duration `json:"window,omitempty" yaml:"window,omitempty"`
	RedisURL string    `json:"redis_url,omitempty" yaml:"redis_url,omitempty"`
	Prefix   string    `json:"prefix,omitempty" yaml:"prefix,omitempty"`
	FailOpen *bool     `json:"fail_open,omitempty" yaml:"fail_open,omitempty"`
}

type Gateway struct {
	Listen         string    `json:"listen,omitempty" yaml:"listen,omitempty"`
	DataDir        string    `json:"data_dir,omitempty" yaml:"data_dir,omitempty"`
	Auth           *bool     `json:"auth,omitempty" yaml:"auth,omitempty"`
	CacheThreshold *float64  `json:"cache_threshold,omitempty" yaml:"cache_threshold,omitempty"`
	CacheTTL       *Duration `json:"cache_ttl,omitempty" yaml:"cache_ttl,omitempty"`
	// CacheScope prevents a response generated for one tenant from being served to
	// another tenant. Valid values are api_key, workspace, organization, and global.
	CacheScope string `json:"cache_scope,omitempty" yaml:"cache_scope,omitempty"`
	// LogPayloads controls storage of raw prompts and completions. It defaults to
	// false at runtime so production deployments do not persist sensitive content.
	LogPayloads            *bool     `json:"log_payloads,omitempty" yaml:"log_payloads,omitempty"`
	CORSOrigins            []string  `json:"cors_origins,omitempty" yaml:"cors_origins,omitempty"`
	CircuitBreakerFailures *int      `json:"circuit_breaker_failures,omitempty" yaml:"circuit_breaker_failures,omitempty"`
	CircuitBreakerCooldown *Duration `json:"circuit_breaker_cooldown,omitempty" yaml:"circuit_breaker_cooldown,omitempty"`
	MaxRequestBytes        int64     `json:"max_request_bytes,omitempty" yaml:"max_request_bytes,omitempty"`
	ReadHeaderTimeout      *Duration `json:"read_header_timeout,omitempty" yaml:"read_header_timeout,omitempty"`
	ReadTimeout            *Duration `json:"read_timeout,omitempty" yaml:"read_timeout,omitempty"`
	WriteTimeout           *Duration `json:"write_timeout,omitempty" yaml:"write_timeout,omitempty"`
	IdleTimeout            *Duration `json:"idle_timeout,omitempty" yaml:"idle_timeout,omitempty"`
	ShadowProvider         string    `json:"shadow_provider,omitempty" yaml:"shadow_provider,omitempty"`
}

type MCP struct {
	Enabled  *bool       `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Policy   string      `json:"policy,omitempty" yaml:"policy,omitempty"`
	Upstream string      `json:"upstream,omitempty" yaml:"upstream,omitempty"`
	Targets  []MCPTarget `json:"targets,omitempty" yaml:"targets,omitempty"`
}

// Guardrails configures OmniSwitch's built-in checks and declarative regex
// rules. Actions are keyed by built-in check type, for example injection:
// deny or pii: redact.
type Guardrails struct {
	Actions      map[string]string  `json:"actions,omitempty" yaml:"actions,omitempty"`
	Rules        []GuardrailRule    `json:"rules,omitempty" yaml:"rules,omitempty"`
	Webhooks     []GuardrailWebhook `json:"webhooks,omitempty" yaml:"webhooks,omitempty"`
	StreamBuffer *bool              `json:"stream_buffer,omitempty" yaml:"stream_buffer,omitempty"`
}

type GuardrailRule struct {
	Name    string `json:"name" yaml:"name"`
	Stage   string `json:"stage,omitempty" yaml:"stage,omitempty"`
	Pattern string `json:"pattern" yaml:"pattern"`
	Action  string `json:"action,omitempty" yaml:"action,omitempty"`
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
}

// GuardrailWebhook delegates a stage to an external moderation or safety
// service. Its endpoint returns {triggered,message,details} or {allowed}.
type GuardrailWebhook struct {
	Name     string            `json:"name" yaml:"name"`
	URL      string            `json:"url" yaml:"url"`
	Stage    string            `json:"stage,omitempty" yaml:"stage,omitempty"`
	Action   string            `json:"action,omitempty" yaml:"action,omitempty"`
	Headers  map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Timeout  *Duration         `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	FailOpen *bool             `json:"fail_open,omitempty" yaml:"fail_open,omitempty"`
}

// MCPTarget is a named remote MCP server. Multiple targets are exposed through
// one OmniSwitch endpoint and tool names are prefixed with the target name.
type MCPTarget struct {
	Name               string            `json:"name" yaml:"name"`
	Transport          string            `json:"transport,omitempty" yaml:"transport,omitempty"`
	Upstream           string            `json:"upstream" yaml:"upstream"`
	Command            string            `json:"command,omitempty" yaml:"command,omitempty"`
	Args               []string          `json:"args,omitempty" yaml:"args,omitempty"`
	Environment        map[string]string `json:"environment,omitempty" yaml:"environment,omitempty"`
	Policy             string            `json:"policy,omitempty" yaml:"policy,omitempty"`
	Headers            map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	ForwardBearerToken *bool             `json:"forward_bearer_token,omitempty" yaml:"forward_bearer_token,omitempty"`
	Enabled            *bool             `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

type Observability struct {
	OTelEnabled       *bool             `json:"otel_enabled,omitempty" yaml:"otel_enabled,omitempty"`
	OTLPEndpoint      string            `json:"otlp_endpoint,omitempty" yaml:"otlp_endpoint,omitempty"`
	ServiceName       string            `json:"service_name,omitempty" yaml:"service_name,omitempty"`
	Headers           map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Insecure          *bool             `json:"insecure,omitempty" yaml:"insecure,omitempty"`
	Timeout           *Duration         `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	PrometheusEnabled *bool             `json:"prometheus_enabled,omitempty" yaml:"prometheus_enabled,omitempty"`
}

type ProviderAccount struct {
	Name         string            `json:"name" yaml:"name"`
	Type         string            `json:"type" yaml:"type"`
	APIKeyEnv    string            `json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`
	BaseURL      string            `json:"base_url,omitempty" yaml:"base_url,omitempty"`
	Models       []string          `json:"models,omitempty" yaml:"models,omitempty"`
	ExtraHeaders map[string]string `json:"extra_headers,omitempty" yaml:"extra_headers,omitempty"`
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
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("parse gateway config json: %w", err)
		}
	default:
		decoder := yaml.NewDecoder(bytes.NewReader(trimmed))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
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
	if cfg.Gateway.CircuitBreakerFailures != nil && *cfg.Gateway.CircuitBreakerFailures < 1 {
		return fmt.Errorf("gateway.circuit_breaker_failures must be positive")
	}
	if cfg.Gateway.CircuitBreakerCooldown != nil && cfg.Gateway.CircuitBreakerCooldown.Duration <= 0 {
		return fmt.Errorf("gateway.circuit_breaker_cooldown must be positive")
	}
	if cfg.Gateway.MaxRequestBytes < 0 {
		return fmt.Errorf("gateway.max_request_bytes must be non-negative")
	}
	for name, value := range map[string]*Duration{
		"read_header_timeout": cfg.Gateway.ReadHeaderTimeout,
		"read_timeout":        cfg.Gateway.ReadTimeout,
		"write_timeout":       cfg.Gateway.WriteTimeout,
		"idle_timeout":        cfg.Gateway.IdleTimeout,
	} {
		if value != nil && value.Duration < 0 {
			return fmt.Errorf("gateway.%s must be non-negative", name)
		}
	}
	if scope := strings.TrimSpace(cfg.Gateway.CacheScope); scope != "" {
		switch scope {
		case "api_key", "workspace", "organization", "global":
		default:
			return fmt.Errorf("gateway.cache_scope must be api_key, workspace, organization, or global")
		}
	}
	if cfg.Observability.Timeout != nil && cfg.Observability.Timeout.Duration < 0 {
		return fmt.Errorf("observability.timeout must be non-negative")
	}
	if cfg.Identity.OIDC.JWKSURL != "" {
		parsed, err := url.Parse(cfg.Identity.OIDC.JWKSURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("identity.oidc.jwks_url must be an absolute HTTP(S) URL")
		}
	}
	if cfg.Identity.OIDC.CacheTTL != nil && cfg.Identity.OIDC.CacheTTL.Duration <= 0 {
		return fmt.Errorf("identity.oidc.cache_ttl must be positive")
	}
	for index, rule := range cfg.Authorization.Rules {
		if strings.TrimSpace(rule.When) == "" {
			return fmt.Errorf("authorization.rules[%d].when is required", index)
		}
		switch strings.ToLower(strings.TrimSpace(rule.Effect)) {
		case "allow", "deny":
		default:
			return fmt.Errorf("authorization.rules[%d].effect must be allow or deny", index)
		}
	}
	if cfg.RateLimit.Requests != nil && *cfg.RateLimit.Requests < 1 {
		return fmt.Errorf("rate_limit.requests must be positive")
	}
	if cfg.RateLimit.Window != nil && cfg.RateLimit.Window.Duration <= 0 {
		return fmt.Errorf("rate_limit.window must be positive")
	}
	for i, account := range cfg.Providers {
		if strings.TrimSpace(account.Name) == "" {
			return fmt.Errorf("providers[%d].name is required", i)
		}
		if strings.TrimSpace(account.Type) == "" {
			return fmt.Errorf("providers[%d].type is required", i)
		}
	}
	for i, target := range cfg.MCP.Targets {
		if strings.TrimSpace(target.Name) == "" {
			return fmt.Errorf("mcp.targets[%d].name is required", i)
		}
		switch strings.ToLower(strings.TrimSpace(target.Transport)) {
		case "", "http", "streamable_http":
			if strings.TrimSpace(target.Upstream) == "" {
				return fmt.Errorf("mcp.targets[%d].upstream is required for HTTP transport", i)
			}
		case "stdio":
			if strings.TrimSpace(target.Command) == "" {
				return fmt.Errorf("mcp.targets[%d].command is required for stdio transport", i)
			}
		default:
			return fmt.Errorf("mcp.targets[%d].transport must be http, streamable_http, or stdio", i)
		}
	}
	for i, rule := range cfg.Guardrails.Rules {
		if strings.TrimSpace(rule.Name) == "" || strings.TrimSpace(rule.Pattern) == "" {
			return fmt.Errorf("guardrails.rules[%d] requires name and pattern", i)
		}
		switch rule.Stage {
		case "", "input", "output", "both":
		default:
			return fmt.Errorf("guardrails.rules[%d].stage must be input, output, or both", i)
		}
		if _, err := regexp.Compile(rule.Pattern); err != nil {
			return fmt.Errorf("guardrails.rules[%d].pattern: %w", i, err)
		}
		if rule.Action != "" && !validGuardrailAction(rule.Action) {
			return fmt.Errorf("guardrails.rules[%d].action must be deny, redact, warn, or log", i)
		}
	}
	for i, webhook := range cfg.Guardrails.Webhooks {
		if strings.TrimSpace(webhook.Name) == "" || strings.TrimSpace(webhook.URL) == "" {
			return fmt.Errorf("guardrails.webhooks[%d] requires name and url", i)
		}
		parsed, err := url.Parse(webhook.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("guardrails.webhooks[%d].url must be an absolute HTTP(S) URL", i)
		}
		switch webhook.Stage {
		case "", "input", "output", "both":
		default:
			return fmt.Errorf("guardrails.webhooks[%d].stage must be input, output, or both", i)
		}
		if webhook.Action != "" && !validGuardrailAction(webhook.Action) {
			return fmt.Errorf("guardrails.webhooks[%d].action must be deny, redact, warn, or log", i)
		}
		if webhook.Timeout != nil && webhook.Timeout.Duration <= 0 {
			return fmt.Errorf("guardrails.webhooks[%d].timeout must be positive", i)
		}
	}
	for check, action := range cfg.Guardrails.Actions {
		if strings.TrimSpace(check) == "" || !validGuardrailAction(action) {
			return fmt.Errorf("guardrails.actions must map a check name to deny, redact, warn, or log")
		}
	}
	for model, route := range cfg.Routes {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("route model cannot be empty")
		}
		if route.MaxRetries < 0 {
			return fmt.Errorf("route %q max_retries must be non-negative", model)
		}
		if route.Timeout != "" {
			if duration, err := time.ParseDuration(route.Timeout); err != nil || duration <= 0 {
				return fmt.Errorf("route %q timeout must be a positive duration", model)
			}
		}
		if route.RetryBackoff != "" {
			if duration, err := time.ParseDuration(route.RetryBackoff); err != nil || duration <= 0 {
				return fmt.Errorf("route %q retry_backoff must be a positive duration", model)
			}
		}
		for _, code := range route.RetryCodes {
			if code < 100 || code > 599 {
				return fmt.Errorf("route %q retry_codes must contain HTTP status codes", model)
			}
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

func validGuardrailAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "deny", "redact", "warn", "log":
		return true
	default:
		return false
	}
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
