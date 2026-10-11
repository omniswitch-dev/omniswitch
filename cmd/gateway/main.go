package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/admin"
	"github.com/omniswitch-dev/omniswitch/internal/dashboard"
	"github.com/omniswitch-dev/omniswitch/internal/eval"
	"github.com/omniswitch-dev/omniswitch/internal/feedback"
	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/org"
	"github.com/omniswitch-dev/omniswitch/internal/prompt"
	mcpproxy "github.com/omniswitch-dev/omniswitch/internal/proxy"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
	"github.com/omniswitch-dev/omniswitch/internal/telemetry"
	"github.com/omniswitch-dev/omniswitch/internal/vault"
)

// Injected at build time by goreleaser.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	action, code := dispatch(os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	if action == actionExit {
		os.Exit(code)
	}
	runServe()
}

// runServe builds every gateway component and blocks serving HTTP until it
// receives SIGINT/SIGTERM, then drains in-flight requests (including SSE
// streams) before exiting.
func runServe() {
	log.Printf("OmniSwitch %s (commit %s, built %s)", version, commit, date)
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

	// Initialize store. OMNISWITCH_DATABASE_URL takes precedence over
	// gateway.storage_url from the config file so an operator can always
	// override storage without editing config-as-code.
	var st *store.Store
	dsn := strings.TrimSpace(os.Getenv("OMNISWITCH_DATABASE_URL"))
	if dsn == "" {
		dsn = strings.TrimSpace(settings.storageURL)
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		st, err = store.OpenPostgres(dsn)
		if err != nil {
			log.Fatalf("failed to connect to postgres: %v", err)
		}
		log.Printf("using PostgreSQL storage (shared state across replicas)")
	} else {
		st, err = store.New(settings.dataDir)
		if err != nil {
			log.Fatalf("failed to initialize store: %v", err)
		}
	}
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
	registry := buildProviderRegistry(settings, vaultManager)

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
	gw.SetAlerts(settings.alerts)
	if settings.configPath != "" {
		startConfigReloader(settings.configPath, rtr, gw, settings)
		log.Printf("config hot-reload enabled for %s", settings.configPath)
	}
	adminHandler := admin.New(st)
	feedbackHandler := feedback.New(st)
	promptHandler := prompt.New(st)
	orgHandler := org.New(st)
	evalHandler := eval.NewHandler(st)
	vaultHandler := vault.NewHandler(vaultManager)
	dash := dashboard.New(st)
	dash.SetConfigPath(settings.configPath)

	var mcpHandler *mcpproxy.Handler
	if settings.mcpEnabled {
		mcpHandler, err = buildMCPHandler(settings, st)
		if err != nil {
			log.Fatal(err)
		}
		defer mcpHandler.Close()
	}

	// Build mux and wrap it with middleware.
	mux := newGatewayMux(routeHandlers{
		settings: settings,
		registry: registry,
		gateway:  gw,
		admin:    adminHandler,
		feedback: feedbackHandler,
		prompt:   promptHandler,
		org:      orgHandler,
		eval:     evalHandler,
		vault:    vaultHandler,
		dash:     dash,
		mcp:      mcpHandler,
	})
	handler, closeRateLimiter, err := wrapGatewayMiddleware(settings, st, mux)
	if err != nil {
		log.Fatal(err)
	}
	defer closeRateLimiter()
	topMux := newTopMux(newHealthState(st, registry, settings.allowNoProviders), handler)

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
		Handler:           topMux,
		ReadHeaderTimeout: settings.readHeaderTimeout,
		ReadTimeout:       settings.readTimeout,
		WriteTimeout:      settings.writeTimeout,
		IdleTimeout:       settings.idleTimeout,
	}

	runWithGracefulShutdown(server, settings.shutdownTimeout)
}

// runWithGracefulShutdown starts server and blocks until it stops. On
// SIGINT/SIGTERM it calls server.Shutdown with the given drain timeout so
// in-flight requests, including long-lived SSE streams, get a chance to
// finish instead of being cut off. It only calls log.Fatalf for a genuine
// startup/listen failure; a clean shutdown returns normally so the process
// exits 0.
func runWithGracefulShutdown(server *http.Server, shutdownTimeout time.Duration) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server failed: %v", err)
		}
	case <-ctx.Done():
		log.Printf("shutdown signal received, draining for up to %s", shutdownTimeout)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown did not complete cleanly: %v", err)
		}
		if err := <-serveErr; err != nil && err != http.ErrServerClosed {
			log.Printf("server reported an error while shutting down: %v", err)
		}
		log.Printf("shutdown complete")
	}
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

func banner() string {
	return `
   ___                  _ ____            _ _       _
  / _ \ _ __ ___  _ __ (_) ___|_      _(_) |_ ___| |__
 | | | | '_ ` + "`" + ` _ \| '_ \| \___ \ \ /\ / / | __/ __| '_ \
 | |_| | | | | | | | | | |___) \ V  V /| | || (__| | | |
  \___/|_| |_| |_|_| |_|_|____/ \_/\_/ |_|\__\___|_| |_|

  OmniSwitch AI Gateway - Routing | Guardrails | MCP | Observability
`
}
