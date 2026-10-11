package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/admin"
	"github.com/omniswitch-dev/omniswitch/internal/audit"
	"github.com/omniswitch-dev/omniswitch/internal/dashboard"
	"github.com/omniswitch-dev/omniswitch/internal/eval"
	"github.com/omniswitch-dev/omniswitch/internal/feedback"
	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/org"
	"github.com/omniswitch-dev/omniswitch/internal/policy"
	"github.com/omniswitch-dev/omniswitch/internal/prompt"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	mcpproxy "github.com/omniswitch-dev/omniswitch/internal/proxy"
	"github.com/omniswitch-dev/omniswitch/internal/store"
	"github.com/omniswitch-dev/omniswitch/internal/vault"
)

// routeHandlers holds the handlers behind the authenticated gateway mux.
// mcp is nil when MCP is disabled.
type routeHandlers struct {
	settings runtimeSettings
	registry *provider.Registry
	gateway  *gateway.Handler
	admin    *admin.Handler
	feedback *feedback.Handler
	prompt   *prompt.Handler
	org      *org.Handler
	eval     *eval.Handler
	vault    *vault.Handler
	dash     *dashboard.Handler
	mcp      *mcpproxy.Handler
}

// newGatewayMux registers every route that sits behind the CORS, logging,
// authentication, authorization, and rate-limit middleware.
func newGatewayMux(h routeHandlers) *http.ServeMux {
	mux := http.NewServeMux()
	registerGatewayAPIRoutes(mux, h.gateway)
	registerInfoRoutes(mux, h.settings, h.registry)
	if h.mcp != nil {
		mux.Handle("/mcp", h.mcp)
		mux.Handle("/v1/mcp/tools/call", h.mcp)
	}
	registerAdminRoutes(mux, h.admin, h.vault)
	registerFeedbackRoutes(mux, h.feedback)
	registerOrgRoutes(mux, h.org)
	registerPromptRoutes(mux, h.prompt)
	registerEvalRoutes(mux, h.eval)

	// Dashboard API + static files.
	h.dash.RegisterRoutes(mux)
	if h.settings.prometheusEnabled {
		h.dash.RegisterPrometheus(mux)
	}
	return mux
}

// registerGatewayAPIRoutes registers the OpenAI-compatible, Anthropic
// Messages, and A2A endpoints.
func registerGatewayAPIRoutes(mux *http.ServeMux, gw *gateway.Handler) {
	mux.HandleFunc("/v1/chat/completions", gw.ChatCompletions)
	mux.HandleFunc("/v1/responses", gw.Responses)
	mux.HandleFunc("/v1/messages", gw.Messages)
	mux.HandleFunc("/v1/embeddings", gw.Embeddings)
	mux.HandleFunc("/v1/moderations", gw.Moderations)
	mux.HandleFunc("/v1/rerank", gw.Rerank)
	mux.HandleFunc("/v1/models", gw.ListModels)
	mux.HandleFunc("/.well-known/agent-card.json", gw.A2AAgentCard)
	mux.HandleFunc("/a2a", gw.A2A)
}

// registerInfoRoutes registers the read-only provider, config, and version
// endpoints.
func registerInfoRoutes(mux *http.ServeMux, settings runtimeSettings, registry *provider.Registry) {
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
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"version": version, "commit": commit, "built": date})
	})
}

// registerAdminRoutes registers the gateway API key and virtual key
// endpoints.
func registerAdminRoutes(mux *http.ServeMux, adminHandler *admin.Handler, vaultHandler *vault.Handler) {
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
}

// registerFeedbackRoutes registers the request feedback endpoints.
func registerFeedbackRoutes(mux *http.ServeMux, feedbackHandler *feedback.Handler) {
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
}

// registerOrgRoutes registers the organization, workspace, user, and
// membership endpoints.
func registerOrgRoutes(mux *http.ServeMux, orgHandler *org.Handler) {
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
}

// registerPromptRoutes registers the prompt registry endpoints.
func registerPromptRoutes(mux *http.ServeMux, promptHandler *prompt.Handler) {
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
}

// registerEvalRoutes registers the policy replay endpoint.
func registerEvalRoutes(mux *http.ServeMux, evalHandler *eval.Handler) {
	mux.HandleFunc("/api/evals/policy", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		evalHandler.ReplayPolicies(w, r)
	})
}

// wrapGatewayMiddleware wraps mux with CORS, request logging,
// authentication, authorization, and rate limiting, outermost first. The
// returned function releases the rate limiter backend.
func wrapGatewayMiddleware(settings runtimeSettings, st *store.Store, mux http.Handler) (http.Handler, func(), error) {
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
			return nil, nil, fmt.Errorf("failed to initialize OIDC identity: %w", err)
		}
		auth.SetTokenAuthenticator(oidcAuthenticator)
	}
	authorize := gateway.NewAuthorizationMiddleware(settings.authEnabled)
	authorizationPolicy, err := gateway.NewAuthorizationPolicy(settings.authorizationRules)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize authorization policy: %w", err)
	}
	authorize.SetPolicy(authorizationPolicy)
	rateLimiter, closeRateLimiter, err := buildRateLimiter(settings)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize rate limiter: %w", err)
	}
	handler := gateway.CORSMiddlewareWithOrigins(settings.corsOrigins, gateway.LoggingMiddleware(auth.Wrap(authorize.Wrap(rateLimiter.Wrap(mux)))))
	return handler, closeRateLimiter, nil
}

// newTopMux serves /healthz and /readyz ahead of the auth, authorization,
// and rate-limit chain so liveness and readiness probes never need
// credentials and are never rate limited. Every other path goes to handler.
func newTopMux(health *healthState, handler http.Handler) *http.ServeMux {
	topMux := http.NewServeMux()
	health.registerHealthRoutes(topMux)
	topMux.Handle("/", handler)
	return topMux
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

// buildMCPHandler loads the MCP policy and targets and returns the MCP proxy
// handler. The caller must Close it on shutdown.
func buildMCPHandler(settings runtimeSettings, st *store.Store) (*mcpproxy.Handler, error) {
	resolvedPolicyPath, policySource, err := resolveMCPPolicyPath(settings)
	if err != nil {
		return nil, fmt.Errorf("failed to load MCP policy: %w", err)
	}
	log.Printf("MCP policy: using %s (%s)", resolvedPolicyPath, policySource)
	mcpEngine, err := policy.NewEngineFromFiles(resolvedPolicyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load MCP policy: %w", err)
	}
	mcpAuditor := audit.NewMultiLogger(audit.NewStdoutLogger(os.Stdout), audit.NewStoreLogger(st))
	mcpTargets, err := buildMCPTargets(mcpEngine, settings)
	if err != nil {
		return nil, fmt.Errorf("failed to configure MCP targets: %w", err)
	}
	mcpHandler, err := mcpproxy.NewMultiHandler(mcpEngine, mcpAuditor, mcpTargets)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize MCP gateway: %w", err)
	}
	return mcpHandler, nil
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
			OpenAPISpec:        os.ExpandEnv(configured.OpenAPISpec),
		})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no enabled MCP targets")
	}
	return targets, nil
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
