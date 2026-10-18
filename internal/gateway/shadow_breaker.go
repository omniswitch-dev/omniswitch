package gateway

import "github.com/omniswitch-dev/omniswitch/internal/router"

// SetShadowCircuitBreaker replaces the circuit breaker used for shadow
// (mirrored) traffic. It is kept apart from the production breaker so a
// failing shadow provider cannot open circuits for client traffic. Shadow
// requests already running keep the breaker they started with. A nil
// breaker is ignored. Without a call, the handler uses a breaker with the
// defaults (5 failures, 60s cooldown).
func (h *Handler) SetShadowCircuitBreaker(breaker *router.CircuitBreaker) {
	if breaker == nil {
		return
	}
	h.shadowBreaker.Store(breaker)
}

// ShadowCircuitBreaker returns the circuit breaker used for shadow traffic.
func (h *Handler) ShadowCircuitBreaker() *router.CircuitBreaker {
	return h.shadowBreaker.Load()
}
