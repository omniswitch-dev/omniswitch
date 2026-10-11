package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

type runtimeSettings struct {
	listenAddr             string
	dataDir                string
	authEnabled            bool
	cacheThreshold         float64
	cacheTTL               time.Duration
	cacheScope             string
	logPayloads            bool
	corsOrigins            []string
	circuitBreakerFailures int
	circuitBreakerCooldown time.Duration
	guardrailConfig        guardrail.Config
	authorizationRules     []gateway.AuthorizationRule
	rateLimitRequests      int
	rateLimitWindow        time.Duration
	rateLimitRedisURL      string
	rateLimitPrefix        string
	rateLimitFailOpen      bool
	guardrailStreamBuffer  bool
	maxRequestBytes        int64
	readHeaderTimeout      time.Duration
	readTimeout            time.Duration
	writeTimeout           time.Duration
	idleTimeout            time.Duration
	shadowProvider         string
	storageURL             string
	alerts                 gateway.AlertConfig
	mcpEnabled             bool
	mcpPolicyPath          string
	mcpPolicyExplicit      bool
	mcpUpstreamURL         string
	mcpTargets             []gatewayconfig.MCPTarget
	routes                 map[string]router.Route
	providerAccounts       []gatewayconfig.ProviderAccount
	oidcJWKSURL            string
	oidcIssuer             string
	oidcAudience           string
	oidcRoleClaim          string
	oidcWorkspaceClaim     string
	oidcOrganizationClaim  string
	oidcCacheTTL           time.Duration
	configPath             string
	otelEnabled            bool
	otelEndpoint           string
	otelServiceName        string
	otelHeaders            map[string]string
	otelInsecure           bool
	otelTimeout            time.Duration
	prometheusEnabled      bool
	vaultKey               string
	bootstrapAPIKey        string
	bootstrapWorkspaceID   string
	bootstrapRole          string
	shutdownTimeout        time.Duration
	allowNoProviders       bool
}

func loadRuntimeSettings() (runtimeSettings, error) {
	settings := runtimeSettings{
		listenAddr:             ":8080",
		dataDir:                ".",
		cacheThreshold:         0.95,
		cacheTTL:               24 * time.Hour,
		cacheScope:             "api_key",
		circuitBreakerFailures: 5,
		circuitBreakerCooldown: 60 * time.Second,
		rateLimitRequests:      120,
		rateLimitWindow:        time.Minute,
		rateLimitPrefix:        "omniswitch:ratelimit",
		guardrailStreamBuffer:  true,
		maxRequestBytes:        10 << 20,
		readHeaderTimeout:      5 * time.Second,
		readTimeout:            30 * time.Second,
		// Streaming responses can run for longer than a normal request; use a
		// configurable zero default rather than truncating them unexpectedly.
		writeTimeout:      0,
		idleTimeout:       60 * time.Second,
		mcpEnabled:        false,
		mcpPolicyPath:     "policies/production-delete.yaml",
		mcpUpstreamURL:    "http://127.0.0.1:8090/mcp",
		routes:            map[string]router.Route{},
		oidcCacheTTL:      5 * time.Minute,
		otelServiceName:   "omniswitch-gateway",
		otelTimeout:       10 * time.Second,
		prometheusEnabled: true,
		shutdownTimeout:   30 * time.Second,
	}

	if configPath := strings.TrimSpace(os.Getenv("OMNISWITCH_CONFIG")); configPath != "" {
		cfg, err := gatewayconfig.LoadFile(configPath)
		if err != nil {
			return settings, err
		}
		settings.configPath = configPath
		applyGatewayConfig(&settings, cfg)
	}

	settings.listenAddr = env("OMNISWITCH_LISTEN", settings.listenAddr)
	settings.dataDir = env("OMNISWITCH_DATA", settings.dataDir)
	settings.authEnabled = envBool("OMNISWITCH_AUTH", settings.authEnabled)
	settings.cacheThreshold = envFloat("OMNISWITCH_CACHE_THRESHOLD", settings.cacheThreshold)
	settings.cacheTTL = envDuration("OMNISWITCH_CACHE_TTL", settings.cacheTTL)
	settings.cacheScope = env("OMNISWITCH_CACHE_SCOPE", settings.cacheScope)
	settings.logPayloads = envBool("OMNISWITCH_LOG_PAYLOADS", settings.logPayloads)
	settings.guardrailStreamBuffer = envBool("OMNISWITCH_GUARDRAIL_STREAM_BUFFER", settings.guardrailStreamBuffer)
	settings.maxRequestBytes = envInt64("OMNISWITCH_MAX_REQUEST_BYTES", settings.maxRequestBytes)
	settings.readHeaderTimeout = envDuration("OMNISWITCH_READ_HEADER_TIMEOUT", settings.readHeaderTimeout)
	settings.readTimeout = envDuration("OMNISWITCH_READ_TIMEOUT", settings.readTimeout)
	settings.writeTimeout = envDuration("OMNISWITCH_WRITE_TIMEOUT", settings.writeTimeout)
	settings.idleTimeout = envDuration("OMNISWITCH_IDLE_TIMEOUT", settings.idleTimeout)
	settings.circuitBreakerFailures = envInt("OMNISWITCH_CIRCUIT_BREAKER_FAILURES", settings.circuitBreakerFailures)
	settings.circuitBreakerCooldown = envDuration("OMNISWITCH_CIRCUIT_BREAKER_COOLDOWN", settings.circuitBreakerCooldown)
	settings.rateLimitRequests = envInt("OMNISWITCH_RATE_LIMIT_REQUESTS", settings.rateLimitRequests)
	settings.rateLimitWindow = envDuration("OMNISWITCH_RATE_LIMIT_WINDOW", settings.rateLimitWindow)
	settings.rateLimitRedisURL = env("OMNISWITCH_RATE_LIMIT_REDIS_URL", settings.rateLimitRedisURL)
	settings.rateLimitPrefix = env("OMNISWITCH_RATE_LIMIT_PREFIX", settings.rateLimitPrefix)
	settings.rateLimitFailOpen = envBool("OMNISWITCH_RATE_LIMIT_FAIL_OPEN", settings.rateLimitFailOpen)
	if value := strings.TrimSpace(os.Getenv("OMNISWITCH_CORS_ORIGINS")); value != "" {
		settings.corsOrigins = parseCSV(value)
	}
	settings.shadowProvider = env("OMNISWITCH_SHADOW_PROVIDER", settings.shadowProvider)
	settings.mcpEnabled = envBool("OMNISWITCH_MCP_ENABLED", settings.mcpEnabled)
	if value := strings.TrimSpace(os.Getenv("OMNISWITCH_MCP_POLICY")); value != "" {
		settings.mcpPolicyPath = value
		settings.mcpPolicyExplicit = true
	}
	settings.mcpUpstreamURL = env("OMNISWITCH_MCP_UPSTREAM", settings.mcpUpstreamURL)
	settings.oidcJWKSURL = env("OMNISWITCH_OIDC_JWKS_URL", settings.oidcJWKSURL)
	settings.oidcIssuer = env("OMNISWITCH_OIDC_ISSUER", settings.oidcIssuer)
	settings.oidcAudience = env("OMNISWITCH_OIDC_AUDIENCE", settings.oidcAudience)
	settings.oidcRoleClaim = env("OMNISWITCH_OIDC_ROLE_CLAIM", settings.oidcRoleClaim)
	settings.oidcWorkspaceClaim = env("OMNISWITCH_OIDC_WORKSPACE_CLAIM", settings.oidcWorkspaceClaim)
	settings.oidcOrganizationClaim = env("OMNISWITCH_OIDC_ORGANIZATION_CLAIM", settings.oidcOrganizationClaim)
	settings.oidcCacheTTL = envDuration("OMNISWITCH_OIDC_CACHE_TTL", settings.oidcCacheTTL)
	settings.otelEnabled = envBool("OMNISWITCH_OTEL_ENABLED", settings.otelEnabled)
	settings.otelEndpoint = env("OMNISWITCH_OTEL_ENDPOINT", settings.otelEndpoint)
	settings.otelServiceName = env("OMNISWITCH_OTEL_SERVICE_NAME", settings.otelServiceName)
	settings.otelHeaders = mergeStringMaps(settings.otelHeaders, parseHeaderList(os.Getenv("OMNISWITCH_OTEL_HEADERS")))
	settings.otelInsecure = envBool("OMNISWITCH_OTEL_INSECURE", settings.otelInsecure)
	settings.otelTimeout = envDuration("OMNISWITCH_OTEL_TIMEOUT", settings.otelTimeout)
	settings.prometheusEnabled = envBool("OMNISWITCH_PROMETHEUS_ENABLED", settings.prometheusEnabled)
	settings.shutdownTimeout = envDuration("OMNISWITCH_SHUTDOWN_TIMEOUT", settings.shutdownTimeout)
	settings.allowNoProviders = envBool("OMNISWITCH_ALLOW_NO_PROVIDERS", settings.allowNoProviders)
	settings.vaultKey = env("OMNISWITCH_VAULT_KEY", settings.vaultKey)
	// Bootstrap credentials must not live in config-as-code, so they are only
	// accepted through the process environment or a secret manager injection.
	settings.bootstrapAPIKey = env("OMNISWITCH_BOOTSTRAP_API_KEY", settings.bootstrapAPIKey)
	settings.bootstrapWorkspaceID = env("OMNISWITCH_BOOTSTRAP_WORKSPACE", settings.bootstrapWorkspaceID)
	settings.bootstrapRole = env("OMNISWITCH_BOOTSTRAP_ROLE", settings.bootstrapRole)
	if settings.otelEndpoint != "" {
		settings.otelEnabled = true
	}
	if settings.oidcJWKSURL != "" {
		settings.authEnabled = true
	}
	for model, route := range parseABConfig(os.Getenv("OMNISWITCH_AB_TEST")) {
		settings.routes[model] = mergeRoute(settings.routes[model], route)
	}
	return settings, nil
}

func applyGatewayConfig(settings *runtimeSettings, cfg gatewayconfig.Config) {
	if cfg.Gateway.Listen != "" {
		settings.listenAddr = cfg.Gateway.Listen
	}
	if cfg.Gateway.DataDir != "" {
		settings.dataDir = cfg.Gateway.DataDir
	}
	if cfg.Gateway.Auth != nil {
		settings.authEnabled = *cfg.Gateway.Auth
	}
	if cfg.Gateway.CacheThreshold != nil {
		settings.cacheThreshold = *cfg.Gateway.CacheThreshold
	}
	if cfg.Gateway.CacheTTL != nil {
		settings.cacheTTL = cfg.Gateway.CacheTTL.Duration
	}
	if cfg.Gateway.CacheScope != "" {
		settings.cacheScope = cfg.Gateway.CacheScope
	}
	if cfg.Gateway.LogPayloads != nil {
		settings.logPayloads = *cfg.Gateway.LogPayloads
	}
	if cfg.Gateway.CORSOrigins != nil {
		settings.corsOrigins = append([]string(nil), cfg.Gateway.CORSOrigins...)
	}
	if cfg.Gateway.CircuitBreakerFailures != nil {
		settings.circuitBreakerFailures = *cfg.Gateway.CircuitBreakerFailures
	}
	if cfg.Gateway.CircuitBreakerCooldown != nil {
		settings.circuitBreakerCooldown = cfg.Gateway.CircuitBreakerCooldown.Duration
	}
	if cfg.Gateway.MaxRequestBytes > 0 {
		settings.maxRequestBytes = cfg.Gateway.MaxRequestBytes
	}
	if cfg.Gateway.ReadHeaderTimeout != nil {
		settings.readHeaderTimeout = cfg.Gateway.ReadHeaderTimeout.Duration
	}
	if cfg.Gateway.ReadTimeout != nil {
		settings.readTimeout = cfg.Gateway.ReadTimeout.Duration
	}
	if cfg.Gateway.WriteTimeout != nil {
		settings.writeTimeout = cfg.Gateway.WriteTimeout.Duration
	}
	if cfg.Gateway.IdleTimeout != nil {
		settings.idleTimeout = cfg.Gateway.IdleTimeout.Duration
	}
	if cfg.Identity.OIDC.JWKSURL != "" {
		settings.oidcJWKSURL = cfg.Identity.OIDC.JWKSURL
	}
	if cfg.Identity.OIDC.Issuer != "" {
		settings.oidcIssuer = cfg.Identity.OIDC.Issuer
	}
	if cfg.Identity.OIDC.Audience != "" {
		settings.oidcAudience = cfg.Identity.OIDC.Audience
	}
	if cfg.Identity.OIDC.RoleClaim != "" {
		settings.oidcRoleClaim = cfg.Identity.OIDC.RoleClaim
	}
	if cfg.Identity.OIDC.WorkspaceClaim != "" {
		settings.oidcWorkspaceClaim = cfg.Identity.OIDC.WorkspaceClaim
	}
	if cfg.Identity.OIDC.OrganizationClaim != "" {
		settings.oidcOrganizationClaim = cfg.Identity.OIDC.OrganizationClaim
	}
	if cfg.Identity.OIDC.CacheTTL != nil {
		settings.oidcCacheTTL = cfg.Identity.OIDC.CacheTTL.Duration
	}
	if cfg.Guardrails.Actions != nil {
		settings.guardrailConfig.Actions = cfg.Guardrails.Actions
	}
	if cfg.Authorization.Rules != nil {
		settings.authorizationRules = make([]gateway.AuthorizationRule, 0, len(cfg.Authorization.Rules))
		for _, rule := range cfg.Authorization.Rules {
			settings.authorizationRules = append(settings.authorizationRules, gateway.AuthorizationRule{
				Name: rule.Name, When: rule.When, Effect: rule.Effect, Message: rule.Message,
			})
		}
	}
	if cfg.Guardrails.Rules != nil || cfg.Guardrails.ToolRules != nil || cfg.Guardrails.Webhooks != nil || cfg.Guardrails.LLMJudges != nil {
		settings.guardrailConfig = guardrailConfigFromGatewayConfig(cfg, settings.guardrailConfig)
	}
	if cfg.RateLimit.Requests != nil {
		settings.rateLimitRequests = *cfg.RateLimit.Requests
	}
	if cfg.RateLimit.Window != nil {
		settings.rateLimitWindow = cfg.RateLimit.Window.Duration
	}
	if cfg.RateLimit.RedisURL != "" {
		settings.rateLimitRedisURL = cfg.RateLimit.RedisURL
	}
	if cfg.RateLimit.Prefix != "" {
		settings.rateLimitPrefix = cfg.RateLimit.Prefix
	}
	if cfg.RateLimit.FailOpen != nil {
		settings.rateLimitFailOpen = *cfg.RateLimit.FailOpen
	}
	if cfg.Guardrails.StreamBuffer != nil {
		settings.guardrailStreamBuffer = *cfg.Guardrails.StreamBuffer
	}
	if cfg.Gateway.ShadowProvider != "" {
		settings.shadowProvider = cfg.Gateway.ShadowProvider
	}
	if cfg.Gateway.StorageURL != "" {
		settings.storageURL = cfg.Gateway.StorageURL
	}
	if cfg.Gateway.Alerts != nil {
		settings.alerts = gateway.AlertConfig{
			WebhookURL: cfg.Gateway.Alerts.WebhookURL,
			SlackURL:   cfg.Gateway.Alerts.SlackURL,
			Thresholds: append([]float64(nil), cfg.Gateway.Alerts.Thresholds...),
		}
	}
	if cfg.Observability.OTelEnabled != nil {
		settings.otelEnabled = *cfg.Observability.OTelEnabled
	}
	if cfg.Observability.OTLPEndpoint != "" {
		settings.otelEndpoint = cfg.Observability.OTLPEndpoint
		settings.otelEnabled = true
	}
	if cfg.Observability.ServiceName != "" {
		settings.otelServiceName = cfg.Observability.ServiceName
	}
	if cfg.Observability.Headers != nil {
		settings.otelHeaders = cfg.Observability.Headers
	}
	if cfg.Observability.Insecure != nil {
		settings.otelInsecure = *cfg.Observability.Insecure
	}
	if cfg.Observability.Timeout != nil {
		settings.otelTimeout = cfg.Observability.Timeout.Duration
	}
	if cfg.Observability.PrometheusEnabled != nil {
		settings.prometheusEnabled = *cfg.Observability.PrometheusEnabled
	}
	if cfg.MCP.Enabled != nil {
		settings.mcpEnabled = *cfg.MCP.Enabled
	}
	if cfg.MCP.Policy != "" {
		settings.mcpPolicyPath = cfg.MCP.Policy
		settings.mcpPolicyExplicit = true
	}
	if cfg.MCP.Upstream != "" {
		settings.mcpUpstreamURL = cfg.MCP.Upstream
	}
	if cfg.MCP.Targets != nil {
		settings.mcpTargets = append([]gatewayconfig.MCPTarget(nil), cfg.MCP.Targets...)
	}
	for model, route := range cfg.Routes {
		settings.routes[model] = route
	}
	settings.providerAccounts = append(settings.providerAccounts, cfg.Providers...)
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneAnyMap(source map[string]any) map[string]any {
	if len(source) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func mergeRoute(base, override router.Route) router.Route {
	if override.Provider != "" {
		base.Provider = override.Provider
	}
	if override.Fallbacks != nil {
		base.Fallbacks = override.Fallbacks
	}
	if override.MaxRetries > 0 {
		base.MaxRetries = override.MaxRetries
	}
	if override.Variants != nil {
		base.Variants = override.Variants
	}
	return base
}

// guardrailConfigFromGatewayConfig converts the file schema into engine
// config. Base supplies defaults for sections the file omits.
func guardrailConfigFromGatewayConfig(cfg gatewayconfig.Config, base guardrail.Config) guardrail.Config {
	out := base
	if cfg.Guardrails.Actions != nil {
		out.Actions = cfg.Guardrails.Actions
	}
	if cfg.Guardrails.Rules != nil {
		out.Rules = make([]guardrail.Rule, 0, len(cfg.Guardrails.Rules))
		for _, rule := range cfg.Guardrails.Rules {
			out.Rules = append(out.Rules, guardrail.Rule{
				Name: rule.Name, Stage: rule.Stage, Pattern: rule.Pattern, Action: rule.Action, Message: rule.Message,
				Fallback: rule.Fallback,
			})
		}
	}
	if cfg.Guardrails.ToolRules != nil {
		out.ToolRules = make([]guardrail.ToolRule, 0, len(cfg.Guardrails.ToolRules))
		for _, rule := range cfg.Guardrails.ToolRules {
			out.ToolRules = append(out.ToolRules, guardrail.ToolRule{
				Name: rule.Name, Stage: rule.Stage, ToolName: rule.ToolName,
				ArgumentsPattern: rule.ArgumentsPattern, Action: rule.Action, Message: rule.Message,
			})
		}
	}
	if cfg.Guardrails.Webhooks != nil {
		out.Webhooks = make([]guardrail.Webhook, 0, len(cfg.Guardrails.Webhooks))
		for _, webhook := range cfg.Guardrails.Webhooks {
			configured := guardrail.Webhook{
				Name: webhook.Name, URL: webhook.URL, Stage: webhook.Stage, Action: webhook.Action,
				Headers: expandHeaderValues(webhook.Headers),
			}
			if webhook.Timeout != nil {
				configured.Timeout = webhook.Timeout.Duration
			}
			if webhook.FailOpen != nil {
				configured.FailOpen = *webhook.FailOpen
			}
			if webhook.MaxRetries != nil && *webhook.MaxRetries > 0 {
				configured.MaxRetries = *webhook.MaxRetries
			}
			out.Webhooks = append(out.Webhooks, configured)
		}
	}
	if cfg.Guardrails.LLMJudges != nil {
		out.LLMJudges = make([]guardrail.LLMJudge, 0, len(cfg.Guardrails.LLMJudges))
		for _, judge := range cfg.Guardrails.LLMJudges {
			configured := guardrail.LLMJudge{
				Name: judge.Name, URL: judge.URL, Model: judge.Model, Stage: judge.Stage, Action: judge.Action,
				SystemPrompt: judge.SystemPrompt, APIKey: os.Getenv(strings.TrimSpace(judge.APIKeyEnv)),
				Headers: expandHeaderValues(judge.Headers),
			}
			if judge.Timeout != nil {
				configured.Timeout = judge.Timeout.Duration
			}
			if judge.FailOpen != nil {
				configured.FailOpen = *judge.FailOpen
			}
			out.LLMJudges = append(out.LLMJudges, configured)
		}
	}
	return out
}

// startConfigReloader watches the gateway config file and applies safe runtime
// changes without a restart: routes, guardrails, cache posture, request
// shaping limits, circuit breaker settings, and the shadow provider. Identity,
// providers, rate limiting backends, and MCP targets still require a restart.
func startConfigReloader(path string, rtr *router.Router, gw *gateway.Handler, base runtimeSettings) {
	go func() {
		var lastMod time.Time
		if info, err := os.Stat(path); err == nil {
			lastMod = info.ModTime()
		}
		// Routes currently defined by the config file were already merged into
		// base.routes during startup; track them so reloads can replace them.
		var prevFileRoutes map[string]router.Route
		if cfg, err := gatewayconfig.LoadFile(path); err == nil && cfg.Routes != nil {
			prevFileRoutes = make(map[string]router.Route, len(cfg.Routes))
			for model, route := range cfg.Routes {
				prevFileRoutes[model] = route
			}
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			info, err := os.Stat(path)
			if err != nil || info.ModTime().Equal(lastMod) {
				continue
			}
			lastMod = info.ModTime()
			cfg, err := gatewayconfig.LoadFile(path)
			if err != nil {
				log.Printf("hot-reload skipped (config invalid, keeping previous configuration): %v", err)
				continue
			}
			prevFileRoutes, _ = applyHotConfig(cfg, rtr, gw, base, prevFileRoutes)
			log.Printf("configuration hot-reloaded from %s", path)
		}
	}()
}

// applyHotConfig swaps everything that is safe to change while serving.
// It operates on copies so concurrent /api/config readers never race. Routes
// defined by a previous version of the file are replaced wholesale; routes
// from environment or A/B configuration survive across reloads. The returned
// map is the new set of file-owned routes.
func applyHotConfig(cfg gatewayconfig.Config, rtr *router.Router, gw *gateway.Handler, base runtimeSettings, prevFileRoutes map[string]router.Route) (map[string]router.Route, *guardrail.Engine) {
	effective := make(map[string]router.Route, len(base.routes)+len(cfg.Routes))
	for model, route := range base.routes {
		effective[model] = route
	}
	for model := range prevFileRoutes {
		delete(effective, model)
	}
	fileRoutes := make(map[string]router.Route, len(cfg.Routes))
	for model, route := range cfg.Routes {
		fileRoutes[model] = route
		if existing, ok := effective[model]; ok && !prevRouteOwnedByFile(prevFileRoutes, model) {
			fileRoutes[model] = mergeRoute(existing, route)
			continue
		}
		effective[model] = route
	}
	for model, route := range fileRoutes {
		effective[model] = route
	}
	rtr.ReplaceRoutes(effective)
	gr := guardrail.NewEngine()
	if len(base.guardrailConfig.Actions) > 0 || len(cfg.Guardrails.Rules) > 0 || len(cfg.Guardrails.ToolRules) > 0 || len(cfg.Guardrails.Webhooks) > 0 || len(cfg.Guardrails.LLMJudges) > 0 {
		gr = guardrail.NewEngineWithConfig(guardrailConfigFromGatewayConfig(cfg, base.guardrailConfig))
	}
	gw.SetGuardrails(gr)

	if cfg.Gateway.CacheThreshold != nil {
		gw.SetSemanticCache(*cfg.Gateway.CacheThreshold)
	}
	if cfg.Gateway.CacheTTL != nil {
		gw.SetCacheTTL(cfg.Gateway.CacheTTL.Duration)
	}
	if cfg.Gateway.CacheScope != "" {
		gw.SetCacheScope(cfg.Gateway.CacheScope)
	}
	if cfg.Gateway.LogPayloads != nil {
		gw.SetLogPayloads(*cfg.Gateway.LogPayloads)
	}
	if cfg.Guardrails.StreamBuffer != nil {
		gw.SetStreamGuardrailBuffer(*cfg.Guardrails.StreamBuffer)
	}
	if cfg.Gateway.MaxRequestBytes > 0 {
		gw.SetMaxRequestBytes(cfg.Gateway.MaxRequestBytes)
	}
	if cfg.Gateway.ShadowProvider != "" {
		gw.SetShadowProvider(cfg.Gateway.ShadowProvider)
	} else {
		gw.SetShadowProvider(base.shadowProvider)
	}
	if cfg.Gateway.Alerts != nil {
		gw.SetAlerts(gateway.AlertConfig{
			WebhookURL: cfg.Gateway.Alerts.WebhookURL,
			SlackURL:   cfg.Gateway.Alerts.SlackURL,
			Thresholds: append([]float64(nil), cfg.Gateway.Alerts.Thresholds...),
		})
	} else {
		gw.SetAlerts(base.alerts)
	}
	failures, cooldown := base.circuitBreakerFailures, base.circuitBreakerCooldown
	if cfg.Gateway.CircuitBreakerFailures != nil {
		failures = *cfg.Gateway.CircuitBreakerFailures
	}
	if cfg.Gateway.CircuitBreakerCooldown != nil {
		cooldown = cfg.Gateway.CircuitBreakerCooldown.Duration
	}
	if failures > 0 && cooldown > 0 {
		rtr.SetCircuitBreaker(router.NewCircuitBreaker(failures, cooldown))
	}
	return fileRoutes, gr
}

func prevRouteOwnedByFile(prev map[string]router.Route, model string) bool {
	_, ok := prev[model]
	return ok
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func parseHeaderList(value string) map[string]string {
	headers := map[string]string{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		headers[key] = strings.TrimSpace(val)
	}
	return headers
}

func parseCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func mergeStringMaps(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	merged := map[string]string{}
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range override {
		merged[key] = value
	}
	return merged
}

func expandHeaderValues(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	expanded := make(map[string]string, len(headers))
	for key, value := range headers {
		expanded[key] = os.ExpandEnv(value)
	}
	return expanded
}

func expandStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	expanded := make([]string, len(values))
	for index, value := range values {
		expanded[index] = os.ExpandEnv(value)
	}
	return expanded
}

func parseABConfig(config string) map[string]router.Route {
	routes := map[string]router.Route{}
	config = strings.TrimSpace(config)
	if config == "" {
		return routes
	}

	for _, routeSpec := range strings.Split(config, ";") {
		routeSpec = strings.TrimSpace(routeSpec)
		if routeSpec == "" {
			continue
		}
		parts := strings.SplitN(routeSpec, "=", 2)
		if len(parts) != 2 {
			continue
		}
		model := strings.TrimSpace(parts[0])
		var route router.Route
		for _, variantSpec := range strings.Split(parts[1], ",") {
			fields := strings.Split(variantSpec, ":")
			if len(fields) != 3 {
				continue
			}
			weight, err := strconv.Atoi(strings.TrimSpace(fields[2]))
			if err != nil || weight <= 0 {
				continue
			}
			providerName := strings.TrimSpace(fields[0])
			variantModel := strings.TrimSpace(fields[1])
			route.Variants = append(route.Variants, router.Variant{
				Name:     providerName + "/" + variantModel,
				Provider: providerName,
				Model:    variantModel,
				Weight:   weight,
			})
		}
		if model != "" && len(route.Variants) > 0 {
			routes[model] = route
		}
	}
	return routes
}
