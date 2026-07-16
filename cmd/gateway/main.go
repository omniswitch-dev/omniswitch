package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/admin"
	"github.com/omniswitch-dev/omniswitch/internal/audit"
	"github.com/omniswitch-dev/omniswitch/internal/dashboard"
	"github.com/omniswitch-dev/omniswitch/internal/eval"
	"github.com/omniswitch-dev/omniswitch/internal/feedback"
	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/org"
	"github.com/omniswitch-dev/omniswitch/internal/policy"
	"github.com/omniswitch-dev/omniswitch/internal/prompt"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	mcpproxy "github.com/omniswitch-dev/omniswitch/internal/proxy"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
	"github.com/omniswitch-dev/omniswitch/internal/telemetry"
	"github.com/omniswitch-dev/omniswitch/internal/vault"
)

func main() {
	settings, err := loadRuntimeSettings()
	if err != nil {
		log.Fatalf("failed to load gateway settings: %v", err)
	}
	if settings.configPath != "" {
		log.Printf("loaded gateway config: %s", settings.configPath)
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		Enabled:     settings.otelEnabled,
		Endpoint:    settings.otelEndpoint,
		ServiceName: settings.otelServiceName,
		Headers:     settings.otelHeaders,
		Insecure:    settings.otelInsecure,
		Timeout:     settings.otelTimeout,
	})
	if err != nil {
		log.Fatalf("failed to initialize telemetry: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(ctx); err != nil {
			log.Printf("failed to shutdown telemetry: %v", err)
		}
	}()

	// Initialize store.
	st, err := store.New(settings.dataDir)
	if err != nil {
		log.Fatalf("failed to initialize store: %v", err)
	}
	defer st.Close()
	if err := ensureBootstrapKey(context.Background(), st, settings); err != nil {
		log.Fatalf("failed to initialize authentication: %v", err)
	}
	vaultManager := vault.New(st, settings.vaultKey)
	if settings.vaultKey == "" {
		log.Println("WARNING: OMNISWITCH_VAULT_KEY is not set. Provider vault entries created in this process will not be decryptable after restart.")
	}

	// Initialize provider registry.
	registry := provider.NewRegistry()
	registerEnvProvider(registry, "openai", "OPENAI_API_KEY")
	registerEnvProvider(registry, "anthropic", "ANTHROPIC_API_KEY")
	registerEnvProvider(registry, "google", "GOOGLE_API_KEY")
	registerEnvProvider(registry, "groq", "GROQ_API_KEY")
	for _, account := range settings.providerAccounts {
		registerProviderAccount(registry, account)
	}
	registerVaultProviders(registry, vaultManager)

	if len(registry.Names()) == 0 {
		log.Println("WARNING: No provider API keys configured. Set OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLE_API_KEY, or GROQ_API_KEY.")
	}

	// Initialize components.
	rtr := router.New(registry)
	for model, route := range settings.routes {
		rtr.SetRoute(model, route)
		log.Printf("configured route for %s: %+v", model, route)
	}
	rtr.SetCircuitBreaker(router.NewCircuitBreaker(settings.circuitBreakerFailures, settings.circuitBreakerCooldown))
	gr := guardrail.NewEngine()
	if len(settings.guardrailConfig.Actions) > 0 || len(settings.guardrailConfig.Rules) > 0 {
		gr = guardrail.NewEngineWithConfig(settings.guardrailConfig)
	}
	gw := gateway.New(registry, rtr, st, gr)
	gw.SetSemanticCache(settings.cacheThreshold)
	gw.SetCacheTTL(settings.cacheTTL)
	gw.SetCacheScope(settings.cacheScope)
	gw.SetLogPayloads(settings.logPayloads)
	gw.SetStreamGuardrailBuffer(settings.guardrailStreamBuffer)
	gw.SetMaxRequestBytes(settings.maxRequestBytes)
	gw.SetShadowProvider(settings.shadowProvider)
	adminHandler := admin.New(st)
	feedbackHandler := feedback.New(st)
	promptHandler := prompt.New(st)
	orgHandler := org.New(st)
	evalHandler := eval.New()
	vaultHandler := vault.NewHandler(vaultManager)
	dash := dashboard.New(st)

	// Build mux.
	mux := http.NewServeMux()

	// Gateway API (OpenAI-compatible).
	mux.HandleFunc("/v1/chat/completions", gw.ChatCompletions)
	mux.HandleFunc("/v1/responses", gw.Responses)
	mux.HandleFunc("/v1/messages", gw.Messages)
	mux.HandleFunc("/v1/embeddings", gw.Embeddings)
	mux.HandleFunc("/v1/models", gw.ListModels)
	mux.HandleFunc("/api/providers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"providers": registry.Names(), "models": registry.AllModels()})
	})
	if settings.mcpEnabled {
		mcpEngine, err := policy.NewEngineFromFiles(settings.mcpPolicyPath)
		if err != nil {
			log.Fatalf("failed to load MCP policy: %v", err)
		}
		mcpAuditor := audit.NewMultiLogger(audit.NewStdoutLogger(os.Stdout), audit.NewStoreLogger(st))
		mcpTargets, err := buildMCPTargets(mcpEngine, settings)
		if err != nil {
			log.Fatalf("failed to configure MCP targets: %v", err)
		}
		mcpHandler, err := mcpproxy.NewMultiHandler(mcpEngine, mcpAuditor, mcpTargets)
		if err != nil {
			log.Fatalf("failed to initialize MCP gateway: %v", err)
		}
		mux.Handle("/mcp", mcpHandler)
		mux.Handle("/v1/mcp/tools/call", mcpHandler)
	}

	// Admin API.
	mux.HandleFunc("/api/keys", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			adminHandler.ListAPIKeys(w, r)
		case http.MethodPost:
			adminHandler.CreateAPIKey(w, r)
		case http.MethodDelete:
			adminHandler.DeleteAPIKey(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/virtual-keys", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			vaultHandler.ListVirtualKeys(w, r)
		case http.MethodPost:
			vaultHandler.CreateVirtualKey(w, r)
		case http.MethodDelete:
			vaultHandler.RevokeVirtualKey(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/virtual-keys/rotate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		vaultHandler.RotateVirtualKey(w, r)
	})

	// Feedback API.
	mux.HandleFunc("/api/feedback", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			feedbackHandler.ListFeedback(w, r)
		case http.MethodPost:
			feedbackHandler.CreateFeedback(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Organization and workspace API.
	mux.HandleFunc("/api/orgs", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			orgHandler.ListOrganizations(w, r)
		case http.MethodPost:
			orgHandler.CreateOrganization(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/workspaces", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			orgHandler.ListWorkspaces(w, r)
		case http.MethodPost:
			orgHandler.CreateWorkspace(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			orgHandler.ListUsers(w, r)
		case http.MethodPost:
			orgHandler.CreateUser(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/workspace-members", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			orgHandler.ListWorkspaceMembers(w, r)
		case http.MethodPost:
			orgHandler.UpsertWorkspaceMember(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Prompt API.
	mux.HandleFunc("/api/prompts/versions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		promptHandler.ListPromptVersions(w, r)
	})
	mux.HandleFunc("/api/prompts", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			promptHandler.ListPrompts(w, r)
		case http.MethodPost:
			promptHandler.CreatePrompt(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/prompts/render", promptHandler.RenderPrompt)
	mux.HandleFunc("/api/evals/policy", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		evalHandler.ReplayPolicies(w, r)
	})

	// Dashboard API + static files.
	dash.RegisterRoutes(mux)
	if settings.prometheusEnabled {
		dash.RegisterPrometheus(mux)
	}

	// Wrap with middleware.
	auth := gateway.NewAuthMiddleware(st, settings.authEnabled)
	authorize := gateway.NewAuthorizationMiddleware(settings.authEnabled)
	rateLimiter := gateway.NewRateLimiter(120, time.Minute)
	handler := gateway.CORSMiddlewareWithOrigins(settings.corsOrigins, gateway.LoggingMiddleware(auth.Wrap(authorize.Wrap(rateLimiter.Wrap(mux)))))

	// Print startup banner.
	fmt.Println(banner())
	log.Printf("OmniSwitch AI Gateway listening on %s", settings.listenAddr)
	log.Printf("Dashboard: http://localhost%s", settings.listenAddr)
	log.Printf("Gateway:   http://localhost%s/v1/chat/completions", settings.listenAddr)
	if settings.mcpEnabled {
		log.Printf("MCP:       http://localhost%s/mcp", settings.listenAddr)
	}
	log.Printf("Providers: %v", registry.Names())

	server := &http.Server{
		Addr:              settings.listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: settings.readHeaderTimeout,
		ReadTimeout:       settings.readTimeout,
		WriteTimeout:      settings.writeTimeout,
		IdleTimeout:       settings.idleTimeout,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

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
	guardrailStreamBuffer  bool
	maxRequestBytes        int64
	readHeaderTimeout      time.Duration
	readTimeout            time.Duration
	writeTimeout           time.Duration
	idleTimeout            time.Duration
	shadowProvider         string
	mcpEnabled             bool
	mcpPolicyPath          string
	mcpUpstreamURL         string
	mcpTargets             []gatewayconfig.MCPTarget
	routes                 map[string]router.Route
	providerAccounts       []gatewayconfig.ProviderAccount
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
		guardrailStreamBuffer:  true,
		maxRequestBytes:        10 << 20,
		readHeaderTimeout:      5 * time.Second,
		readTimeout:            30 * time.Second,
		// Streaming responses can run for longer than a normal request; use a
		// configurable zero default rather than truncating them unexpectedly.
		writeTimeout:      0,
		idleTimeout:       60 * time.Second,
		mcpEnabled:        true,
		mcpPolicyPath:     "policies/production-delete.yaml",
		mcpUpstreamURL:    "http://127.0.0.1:8090/mcp",
		routes:            map[string]router.Route{},
		otelServiceName:   "omniswitch-gateway",
		otelTimeout:       10 * time.Second,
		prometheusEnabled: true,
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
	if value := strings.TrimSpace(os.Getenv("OMNISWITCH_CORS_ORIGINS")); value != "" {
		settings.corsOrigins = parseCSV(value)
	}
	settings.shadowProvider = env("OMNISWITCH_SHADOW_PROVIDER", settings.shadowProvider)
	settings.mcpEnabled = envBool("OMNISWITCH_MCP_ENABLED", settings.mcpEnabled)
	settings.mcpPolicyPath = env("OMNISWITCH_MCP_POLICY", settings.mcpPolicyPath)
	settings.mcpUpstreamURL = env("OMNISWITCH_MCP_UPSTREAM", settings.mcpUpstreamURL)
	settings.otelEnabled = envBool("OMNISWITCH_OTEL_ENABLED", settings.otelEnabled)
	settings.otelEndpoint = env("OMNISWITCH_OTEL_ENDPOINT", settings.otelEndpoint)
	settings.otelServiceName = env("OMNISWITCH_OTEL_SERVICE_NAME", settings.otelServiceName)
	settings.otelHeaders = mergeStringMaps(settings.otelHeaders, parseHeaderList(os.Getenv("OMNISWITCH_OTEL_HEADERS")))
	settings.otelInsecure = envBool("OMNISWITCH_OTEL_INSECURE", settings.otelInsecure)
	settings.otelTimeout = envDuration("OMNISWITCH_OTEL_TIMEOUT", settings.otelTimeout)
	settings.prometheusEnabled = envBool("OMNISWITCH_PROMETHEUS_ENABLED", settings.prometheusEnabled)
	settings.vaultKey = env("OMNISWITCH_VAULT_KEY", settings.vaultKey)
	// Bootstrap credentials must not live in config-as-code, so they are only
	// accepted through the process environment or a secret manager injection.
	settings.bootstrapAPIKey = env("OMNISWITCH_BOOTSTRAP_API_KEY", settings.bootstrapAPIKey)
	settings.bootstrapWorkspaceID = env("OMNISWITCH_BOOTSTRAP_WORKSPACE", settings.bootstrapWorkspaceID)
	settings.bootstrapRole = env("OMNISWITCH_BOOTSTRAP_ROLE", settings.bootstrapRole)
	if settings.otelEndpoint != "" {
		settings.otelEnabled = true
	}
	for model, route := range parseABConfig(os.Getenv("OMNISWITCH_AB_TEST")) {
		settings.routes[model] = mergeRoute(settings.routes[model], route)
	}
	return settings, nil
}

// ensureBootstrapKey prevents an auth-enabled, empty deployment from becoming
// unmanageable. The bootstrap secret is never logged or exposed through the
// control-plane API; only its SHA-256 hash is persisted.
func ensureBootstrapKey(ctx context.Context, st *store.Store, settings runtimeSettings) error {
	if !settings.authEnabled {
		return nil
	}
	if secret := strings.TrimSpace(settings.bootstrapAPIKey); secret != "" {
		hash := sha256.Sum256([]byte(secret))
		role := strings.TrimSpace(settings.bootstrapRole)
		if role == "" {
			role = "owner"
		}
		if role != "owner" && role != "admin" {
			return fmt.Errorf("OMNISWITCH_BOOTSTRAP_ROLE must be owner or admin")
		}
		prefix := secret
		if len(prefix) > 12 {
			prefix = prefix[:12]
		}
		return st.InsertAPIKey(ctx, store.APIKey{
			ID:          "bootstrap-admin",
			Name:        "Bootstrap administrator",
			KeyHash:     hex.EncodeToString(hash[:]),
			KeyPrefix:   prefix + "...",
			WorkspaceID: strings.TrimSpace(settings.bootstrapWorkspaceID),
			Role:        role,
			CreatedAt:   time.Now().UTC(),
			RateLimit:   120,
			Enabled:     true,
		})
	}

	keys, err := st.ListAPIKeys(ctx)
	if err != nil {
		return fmt.Errorf("list existing API keys: %w", err)
	}
	if len(keys) == 0 {
		return fmt.Errorf("authentication is enabled but no API keys exist; set OMNISWITCH_BOOTSTRAP_API_KEY for first startup")
	}
	return nil
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
	if cfg.Guardrails.Actions != nil {
		settings.guardrailConfig.Actions = cfg.Guardrails.Actions
	}
	if cfg.Guardrails.Rules != nil {
		settings.guardrailConfig.Rules = make([]guardrail.Rule, 0, len(cfg.Guardrails.Rules))
		for _, rule := range cfg.Guardrails.Rules {
			settings.guardrailConfig.Rules = append(settings.guardrailConfig.Rules, guardrail.Rule{
				Name: rule.Name, Stage: rule.Stage, Pattern: rule.Pattern, Action: rule.Action, Message: rule.Message,
			})
		}
	}
	if cfg.Guardrails.StreamBuffer != nil {
		settings.guardrailStreamBuffer = *cfg.Guardrails.StreamBuffer
	}
	if cfg.Gateway.ShadowProvider != "" {
		settings.shadowProvider = cfg.Gateway.ShadowProvider
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

func buildMCPTargets(defaultEngine policy.Engine, settings runtimeSettings) ([]mcpproxy.TargetConfig, error) {
	if len(settings.mcpTargets) == 0 {
		return []mcpproxy.TargetConfig{{Name: "default", Upstream: settings.mcpUpstreamURL, Engine: defaultEngine}}, nil
	}
	targets := make([]mcpproxy.TargetConfig, 0, len(settings.mcpTargets))
	for _, configured := range settings.mcpTargets {
		if configured.Enabled != nil && !*configured.Enabled {
			continue
		}
		engine := defaultEngine
		if configured.Policy != "" {
			loaded, err := policy.NewEngineFromFiles(configured.Policy)
			if err != nil {
				return nil, fmt.Errorf("load policy for MCP target %q: %w", configured.Name, err)
			}
			engine = loaded
		}
		targets = append(targets, mcpproxy.TargetConfig{
			Name: configured.Name, Upstream: configured.Upstream, Headers: expandHeaderValues(configured.Headers), Engine: engine,
		})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no enabled MCP targets")
	}
	return targets, nil
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

func registerEnvProvider(registry *provider.Registry, providerType string, envKey string) {
	if key := os.Getenv(envKey); key != "" {
		if p := newProvider(providerType, key); p != nil {
			registry.Register(p)
			log.Printf("registered provider: %s", p.Name())
		}
	}
}

func registerProviderAccount(registry *provider.Registry, account gatewayconfig.ProviderAccount) {
	providerType := strings.ToLower(strings.TrimSpace(account.Type))

	// Custom or generic OpenAI-compatible provider with explicit base_url.
	if providerType == "custom" || account.BaseURL != "" {
		envKey := strings.TrimSpace(account.APIKeyEnv)
		apiKey := ""
		if envKey != "" {
			apiKey = os.Getenv(envKey)
		}
		var opts []provider.CustomOption
		if len(account.Models) > 0 {
			opts = append(opts, provider.WithCustomModels(account.Models))
		}
		if len(account.ExtraHeaders) > 0 {
			opts = append(opts, provider.WithCustomHeaders(expandHeaderValues(account.ExtraHeaders)))
		}
		baseURL := account.BaseURL
		if baseURL == "" {
			log.Printf("skipping custom provider %q: base_url is required for type=custom", account.Name)
			return
		}
		custom := provider.NewCustom(account.Name, baseURL, apiKey, opts...)
		registry.Register(custom)
		log.Printf("registered custom provider: %s (%s)", custom.Name(), baseURL)
		return
	}

	// Standard provider account with alias.
	envKey := strings.TrimSpace(account.APIKeyEnv)
	if envKey == "" {
		envKey = defaultProviderKeyEnv(account.Type)
	}
	apiKey := os.Getenv(envKey)
	if apiKey == "" {
		log.Printf("skipping provider account %q: env %s is not set", account.Name, envKey)
		return
	}
	inner := newProvider(account.Type, apiKey)
	if inner == nil {
		log.Printf("skipping provider account %q: unsupported provider type %q", account.Name, account.Type)
		return
	}
	alias := provider.NewAlias(account.Name, inner)
	registry.Register(alias)
	log.Printf("registered provider account: %s (%s)", alias.Name(), inner.Name())
}

func registerVaultProviders(registry *provider.Registry, vaultManager *vault.Vault) {
	if vaultManager == nil {
		return
	}
	keys, err := vaultManager.List(context.Background())
	if err != nil {
		log.Printf("failed to list virtual provider keys: %v", err)
		return
	}
	for _, key := range keys {
		if !key.Enabled {
			continue
		}
		providerKey, providerType, baseURL, err := vaultManager.Resolve(context.Background(), key.Name)
		if err != nil {
			log.Printf("skipping virtual key %q: %v", key.Name, err)
			continue
		}
		providerName := key.ProviderName
		if providerName == "" {
			providerName = key.Name
		}
		providerType = strings.ToLower(strings.TrimSpace(providerType))
		if baseURL != "" || providerType == "custom" {
			opts := []provider.CustomOption{}
			if models := parseCSV(key.Metadata["models"]); len(models) > 0 {
				opts = append(opts, provider.WithCustomModels(models))
			}
			if authHeader := strings.TrimSpace(key.Metadata["auth_header"]); authHeader != "" && providerKey != "" {
				opts = append(opts, provider.WithCustomHeaders(map[string]string{authHeader: providerKey}))
			}
			custom := provider.NewCustom(providerName, baseURL, providerKey, opts...)
			registry.Register(custom)
			log.Printf("registered vaulted provider: %s (%s)", custom.Name(), baseURL)
			continue
		}
		inner := newProvider(providerType, providerKey)
		if inner == nil {
			log.Printf("skipping virtual key %q: unsupported provider type %q", key.Name, providerType)
			continue
		}
		alias := provider.NewAlias(providerName, inner)
		registry.Register(alias)
		log.Printf("registered vaulted provider account: %s (%s)", alias.Name(), inner.Name())
	}
}

func newProvider(providerType string, apiKey string) provider.Provider {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "openai":
		return provider.NewOpenAI(apiKey)
	case "anthropic":
		return provider.NewAnthropic(apiKey)
	case "google", "gemini":
		return provider.NewGoogle(apiKey)
	case "groq":
		return provider.NewGroq(apiKey)
	default:
		return nil
	}
}

func defaultProviderKeyEnv(providerType string) string {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "google", "gemini":
		return "GOOGLE_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	default:
		return ""
	}
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

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func banner() string {
	return `
  ____             _   _            _
 / ___|  ___ _ __ | |_(_)_ __   ___| |
 \___ \ / _ \ '_ \| __| | '_ \ / _ \ |
  ___) |  __/ | | | |_| | | | |  __/ |
 |____/ \___|_| |_|\__|_|_| |_|\___|_|

  AI Gateway - Policy | Guardrails | Observability
`
}
