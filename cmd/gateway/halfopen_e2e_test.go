package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// recoveringChatProvider fails until failing is cleared. After that, a call
// whose prompt contains "probe" waits for release; other calls answer at
// once.
type recoveringChatProvider struct {
	name    string
	failing atomic.Bool
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *recoveringChatProvider) Name() string { return p.name }

func (p *recoveringChatProvider) Models() []provider.ModelInfo { return nil }

func (p *recoveringChatProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	p.calls.Add(1)
	meta := provider.ProviderMeta{Provider: p.name, Model: req.Model, Timestamp: time.Now().UTC()}
	if p.failing.Load() {
		return provider.ChatResponse{}, meta, errors.New("upstream unavailable")
	}
	if len(req.Messages) > 0 && strings.Contains(fmt.Sprint(req.Messages[0].Content), "probe") {
		p.entered <- struct{}{}
		select {
		case <-p.release:
		case <-ctx.Done():
			return provider.ChatResponse{}, meta, ctx.Err()
		}
	}
	return provider.ChatResponse{
		ID: "chat_" + p.name, Object: "chat.completion", Created: time.Now().Unix(), Model: req.Model,
		Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "from " + p.name}, FinishReason: "stop"}},
	}, meta, nil
}

// End to end through the real mux and middleware chain: provider a fails
// until its circuit opens; after the cooldown one slow probe request goes
// to a, and a concurrent request falls back to b and gets 200 instead of
// piling onto the recovering provider.
func TestHalfOpenProbeThroughGatewayChain(t *testing.T) {
	st := newTestStore(t)
	settings := runtimeSettings{
		authEnabled:            true,
		bootstrapAPIKey:        routeTestKey,
		rateLimitRequests:      1000,
		rateLimitWindow:        time.Minute,
		circuitBreakerFailures: 1,
		circuitBreakerCooldown: 500 * time.Millisecond,
	}
	if err := ensureBootstrapKey(context.Background(), st, settings); err != nil {
		t.Fatalf("ensureBootstrapKey() error = %v", err)
	}
	alpha := &recoveringChatProvider{name: "alpha", entered: make(chan struct{}, 4), release: make(chan struct{})}
	alpha.failing.Store(true)
	registry := provider.NewRegistry()
	registry.Register(alpha)
	registry.Register(configRouteProvider{name: "beta"})
	rtr := router.New(registry)
	rtr.ReplaceRoutes(map[string]router.Route{"m": {Provider: "alpha", Fallbacks: []string{"beta"}}})
	gw := gateway.New(registry, rtr, st, guardrail.NewEngine())
	setupCircuitBreakers(rtr, gw, settings)
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

	chat := func(content string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":%q}]}`, content)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+routeTestKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}

	if rec := chat("opening request"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "from beta") {
		t.Fatalf("opening request: status %d body %s, want fallback to beta", rec.Code, rec.Body.String())
	}
	if state := rtr.CircuitBreaker().State("alpha"); state != router.CircuitOpen {
		t.Fatalf("alpha circuit = %s, want open", state)
	}
	alpha.failing.Store(false)
	time.Sleep(settings.circuitBreakerCooldown + 20*time.Millisecond)

	probeDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { probeDone <- chat("probe request") }()
	select {
	case <-alpha.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("probe request did not reach alpha")
	}
	callsWithProbe := alpha.calls.Load()

	rec := chat("concurrent request")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "from beta") {
		t.Fatalf("concurrent request: status %d body %s, want 200 from beta", rec.Code, rec.Body.String())
	}
	if got := alpha.calls.Load(); got != callsWithProbe {
		t.Fatalf("alpha got %d extra calls while the probe was in flight, want 0", got-callsWithProbe)
	}

	close(alpha.release)
	probe := <-probeDone
	if probe.Code != http.StatusOK || !strings.Contains(probe.Body.String(), "from alpha") {
		t.Fatalf("probe request: status %d body %s, want 200 from alpha", probe.Code, probe.Body.String())
	}
	if state := rtr.CircuitBreaker().State("alpha"); state != router.CircuitClosed {
		t.Fatalf("alpha circuit = %s after the probe succeeded, want closed", state)
	}
	if rec := chat("after recovery"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "from alpha") {
		t.Fatalf("after recovery: status %d body %s, want alpha", rec.Code, rec.Body.String())
	}
}
