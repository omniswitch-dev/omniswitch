package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"sentinel/internal/admin"
	"sentinel/internal/audit"
	"sentinel/internal/dashboard"
	"sentinel/internal/eval"
	"sentinel/internal/feedback"
	"sentinel/internal/gateway"
	"sentinel/internal/gatewayconfig"
	"sentinel/internal/guardrail"
	"sentinel/internal/org"
	"sentinel/internal/policy"
	"sentinel/internal/prompt"
	"sentinel/internal/provider"
	mcpproxy "sentinel/internal/proxy"
	"sentinel/internal/router"
	"sentinel/internal/store"
)

func main() {
	settings, err := loadRuntimeSettings()
	if err != nil {
		log.Fatalf("failed to load gateway settings: %v", err)
	}
	if settings.configPath != "" {
		log.Printf("loaded gateway config: %s", settings.configPath)
	}

	// Initialize store.
	st, err := store.New(settings.dataDir)
	if err != nil {
		log.Fatalf("failed to initialize store: %v", err)
	}
	defer st.Close()

	// Initialize provider registry.
	registry := provider.NewRegistry()
	registerEnvProvider(registry, "openai", "OPENAI_API_KEY")
	registerEnvProvider(registry, "anthropic", "ANTHROPIC_API_KEY")
	registerEnvProvider(registry, "google", "GOOGLE_API_KEY")
	registerEnvProvider(registry, "groq", "GROQ_API_KEY")
	for _, account := range settings.providerAccounts {
		registerProviderAccount(registry, account)
	}

	if len(registry.Names()) == 0 {
		log.Println("WARNING: No provider API keys configured. Set OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLE_API_KEY, or GROQ_API_KEY.")
	}

	// Initialize components.
	rtr := router.New(registry)
	for model, route := range settings.routes {
		rtr.SetRoute(model, route)
		log.Printf("configured route for %s: %+v", model, route)
	}
	gr := guardrail.NewEngine()
	gw := gateway.New(registry, rtr, st, gr)
	gw.SetSemanticCache(settings.cacheThreshold)
	gw.SetCacheTTL(settings.cacheTTL)
	gw.SetShadowProvider(settings.shadowProvider)
	adminHandler := admin.New(st)
	feedbackHandler := feedback.New(st)
	promptHandler := prompt.New(st)
	orgHandler := org.New(st)
	evalHandler := eval.New()
	dash := dashboard.New(st)

	// Build mux.
	mux := http.NewServeMux()

	// Gateway API (OpenAI-compatible).
	mux.HandleFunc("/v1/chat/completions", gw.ChatCompletions)
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
		mcpHandler, err := mcpproxy.NewHandler(mcpEngine, mcpAuditor, settings.mcpUpstreamURL)
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

	// Wrap with middleware.
	auth := gateway.NewAuthMiddleware(st, settings.authEnabled)
	rateLimiter := gateway.NewRateLimiter(120, time.Minute)
	handler := gateway.CORSMiddleware(gateway.LoggingMiddleware(auth.Wrap(rateLimiter.Wrap(mux))))

	// Print startup banner.
	fmt.Println(banner())
	log.Printf("Sentinel AI Gateway listening on %s", settings.listenAddr)
	log.Printf("Dashboard: http://localhost%s", settings.listenAddr)
	log.Printf("Gateway:   http://localhost%s/v1/chat/completions", settings.listenAddr)
	if settings.mcpEnabled {
		log.Printf("MCP:       http://localhost%s/mcp", settings.listenAddr)
	}
	log.Printf("Providers: %v", registry.Names())

	if err := http.ListenAndServe(settings.listenAddr, handler); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

type runtimeSettings struct {
	listenAddr       string
	dataDir          string
	authEnabled      bool
	cacheThreshold   float64
	cacheTTL         time.Duration
	shadowProvider   string
	mcpEnabled       bool
	mcpPolicyPath    string
	mcpUpstreamURL   string
	routes           map[string]router.Route
	providerAccounts []gatewayconfig.ProviderAccount
	configPath       string
}

func loadRuntimeSettings() (runtimeSettings, error) {
	settings := runtimeSettings{
		listenAddr:     ":8080",
		dataDir:        ".",
		cacheThreshold: 0.95,
		cacheTTL:       24 * time.Hour,
		mcpEnabled:     true,
		mcpPolicyPath:  "policies/production-delete.yaml",
		mcpUpstreamURL: "http://127.0.0.1:8090/mcp",
		routes:         map[string]router.Route{},
	}

	if configPath := strings.TrimSpace(os.Getenv("SENTINEL_CONFIG")); configPath != "" {
		cfg, err := gatewayconfig.LoadFile(configPath)
		if err != nil {
			return settings, err
		}
		settings.configPath = configPath
		applyGatewayConfig(&settings, cfg)
	}

	settings.listenAddr = env("SENTINEL_LISTEN", settings.listenAddr)
	settings.dataDir = env("SENTINEL_DATA", settings.dataDir)
	settings.authEnabled = envBool("SENTINEL_AUTH", settings.authEnabled)
	settings.cacheThreshold = envFloat("SENTINEL_CACHE_THRESHOLD", settings.cacheThreshold)
	settings.cacheTTL = envDuration("SENTINEL_CACHE_TTL", settings.cacheTTL)
	settings.shadowProvider = env("SENTINEL_SHADOW_PROVIDER", settings.shadowProvider)
	settings.mcpEnabled = envBool("SENTINEL_MCP_ENABLED", settings.mcpEnabled)
	settings.mcpPolicyPath = env("SENTINEL_MCP_POLICY", settings.mcpPolicyPath)
	settings.mcpUpstreamURL = env("SENTINEL_MCP_UPSTREAM", settings.mcpUpstreamURL)
	for model, route := range parseABConfig(os.Getenv("SENTINEL_AB_TEST")) {
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
	if cfg.Gateway.ShadowProvider != "" {
		settings.shadowProvider = cfg.Gateway.ShadowProvider
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
	for model, route := range cfg.Routes {
		settings.routes[model] = route
	}
	settings.providerAccounts = append(settings.providerAccounts, cfg.Providers...)
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

func registerEnvProvider(registry *provider.Registry, providerType string, envKey string) {
	if key := os.Getenv(envKey); key != "" {
		if p := newProvider(providerType, key); p != nil {
			registry.Register(p)
			log.Printf("registered provider: %s", p.Name())
		}
	}
}

func registerProviderAccount(registry *provider.Registry, account gatewayconfig.ProviderAccount) {
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
