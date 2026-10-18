package router

import (
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

// GW-19: route and breaker changes bump the generation, so cached clones
// know when to rebuild.
func TestRouterGenerationChangesOnRoutesAndBreaker(t *testing.T) {
	rtr := New(provider.NewRegistry())
	gen := rtr.Generation()
	rtr.SetRoute("a", Route{Provider: "p"})
	if next := rtr.Generation(); next == gen {
		t.Fatalf("SetRoute did not change the generation")
	} else {
		gen = next
	}
	rtr.ReplaceRoutes(map[string]Route{"b": {Provider: "p"}})
	if next := rtr.Generation(); next == gen {
		t.Fatalf("ReplaceRoutes did not change the generation")
	} else {
		gen = next
	}
	rtr.SetCircuitBreaker(NewCircuitBreaker(1, time.Hour))
	if rtr.Generation() == gen {
		t.Fatalf("SetCircuitBreaker did not change the generation")
	}
	gen = rtr.Generation()
	_ = rtr.CloneWithRoutes(map[string]Route{"c": {Provider: "p"}})
	if rtr.Generation() != gen {
		t.Fatalf("CloneWithRoutes changed the base generation")
	}
}

// GW-19: a clone uses the current breaker of its base router, also after the
// base breaker is swapped, so open circuits are shared.
func TestCloneWithRoutesFollowsBaseBreaker(t *testing.T) {
	rtr := New(provider.NewRegistry())
	first := NewCircuitBreaker(1, time.Hour)
	rtr.SetCircuitBreaker(first)
	clone := rtr.CloneWithRoutes(map[string]Route{"m": {Provider: "p"}})
	nested := clone.CloneWithRoutes(nil)
	if clone.CircuitBreaker() != first || nested.CircuitBreaker() != first {
		t.Fatalf("clones do not share the base breaker")
	}
	clone.recordFailure("p")
	if rtr.allowProvider("p") {
		t.Fatalf("failure recorded through the clone did not open the base circuit")
	}
	second := NewCircuitBreaker(3, time.Minute)
	rtr.SetCircuitBreaker(second)
	if clone.CircuitBreaker() != second || nested.CircuitBreaker() != second {
		t.Fatalf("clones kept the old breaker after the base breaker was swapped")
	}
	shadow := clone.CloneForShadow(NewCircuitBreaker(5, time.Hour))
	if shadow.CircuitBreaker() == second {
		t.Fatalf("shadow clone uses the production breaker")
	}
	if threshold, cooldown := second.Settings(); threshold != 3 || cooldown != time.Minute {
		t.Fatalf("Settings() = %d, %v; want 3, 1m", threshold, cooldown)
	}
}
