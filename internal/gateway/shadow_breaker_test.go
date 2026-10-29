package gateway

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestShadowCircuitBreakerDefaultAndSetter(t *testing.T) {
	h := New(provider.NewRegistry(), router.New(provider.NewRegistry()), nil, guardrail.NewEngine())
	defaults := h.ShadowCircuitBreaker()
	if threshold, cooldown := defaults.Settings(); threshold != 5 || cooldown != 60*time.Second {
		t.Fatalf("default shadow breaker = %d, %v; want 5, 1m", threshold, cooldown)
	}
	h.SetShadowCircuitBreaker(nil)
	if h.ShadowCircuitBreaker() != defaults {
		t.Fatal("SetShadowCircuitBreaker(nil) replaced the breaker")
	}
	custom := router.NewCircuitBreaker(2, time.Hour)
	h.SetShadowCircuitBreaker(custom)
	if h.ShadowCircuitBreaker() != custom {
		t.Fatal("SetShadowCircuitBreaker did not install the breaker")
	}
}

// Shadow requests use the breaker installed with SetShadowCircuitBreaker:
// with threshold 1 one failed shadow call opens the shadow circuit, the
// next shadow request skips the provider, and the production breaker is
// untouched.
func TestShadowRequestsUseConfiguredBreaker(t *testing.T) {
	var shadowCalls int64
	primary := scriptedProvider{name: "primary", model: "test-model", content: "ok"}
	shadow := scriptedProvider{name: "shadow", model: "test-model", failChat: true, calls: &shadowCalls}
	st := newGatewayTestStore(t)
	reg := provider.NewRegistry()
	reg.Register(primary)
	reg.Register(shadow)
	rt := router.New(reg)
	prodBreaker := router.NewCircuitBreaker(1, time.Hour)
	rt.SetCircuitBreaker(prodBreaker)
	h := New(reg, rt, st, guardrail.NewEngine())
	shadowBreaker := router.NewCircuitBreaker(1, time.Hour)
	h.SetShadowCircuitBreaker(shadowBreaker)

	waitLog := func(requestID string) store.ShadowLog {
		t.Helper()
		for i := 0; i < 500; i++ {
			logs, err := st.ListShadowLogs(context.Background(), requestID)
			if err != nil {
				t.Fatalf("ListShadowLogs() error = %v", err)
			}
			if len(logs) == 1 {
				return logs[0]
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no shadow log for %s", requestID)
		return store.ShadowLog{}
	}
	req := provider.ChatRequest{Model: "test-model", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	h.shadow(rt, "req_shadow_1", "trace", "primary", req, "shadow")
	if entry := waitLog("req_shadow_1"); entry.Status != statusError {
		t.Fatalf("first shadow log = %+v, want error", entry)
	}
	if state := shadowBreaker.State("shadow"); state != router.CircuitOpen {
		t.Fatalf("configured shadow breaker state = %s, want open after one failure", state)
	}
	h.shadow(rt, "req_shadow_2", "trace", "primary", req, "shadow")
	entry := waitLog("req_shadow_2")
	if !strings.Contains(entry.ErrorMessage, "circuit is open") {
		t.Fatalf("second shadow log error = %q, want circuit open", entry.ErrorMessage)
	}
	if got := atomic.LoadInt64(&shadowCalls); got != 1 {
		t.Fatalf("shadow provider calls = %d, want 1", got)
	}
	if state := prodBreaker.State("shadow"); state != router.CircuitClosed {
		t.Fatalf("production breaker state for shadow = %s, want closed", state)
	}
}
