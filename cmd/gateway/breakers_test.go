package main

import (
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

// The shadow breaker uses the configured threshold and cooldown at startup,
// is rebuilt when a reload changes them, and is kept (with its open
// circuits) when a reload leaves them unchanged.
func TestShadowBreakerFollowsCircuitBreakerSettings(t *testing.T) {
	registry := provider.NewRegistry()
	rtr := router.New(registry)
	gw := gateway.New(registry, rtr, nil, guardrail.NewEngine())
	base := runtimeSettings{circuitBreakerFailures: 2, circuitBreakerCooldown: 90 * time.Second}
	setupCircuitBreakers(rtr, gw, base)

	shadow := gw.ShadowCircuitBreaker()
	if shadow == rtr.CircuitBreaker() {
		t.Fatal("shadow traffic shares the production breaker")
	}
	if threshold, cooldown := shadow.Settings(); threshold != 2 || cooldown != 90*time.Second {
		t.Fatalf("startup shadow breaker = %d, %v; want 2, 1m30s", threshold, cooldown)
	}
	if threshold, cooldown := rtr.CircuitBreaker().Settings(); threshold != 2 || cooldown != 90*time.Second {
		t.Fatalf("startup production breaker = %d, %v; want 2, 1m30s", threshold, cooldown)
	}

	shadow.RecordFailure("flaky")
	shadow.RecordFailure("flaky")
	if state := shadow.State("flaky"); state != router.CircuitOpen {
		t.Fatalf("shadow circuit = %s, want open", state)
	}
	applyHotConfig(gatewayconfig.Config{}, rtr, gw, base, nil)
	if gw.ShadowCircuitBreaker() != shadow {
		t.Fatal("reload with unchanged settings replaced the shadow breaker")
	}
	if state := gw.ShadowCircuitBreaker().State("flaky"); state != router.CircuitOpen {
		t.Fatalf("reload with unchanged settings closed the shadow circuit (state %s)", state)
	}

	failures := 4
	cfg := gatewayconfig.Config{}
	cfg.Gateway.CircuitBreakerFailures = &failures
	cfg.Gateway.CircuitBreakerCooldown = &gatewayconfig.Duration{Duration: 3 * time.Minute}
	applyHotConfig(cfg, rtr, gw, base, nil)
	replaced := gw.ShadowCircuitBreaker()
	if replaced == shadow {
		t.Fatal("reload with new settings kept the old shadow breaker")
	}
	if threshold, cooldown := replaced.Settings(); threshold != 4 || cooldown != 3*time.Minute {
		t.Fatalf("reloaded shadow breaker = %d, %v; want 4, 3m", threshold, cooldown)
	}
	if replaced == rtr.CircuitBreaker() {
		t.Fatal("reload made shadow traffic share the production breaker")
	}
	applyHotConfig(cfg, rtr, gw, base, nil)
	if gw.ShadowCircuitBreaker() != replaced {
		t.Fatal("second reload with the same settings replaced the shadow breaker")
	}
}
