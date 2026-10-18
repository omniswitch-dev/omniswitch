package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/omniswitch-dev/omniswitch/internal/store"
	"github.com/omniswitch-dev/omniswitch/internal/vault"
)

// C1: end to end through the real mux and middleware chain. Requests keep
// using a stored config by name while many other configs fill the router
// cache, which used to evict only the config's id key. Disabling it through
// PATCH /api/configs?id=<id> must still take effect at once for requests
// that send the name.
func TestDynamicConfigDisableByIDUnderCachePressure(t *testing.T) {
	st := newTestStore(t)
	settings := runtimeSettings{
		authEnabled:       true,
		bootstrapAPIKey:   routeTestKey,
		rateLimitRequests: 100000,
		rateLimitWindow:   time.Minute,
	}
	ctx := context.Background()
	if err := ensureBootstrapKey(ctx, st, settings); err != nil {
		t.Fatalf("ensureBootstrapKey() error = %v", err)
	}
	// The test sends a few hundred requests; lift the key's own limit.
	key, err := st.GetAPIKeyByID(ctx, bootstrapKeyID)
	if err != nil {
		t.Fatalf("GetAPIKeyByID() error = %v", err)
	}
	key.RateLimit = 100000
	if err := st.InsertAPIKey(ctx, key); err != nil {
		t.Fatalf("InsertAPIKey() error = %v", err)
	}
	registry := provider.NewRegistry()
	registry.Register(configRouteProvider{name: "alpha"})
	registry.Register(configRouteProvider{name: "beta"})
	rtr := router.New(registry)
	rtr.ReplaceRoutes(map[string]router.Route{"m": {Provider: "alpha"}})
	gw := gateway.New(registry, rtr, st, guardrail.NewEngine())
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
	server := newTopMux(newHealthState(st, registry, false), handler)

	serve := func(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+routeTestKey)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	calls := 0
	chat := func(config string) *httptest.ResponseRecorder {
		calls++
		body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"question %d"}]}`, calls)
		return serve(http.MethodPost, "/v1/chat/completions", body, map[string]string{"x-omniswitch-config": config})
	}

	configBody := "apiVersion: omniswitch.dev/v1\nkind: GatewayConfig\nroutes:\n  m:\n    provider: beta\n"
	payload, _ := json.Marshal(map[string]string{"id": "cfg_hot", "name": "hot", "format": "yaml", "body": configBody})
	if rec := serve(http.MethodPost, "/api/configs", string(payload), nil); rec.Code != http.StatusCreated {
		t.Fatalf("create config: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := chat("hot"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "from beta") {
		t.Fatalf("stored route: status %d body %s, want beta", rec.Code, rec.Body.String())
	}
	// More configs than the cache holds (2 keys each, 512 keys), with the
	// hot config used by name every few requests.
	const fillers = 300
	for i := 0; i < fillers; i++ {
		name := fmt.Sprintf("fill-%d", i)
		if err := st.UpsertGatewayConfig(ctx, store.GatewayConfigRecord{
			ID: "cfg_" + name, Name: name, Format: "yaml", Body: configBody, Enabled: true,
		}); err != nil {
			t.Fatalf("UpsertGatewayConfig(%s) error = %v", name, err)
		}
		if rec := chat(name); rec.Code != http.StatusOK {
			t.Fatalf("filler %s: status %d body %s", name, rec.Code, rec.Body.String())
		}
		if i%10 == 0 {
			if rec := chat("hot"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "from beta") {
				t.Fatalf("hot config: status %d body %s, want beta", rec.Code, rec.Body.String())
			}
		}
	}
	if rec := serve(http.MethodPatch, "/api/configs?id=cfg_hot", `{"enabled":false}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("disable config: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := chat("hot"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("disabled config by name: status %d body %s, want 400 at once", rec.Code, rec.Body.String())
	}
}
