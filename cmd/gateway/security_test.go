package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// newSecurityTestServer builds the full serving chain (top mux, middleware,
// gateway mux) like runServe does. modify can adjust settings first.
func newSecurityTestServer(t *testing.T, modify func(*runtimeSettings)) http.Handler {
	t.Helper()
	st := newTestStore(t)
	settings := runtimeSettings{
		authEnabled:       true,
		bootstrapAPIKey:   routeTestKey,
		rateLimitRequests: 1000,
		rateLimitWindow:   time.Minute,
		prometheusEnabled: true,
	}
	if modify != nil {
		modify(&settings)
	}
	if err := ensureBootstrapKey(context.Background(), st, settings); err != nil {
		t.Fatalf("ensureBootstrapKey() error = %v", err)
	}
	registry := provider.NewRegistry()
	registry.Register(fakeHealthProvider{name: "fake"})
	gw := gateway.New(registry, router.New(registry), st, guardrail.NewEngine())
	dash := dashboard.New(st)
	mux := newGatewayMux(routeHandlers{
		settings: settings, registry: registry, gateway: gw, admin: admin.New(st),
		feedback: feedback.New(st), prompt: prompt.New(st), org: org.New(st), eval: eval.NewHandler(st),
		vault: vault.NewHandler(vault.New(st, "")), dash: dash,
	})
	handler, closeRateLimiter, err := wrapGatewayMiddleware(settings, st, mux)
	if err != nil {
		t.Fatalf("wrapGatewayMiddleware() error = %v", err)
	}
	t.Cleanup(closeRateLimiter)
	return newTopMux(newHealthState(st, registry, false), handler)
}

func doRequest(h http.Handler, method, path, key, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func createTestKey(t *testing.T, h http.Handler, by, body string) string {
	t.Helper()
	rec := doRequest(h, http.MethodPost, "/api/keys", by, body, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Key
}

func TestViewerCannotReadSecretBearingControlPlane(t *testing.T) {
	h := newSecurityTestServer(t, nil)
	viewer := createTestKey(t, h, routeTestKey, `{"name":"app"}`)
	for _, path := range []string{"/api/keys", "/api/users", "/api/workspace-members", "/api/virtual-keys", "/api/configs"} {
		if rec := doRequest(h, http.MethodGet, path, viewer, "", nil); rec.Code != http.StatusForbidden {
			t.Errorf("viewer GET %s = %d, want 403", path, rec.Code)
		}
		if rec := doRequest(h, http.MethodGet, path, routeTestKey, "", nil); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Errorf("owner GET %s = %d, want access", path, rec.Code)
		}
	}
	for _, path := range []string{"/api/logs", "/api/metrics", "/api/analytics/cost", "/metrics"} {
		if rec := doRequest(h, http.MethodGet, path, viewer, "", nil); rec.Code != http.StatusOK {
			t.Errorf("viewer GET %s = %d, want 200", path, rec.Code)
		}
	}
}

func TestViewerCannotRenderPromptWithGETBody(t *testing.T) {
	h := newSecurityTestServer(t, nil)
	viewer := createTestKey(t, h, routeTestKey, `{"name":"app"}`)
	if rec := doRequest(h, http.MethodGet, "/api/prompts/render", viewer, `{"prompt_id":"x"}`, nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("viewer GET render = %d, want 405; body = %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(h, http.MethodPost, "/api/prompts/render", viewer, `{"prompt_id":"x"}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer POST render = %d, want 403", rec.Code)
	}
	if rec := doRequest(h, http.MethodPost, "/api/logs", viewer, `{}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer POST /api/logs = %d, want 403", rec.Code)
	}
}
