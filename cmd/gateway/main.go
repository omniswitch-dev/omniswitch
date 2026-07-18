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
	"sort"
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
	mux.HandleFunc("/v1/moderations", gw.Moderations)
	mux.HandleFunc("/v1/rerank", gw.Rerank)
	mux.HandleFunc("/v1/models", gw.ListModels)
	mux.HandleFunc("/.well-known/agent-card.json", gw.A2AAgentCard)
	mux.HandleFunc("/a2a", gw.A2A)
	mux.HandleFunc("/api/providers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"providers": registry.Names(), "models": registry.AllModels()})
	})
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, runtimeConfigSummary(settings, registry))
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
		defer mcpHandler.Close()
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
	if settings.oidcJWKSURL != "" {
		oidcAuthenticator, err := gateway.NewJWTAuthenticator(gateway.JWTAuthenticatorConfig{
			JWKSURL:           settings.oidcJWKSURL,
			Issuer:            settings.oidcIssuer,
			Audience:          settings.oidcAudience,
			RoleClaim:         settings.oidcRoleClaim,
			WorkspaceClaim:    settings.oidcWorkspaceClaim,
			OrganizationClaim: settings.oidcOrganizationClaim,
			CacheTTL:          settings.oidcCacheTTL,
		})
		if err != nil {
			log.Fatalf("failed to initialize OIDC identity: %v", err)
		}
		auth.SetTokenAuthenticator(oidcAuthenticator)
	}
	authorize := gateway.NewAuthorizationMiddleware(settings.authEnabled)
	authorizationPolicy, err := gateway.NewAuthorizationPolicy(settings.authorizationRules)
	if err != nil {
		log.Fatalf("failed to initialize authorization policy: %v", err)
	}
	authorize.SetPolicy(authorizationPolicy)
	rateLimiter, closeRateLimiter, err := buildRateLimiter(settings)
	if err != nil {
		log.Fatalf("failed to initialize rate limiter: %v", err)
	}
	defer closeRateLimiter()
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
	mcpEnabled             bool
	mcpPolicyPath          string
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
		mcpEnabled:        true,
		mcpPolicyPath:     "policies/production-delete.yaml",
		mcpUpstreamURL:    "http://127.0.0.1:8090/mcp",
		routes:            map[string]router.Route{},
		oidcCacheTTL:      5 * time.Minute,
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
	settings.mcpPolicyPath = env("OMNISWITCH_MCP_POLICY", settings.mcpPolicyPath)
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
	if len(keys) == 0 && settings.oidcJWKSURL == "" {
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
	if cfg.Guardrails.Rules != nil {
		settings.guardrailConfig.Rules = make([]guardrail.Rule, 0, len(cfg.Guardrails.Rules))
		for _, rule := range cfg.Guardrails.Rules {
			settings.guardrailConfig.Rules = append(settings.guardrailConfig.Rules, guardrail.Rule{
				Name: rule.Name, Stage: rule.Stage, Pattern: rule.Pattern, Action: rule.Action, Message: rule.Message,
			})
		}
	}
	if cfg.Guardrails.Webhooks != nil {
		settings.guardrailConfig.Webhooks = make([]guardrail.Webhook, 0, len(cfg.Guardrails.Webhooks))
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
			settings.guardrailConfig.Webhooks = append(settings.guardrailConfig.Webhooks, configured)
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

func buildRateLimiter(settings runtimeSettings) (*gateway.RateLimiter, func(), error) {
	if strings.TrimSpace(settings.rateLimitRedisURL) == "" {
		return gateway.NewRateLimiter(settings.rateLimitRequests, settings.rateLimitWindow), func() {}, nil
	}

	backend, err := gateway.NewRedisRateLimitBackend(settings.rateLimitRedisURL, settings.rateLimitPrefix)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := backend.Ping(ctx); err != nil {
		_ = backend.Close()
		return nil, nil, err
	}
	return gateway.NewRateLimiterWithBackend(
			settings.rateLimitRequests,
			settings.rateLimitWindow,
			backend,
			settings.rateLimitFailOpen,
		), func() {
			if err := backend.Close(); err != nil {
				log.Printf("failed to close rate limit Redis client: %v", err)
			}
		}, nil
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
			Name: configured.Name, Transport: configured.Transport, Upstream: configured.Upstream,
			Command: os.ExpandEnv(configured.Command), Args: expandStrings(configured.Args),
			Environment: expandHeaderValues(configured.Environment), Headers: expandHeaderValues(configured.Headers), Engine: engine,
			ForwardBearerToken: configured.ForwardBearerToken != nil && *configured.ForwardBearerToken,
		})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no enabled MCP targets")
	}
	return targets, nil
}

func runtimeConfigSummary(settings runtimeSettings, registry *provider.Registry) map[string]any {
	providerNames := []string{}
	modelCount := 0
	if registry != nil {
		providerNames = registry.Names()
		sort.Strings(providerNames)
		modelCount = len(registry.AllModels())
	}
	return map[string]any{
		"config_path": settings.configPath,
		"gateway": map[string]any{
			"listen":                   settings.listenAddr,
			"auth_enabled":             settings.authEnabled,
			"cache_threshold":          settings.cacheThreshold,
			"cache_ttl":                settings.cacheTTL.String(),
			"cache_scope":              settings.cacheScope,
			"log_payloads":             settings.logPayloads,
			"cors_origins":             append([]string(nil), settings.corsOrigins...),
			"max_request_bytes":        settings.maxRequestBytes,
			"read_header_timeout":      settings.readHeaderTimeout.String(),
			"read_timeout":             settings.readTimeout.String(),
			"write_timeout":            settings.writeTimeout.String(),
			"idle_timeout":             settings.idleTimeout.String(),
			"circuit_breaker_failures": settings.circuitBreakerFailures,
			"circuit_breaker_cooldown": settings.circuitBreakerCooldown.String(),
			"shadow_provider":          settings.shadowProvider,
		},
		"rate_limit": map[string]any{
			"requests":  settings.rateLimitRequests,
			"window":    settings.rateLimitWindow.String(),
			"backend":   rateLimitBackendName(settings),
			"prefix":    settings.rateLimitPrefix,
			"fail_open": settings.rateLimitFailOpen,
		},
		"identity": map[string]any{
			"api_keys_enabled": settings.authEnabled,
			"oidc": map[string]any{
				"enabled":            settings.oidcJWKSURL != "",
				"issuer":             settings.oidcIssuer,
				"audience":           settings.oidcAudience,
				"role_claim":         settings.oidcRoleClaim,
				"workspace_claim":    settings.oidcWorkspaceClaim,
				"organization_claim": settings.oidcOrganizationClaim,
				"cache_ttl":          settings.oidcCacheTTL.String(),
			},
		},
		"authorization": map[string]any{
			"rule_count": len(settings.authorizationRules),
			"rules":      authorizationRuleSummaries(settings.authorizationRules),
		},
		"guardrails": map[string]any{
			"stream_buffer":  settings.guardrailStreamBuffer,
			"actions":        cloneStringMap(settings.guardrailConfig.Actions),
			"rule_count":     len(settings.guardrailConfig.Rules),
			"rules":          guardrailRuleSummaries(settings.guardrailConfig.Rules),
			"webhook_count":  len(settings.guardrailConfig.Webhooks),
			"webhooks":       guardrailWebhookSummaries(settings.guardrailConfig.Webhooks),
			"moderation_api": true,
		},
		"providers": map[string]any{
			"names":       providerNames,
			"model_count": modelCount,
		},
		"routes": routeSummaries(settings.routes),
		"mcp": map[string]any{
			"enabled": settings.mcpEnabled,
			"targets": mcpTargetSummaries(settings),
		},
		"a2a": map[string]any{
			"enabled":            true,
			"agent_card_path":    "/.well-known/agent-card.json",
			"jsonrpc_path":       "/a2a",
			"methods":            []string{"SendMessage", "GetExtendedAgentCard"},
			"task_lifecycle":     false,
			"streaming":          false,
			"push_notifications": false,
		},
	}
}

func rateLimitBackendName(settings runtimeSettings) string {
	if strings.TrimSpace(settings.rateLimitRedisURL) != "" {
		return "redis"
	}
	return "local"
}

func authorizationRuleSummaries(rules []gateway.AuthorizationRule) []map[string]string {
	summaries := make([]map[string]string, 0, len(rules))
	for _, rule := range rules {
		summaries = append(summaries, map[string]string{
			"name":   rule.Name,
			"effect": rule.Effect,
		})
	}
	return summaries
}

func guardrailRuleSummaries(rules []guardrail.Rule) []map[string]string {
	summaries := make([]map[string]string, 0, len(rules))
	for _, rule := range rules {
		summaries = append(summaries, map[string]string{
			"name":   rule.Name,
			"stage":  rule.Stage,
			"action": rule.Action,
		})
	}
	return summaries
}

func guardrailWebhookSummaries(webhooks []guardrail.Webhook) []map[string]any {
	summaries := make([]map[string]any, 0, len(webhooks))
	for _, webhook := range webhooks {
		summaries = append(summaries, map[string]any{
			"name":      webhook.Name,
			"stage":     webhook.Stage,
			"action":    webhook.Action,
			"timeout":   webhook.Timeout.String(),
			"fail_open": webhook.FailOpen,
		})
	}
	return summaries
}

func routeSummaries(routes map[string]router.Route) []map[string]any {
	names := make([]string, 0, len(routes))
	for name := range routes {
		names = append(names, name)
	}
	sort.Strings(names)
	summaries := make([]map[string]any, 0, len(names))
	for _, name := range names {
		route := routes[name]
		summaries = append(summaries, map[string]any{
			"model":           name,
			"provider":        route.Provider,
			"fallbacks":       append([]string(nil), route.Fallbacks...),
			"max_retries":     route.MaxRetries,
			"retry_backoff":   route.RetryBackoff,
			"retry_codes":     append([]int(nil), route.RetryCodes...),
			"timeout":         route.Timeout,
			"shadow_provider": route.ShadowProvider,
			"default_params":  cloneAnyMap(route.DefaultParams),
			"override_params": cloneAnyMap(route.OverrideParams),
			"drop_params":     append([]string(nil), route.DropParams...),
			"variants":        append([]router.Variant(nil), route.Variants...),
		})
	}
	return summaries
}

func mcpTargetSummaries(settings runtimeSettings) []map[string]any {
	if len(settings.mcpTargets) == 0 {
		if strings.TrimSpace(settings.mcpUpstreamURL) == "" {
			return []map[string]any{}
		}
		return []map[string]any{{
			"name":                   "default",
			"transport":              "http",
			"enabled":                settings.mcpEnabled,
			"policy_configured":      strings.TrimSpace(settings.mcpPolicyPath) != "",
			"headers_configured":     false,
			"environment_configured": false,
			"forward_bearer_token":   false,
		}}
	}
	summaries := make([]map[string]any, 0, len(settings.mcpTargets))
	for _, target := range settings.mcpTargets {
		transport := strings.TrimSpace(target.Transport)
		if transport == "" {
			transport = "http"
		}
		enabled := true
		if target.Enabled != nil {
			enabled = *target.Enabled
		}
		forwardBearer := false
		if target.ForwardBearerToken != nil {
			forwardBearer = *target.ForwardBearerToken
		}
		summaries = append(summaries, map[string]any{
			"name":                   target.Name,
			"transport":              transport,
			"enabled":                enabled,
			"policy_configured":      strings.TrimSpace(target.Policy) != "",
			"headers_configured":     len(target.Headers) > 0,
			"environment_configured": len(target.Environment) > 0,
			"forward_bearer_token":   forwardBearer,
		})
	}
	return summaries
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
