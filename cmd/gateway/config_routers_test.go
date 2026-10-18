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
	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/org"
	"github.com/omniswitch-dev/omniswitch/internal/prompt"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/vault"
)

// GW-19: a reload with unchanged breaker settings keeps the breaker, so an
// open circuit stays open; changed settings install a new breaker that the
// config-router clones share.
func TestApplyHotConfigKeepsBreakerWhenSettingsUnchanged(t *testing.T) {
	registry := provider.NewRegistry()
	rtr := router.New(registry)
	base := runtimeSettings{circuitBreakerFailures: 1, circuitBreakerCooldown: time.Hour}
	rtr.SetCircuitBreaker(router.NewCircuitBreaker(base.circuitBreakerFailures, base.circuitBreakerCooldown))
	gw := gateway.New(registry, rtr, nil, guardrail.NewEngine())
	clone := rtr.CloneWithRoutes(nil)

	original := rtr.CircuitBreaker()
	original.RecordFailure("flaky")
	if original.State("flaky") != router.CircuitOpen {
		t.Fatalf("circuit state = %s, want open", original.State("flaky"))
	}
	applyHotConfig(gatewayconfig.Config{}, rtr, gw, base, nil)
	if rtr.CircuitBreaker() != original {
		t.Fatalf("reload with unchanged settings replaced the breaker")
	}
	if rtr.CircuitBreaker().State("flaky") != router.CircuitOpen {
		t.Fatalf("reload with unchanged settings closed an open circuit")
	}

	failures := 3
	cfg := gatewayconfig.Config{}
	cfg.Gateway.CircuitBreakerFailures = &failures
	applyHotConfig(cfg, rtr, gw, base, nil)
	replaced := rtr.CircuitBreaker()
	if replaced == original {
		t.Fatalf("reload with new settings kept the old breaker")
	}
	if threshold, cooldown := replaced.Settings(); threshold != 3 || cooldown != time.Hour {
		t.Fatalf("new breaker settings = %d, %v; want 3, 1h", threshold, cooldown)
	}
	if clone.CircuitBreaker() != replaced {
		t.Fatalf("config-router clone does not use the new breaker")
	}
	applyHotConfig(cfg, rtr, gw, base, nil)
	if rtr.CircuitBreaker() != replaced {
		t.Fatalf("second reload with the same settings replaced the breaker")
	}
}

type configRouteProvider struct{ name string }

func (p configRouteProvider) Name() string { return p.name }

func (p configRouteProvider) Models() []provider.ModelInfo { return nil }

func (p configRouteProvider) ChatCompletion(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	return provider.ChatResponse{
		ID: "chat_" + p.name, Object: "chat.completion", Created: time.Now().Unix(), Model: req.Model,
		Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "from " + p.name}, FinishReason: "stop"}},
	}, provider.ProviderMeta{Provider: p.name, Model: req.Model, Timestamp: time.Now().UTC()}, nil
}

// GW-19: end to end through the real mux and middleware chain. A config
// created through /api/configs routes x-omniswitch-config requests, a hot
// reload of the file routes reaches it, and a delete through /api/configs
// takes effect at once on this replica.
func TestDynamicConfigThroughGatewayChain(t *testing.T) {
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
	registry.Register(configRouteProvider{name: "alpha"})
	registry.Register(configRouteProvider{name: "beta"})
	rtr := router.New(registry)
	rtr.ReplaceRoutes(map[string]router.Route{"m": {Provider: "alpha"}, "n": {Provider: "alpha"}})
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
	chat := func(model string) *httptest.ResponseRecorder {
		calls++
		body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"question %d"}]}`, model, calls)
		return serve(http.MethodPost, "/v1/chat/completions", body, map[string]string{"x-omniswitch-config": "team-c"})
	}

	// An unknown name is rejected as before (and remembered briefly).
	if rec := chat("m"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("unknown config: status %d body %s, want 400", rec.Code, rec.Body.String())
	}
	configBody := "apiVersion: omniswitch.dev/v1\nkind: GatewayConfig\nroutes:\n  m:\n    provider: beta\n"
	payload, _ := json.Marshal(map[string]string{"name": "team-c", "format": "yaml", "body": configBody})
	if rec := serve(http.MethodPost, "/api/configs", string(payload), nil); rec.Code != http.StatusCreated {
		t.Fatalf("create config: status %d body %s", rec.Code, rec.Body.String())
	}
	// The create hook drops the negative entry at once.
	if rec := chat("m"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "from beta") {
		t.Fatalf("stored route: status %d body %s, want beta", rec.Code, rec.Body.String())
	}
	if rec := chat("n"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "from alpha") {
		t.Fatalf("base route: status %d body %s, want alpha", rec.Code, rec.Body.String())
	}
	// Hot reload of the file routes.
	applyHotConfig(gatewayconfig.Config{Routes: map[string]router.Route{"m": {Provider: "alpha"}, "n": {Provider: "beta"}}}, rtr, gw, settings, nil)
	if rec := chat("n"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "from beta") {
		t.Fatalf("reloaded base route: status %d body %s, want beta", rec.Code, rec.Body.String())
	}
	if rec := chat("m"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "from beta") {
		t.Fatalf("stored route after reload: status %d body %s, want beta", rec.Code, rec.Body.String())
	}
	if rec := serve(http.MethodDelete, "/api/configs?name=team-c", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete config: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := chat("m"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("deleted config: status %d body %s, want 400 at once", rec.Code, rec.Body.String())
	}
}
