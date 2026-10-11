package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/admin"
	"github.com/omniswitch-dev/omniswitch/internal/dashboard"
	"github.com/omniswitch-dev/omniswitch/internal/eval"
	"github.com/omniswitch-dev/omniswitch/internal/feedback"
	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/org"
	"github.com/omniswitch-dev/omniswitch/internal/prompt"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/vault"
)

const routeTestKey = "sk-osw-route-test-secret"

// newRouteTestServer wires the same mux, middleware chain, and top-level
// mux that runServe builds, with authentication on and MCP off.
func newRouteTestServer(t *testing.T) http.Handler {
	t.Helper()
	st := newTestStore(t)
	settings := runtimeSettings{
		authEnabled:       true,
		bootstrapAPIKey:   routeTestKey,
		rateLimitRequests: 1000,
		rateLimitWindow:   time.Minute,
	}
	if err := ensureBootstrapKey(context.Background(), st, settings); err != nil {
		t.Fatalf("ensureBootstrapKey() error = %v", err)
	}
	registry := provider.NewRegistry()
	registry.Register(fakeHealthProvider{name: "fake"})
	gw := gateway.New(registry, router.New(registry), st, guardrail.NewEngine())
	mux := newGatewayMux(routeHandlers{
		settings: settings,
		registry: registry,
		gateway:  gw,
		admin:    admin.New(st),
		feedback: feedback.New(st),
		prompt:   prompt.New(st),
		org:      org.New(st),
		eval:     eval.NewHandler(st),
		vault:    vault.NewHandler(vault.New(st, "")),
		dash:     dashboard.New(st),
	})
	handler, closeRateLimiter, err := wrapGatewayMiddleware(settings, st, mux)
	if err != nil {
		t.Fatalf("wrapGatewayMiddleware() error = %v", err)
	}
	t.Cleanup(closeRateLimiter)
	return newTopMux(newHealthState(st, registry, false), handler)
}

func serveRoute(h http.Handler, method, path string, authenticated bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if authenticated {
		req.Header.Set("Authorization", "Bearer "+routeTestKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTopMuxServesProbesWithoutCredentials(t *testing.T) {
	h := newRouteTestServer(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		if rec := serveRoute(h, http.MethodGet, path, false); rec.Code != http.StatusOK {
			t.Errorf("GET %s without credentials = %d, want 200; body %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestGatewayRoutesRequireCredentials(t *testing.T) {
	h := newRouteTestServer(t)
	if rec := serveRoute(h, http.MethodGet, "/api/version", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/version without credentials = %d, want 401", rec.Code)
	}
	if rec := serveRoute(h, http.MethodGet, "/api/version", true); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/version with the bootstrap key = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
}

func TestGatewayMuxRegistersRoutes(t *testing.T) {
	h := newRouteTestServer(t)
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/models"},
		{http.MethodGet, "/v1/chat/completions"},
		{http.MethodGet, "/v1/responses"},
		{http.MethodGet, "/v1/messages"},
		{http.MethodGet, "/v1/embeddings"},
		{http.MethodGet, "/v1/moderations"},
		{http.MethodGet, "/v1/rerank"},
		{http.MethodGet, "/.well-known/agent-card.json"},
		{http.MethodGet, "/a2a"},
		{http.MethodGet, "/api/providers"},
		{http.MethodGet, "/api/config"},
		{http.MethodGet, "/api/keys"},
		{http.MethodGet, "/api/virtual-keys"},
		{http.MethodGet, "/api/virtual-keys/rotate"},
		{http.MethodGet, "/api/feedback"},
		{http.MethodGet, "/api/orgs"},
		{http.MethodGet, "/api/workspaces"},
		{http.MethodGet, "/api/users"},
		{http.MethodGet, "/api/workspace-members"},
		{http.MethodGet, "/api/prompts"},
		{http.MethodGet, "/api/prompts/versions"},
		{http.MethodGet, "/api/evals/policy"},
		{http.MethodGet, "/api/logs"},
		{http.MethodGet, "/api/health"},
	}
	for _, route := range routes {
		if rec := serveRoute(h, route.method, route.path, true); rec.Code == http.StatusNotFound {
			t.Errorf("%s %s = 404, want a registered route", route.method, route.path)
		}
	}
	// MCP is off, so its routes are not registered.
	for _, path := range []string{"/mcp", "/v1/mcp/tools/call"} {
		if rec := serveRoute(h, http.MethodPost, path, true); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s with MCP disabled = %d, want 404", path, rec.Code)
		}
	}
	// /metrics is registered only when Prometheus is enabled.
	if rec := serveRoute(h, http.MethodGet, "/metrics", true); rec.Code != http.StatusNotFound {
		t.Errorf("GET /metrics with Prometheus disabled = %d, want 404", rec.Code)
	}
}
