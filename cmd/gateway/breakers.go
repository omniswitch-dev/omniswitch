package main

import (
	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

// setupCircuitBreakers installs the startup circuit breakers: one for
// client traffic on rtr and a separate one for shadow traffic on gw, both
// with the configured failure threshold and cooldown. The shadow breaker is
// kept apart so a failing shadow provider cannot open circuits for client
// traffic. applyHotConfig replaces both when the settings change.
func setupCircuitBreakers(rtr *router.Router, gw *gateway.Handler, settings runtimeSettings) {
	rtr.SetCircuitBreaker(router.NewCircuitBreaker(settings.circuitBreakerFailures, settings.circuitBreakerCooldown))
	gw.SetShadowCircuitBreaker(router.NewCircuitBreaker(settings.circuitBreakerFailures, settings.circuitBreakerCooldown))
}
