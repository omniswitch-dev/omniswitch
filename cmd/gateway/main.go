package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
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
	dsn, err := resolveStorageDSN(os.Getenv("OMNISWITCH_DATABASE_URL"), settings)
	if err != nil {
		log.Fatalf("invalid storage settings: %v", err)
	}
	if dsn != "" {
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
	defer st.Close()
	// Runs until main returns; the deferred stop runs before st.Close.
	maintenanceCtx, stopMaintenance := context.WithCancel(context.Background())
	maintenanceDone := startMaintenance(maintenanceCtx, st, settings.logRetentionDays, maintenanceInterval)
	defer func() {
		stopMaintenance()
		<-maintenanceDone
	}()
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
	// Tool rules, webhooks and LLM judges count too: a webhook-only
	// configuration must be enforced from the first request.
	gr := guardrail.NewEngineWithConfig(settings.guardrailConfig)
	gw := gateway.New(registry, rtr, st, gr)
	gw.SetSemanticCache(settings.cacheThreshold)
	gw.SetCacheTTL(settings.cacheTTL)
	gw.SetCacheScope(settings.cacheScope)
	gw.SetLogPayloads(settings.logPayloads)
	gw.SetStreamGuardrailBuffer(settings.guardrailStreamBuffer)
	gw.SetMaxRequestBytes(settings.maxRequestBytes)
	gw.SetShadowProvider(settings.shadowProvider)
	gw.SetAlerts(settings.alerts)
	gw.SetBudgetFailOpen(settings.budgetFailOpen)
	gw.SetBudgetRefreshInterval(settings.budgetRefreshInterval)
	if settings.configPath != "" {
		startConfigReloader(settings.configPath, rtr, gw, settings)
		log.Printf("config hot-reload enabled for %s", settings.configPath)
	}
	adminHandler := admin.New(st)
	feedbackHandler := feedback.New(st)
	promptHandler := prompt.New(st)
	orgHandler := org.New(st)
	evalHandler := eval.NewHandler(st)
	evalHandler.SetPolicyRoot(settings.policyDir)
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
	registerPublicMetrics(topMux, settings, dash)

	// Print startup banner.
	fmt.Println(banner())
	log.Printf("OmniSwitch AI Gateway listening on %s", settings.listenAddr)
	baseURL := displayBaseURL(settings.listenAddr)
	if warning := insecureListenWarning(settings); warning != "" {
		log.Print(warning)
	}
	log.Printf("Dashboard: %s", baseURL)
	log.Printf("Gateway:   %s/v1/chat/completions", baseURL)
	if settings.mcpEnabled {
		log.Printf("MCP:       %s/mcp", baseURL)
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
const bootstrapKeyID = "bootstrap-admin"

// hasEnabledAdminKey reports whether keys include an enabled, unexpired owner
// or admin key.
func hasEnabledAdminKey(keys []store.APIKey, now time.Time) bool {
	for _, key := range keys {
		if !key.Enabled || (key.ExpiresAt != nil && !key.ExpiresAt.After(now)) {
			continue
		}
		if role := gateway.NormalizeRole(key.Role); role == "owner" || role == "admin" {
			return true
		}
	}
	return false
}

// ensureBootstrapKey creates the bootstrap owner key from
// OMNISWITCH_BOOTSTRAP_API_KEY on first start. Later starts leave an existing
// row alone except to rotate its hash when the secret changes; a disabled row
// stays disabled. If the row is absent but another enabled owner or admin key
// exists, nothing is created.
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
		keyHash := hex.EncodeToString(hash[:])
		existing, err := st.GetAPIKeyByID(ctx, bootstrapKeyID)
		switch {
		case err == nil:
			// The row already exists. Never re-enable it or reset its spend:
			// an operator who disabled it through the API meant it.
			if !existing.Enabled {
				log.Printf("WARNING: bootstrap key %q is disabled and will not be re-enabled; remove OMNISWITCH_BOOTSTRAP_API_KEY", bootstrapKeyID)
				return nil
			}
			if existing.KeyHash == keyHash {
				log.Printf("bootstrap key %q already exists; OMNISWITCH_BOOTSTRAP_API_KEY can be removed", bootstrapKeyID)
				return nil
			}
			// The secret changed: rotate the hash and keep everything else.
			existing.KeyHash = keyHash
			existing.KeyPrefix = prefix + "..."
			if err := st.InsertAPIKey(ctx, existing); err != nil {
				return fmt.Errorf("rotate bootstrap key: %w", err)
			}
			log.Printf("rotated bootstrap key %q from OMNISWITCH_BOOTSTRAP_API_KEY", bootstrapKeyID)
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("look up bootstrap key: %w", err)
		}
		keys, err := st.ListAPIKeys(ctx)
		if err != nil {
			return fmt.Errorf("list existing API keys: %w", err)
		}
		if hasEnabledAdminKey(keys, time.Now()) {
			log.Printf("an enabled owner or admin key already exists; not creating bootstrap key %q. Remove OMNISWITCH_BOOTSTRAP_API_KEY", bootstrapKeyID)
			return nil
		}
		return st.InsertAPIKey(ctx, store.APIKey{
			ID:          bootstrapKeyID,
			Name:        "Bootstrap administrator",
			KeyHash:     keyHash,
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
