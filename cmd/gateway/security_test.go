package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	top := newTopMux(newHealthState(st, registry, false), handler)
	registerPublicMetrics(top, settings, dash)
	return top
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

func TestAuthDisabledIgnoresSpoofedRateLimitHeaders(t *testing.T) {
	h := newSecurityTestServer(t, func(s *runtimeSettings) {
		s.authEnabled = false
		s.rateLimitRequests = 2
	})
	allowed := 0
	for i := range 10 {
		rec := doRequest(h, http.MethodGet, "/api/version", "", "", map[string]string{
			"x-omniswitch-rate-limit": "1000000",
			"x-omniswitch-key-id":     "rotating-" + string(rune('a'+i)),
		})
		if rec.Code == http.StatusOK {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("allowed = %d of 10, want 2", allowed)
	}
}

func TestControlPlaneRejectsOversizedBodies(t *testing.T) {
	h := newSecurityTestServer(t, nil)
	big := `{"request_id":"r","comment":"` + strings.Repeat("x", 2<<20) + `"}`
	if rec := doRequest(h, http.MethodPost, "/api/feedback", routeTestKey, big, nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized feedback status = %d, want 413", rec.Code)
	}
	if rec := doRequest(h, http.MethodPost, "/api/feedback", routeTestKey, `{"request_id":"r","score":1}`, nil); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("small feedback status = %d body = %s", rec.Code, rec.Body.String())
	}

	// A streamed body has no Content-Length, so the limit is found only
	// while the handler reads it. It still gets 413, and an unauthenticated
	// caller is rejected before the body is read.
	streamed := func(path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, io.MultiReader(strings.NewReader(body)))
		req.ContentLength = -1
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	bigKey := `{"name":"` + strings.Repeat("x", 2<<20) + `"}`
	for _, path := range []string{"/api/feedback", "/api/keys", "/api/prompts"} {
		body := big
		if path != "/api/feedback" {
			body = bigKey
		}
		rec := streamed(path, routeTestKey, body)
		if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "request body too large") {
			t.Fatalf("streamed oversized %s = %d %s, want 413", path, rec.Code, rec.Body.String())
		}
	}
	if rec := streamed("/api/keys", "", bigKey); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated streamed body = %d, want 401", rec.Code)
	}
}

// TestPolicyReplayDirectoryDefault checks that policy replay reads only a
// dedicated policies directory by default. A policy file next to the gateway
// config or in the working directory must not expose the other files there.
func TestPolicyReplayDirectoryDefault(t *testing.T) {
	cases := []struct {
		name, mcpPolicy, policyDirEnv, want string
	}{
		{name: "built-in default", want: "policies"},
		{name: "policies directory", mcpPolicy: "/app/policies/mcp.yaml", want: "/app/policies"},
		{name: "policy next to the config", mcpPolicy: "/config/mcp-policy.yaml", want: ""},
		{name: "policy in the working directory", mcpPolicy: "mcp-policy.yaml", want: ""},
		{name: "explicit directory", mcpPolicy: "/config/mcp-policy.yaml", policyDirEnv: "/srv/replay", want: "/srv/replay"},
		{name: "explicitly off", policyDirEnv: "off", want: ""},
		{name: "explicitly off with a dash", policyDirEnv: "-", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearGatewayEnv(t)
			if tc.mcpPolicy != "" {
				t.Setenv("OMNISWITCH_MCP_POLICY", tc.mcpPolicy)
			}
			if tc.policyDirEnv != "" {
				t.Setenv("OMNISWITCH_POLICY_DIR", tc.policyDirEnv)
			}
			settings, err := loadRuntimeSettings()
			if err != nil {
				t.Fatalf("loadRuntimeSettings() error = %v", err)
			}
			if settings.policyDir != filepath.FromSlash(tc.want) {
				t.Fatalf("policyDir = %q, want %q", settings.policyDir, tc.want)
			}
		})
	}
}
