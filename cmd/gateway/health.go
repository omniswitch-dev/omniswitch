package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// healthState backs the /healthz and /readyz endpoints. It lives in
// cmd/gateway rather than internal/gateway so the process-level questions
// ("is the binary alive", "can it currently serve traffic") stay independent
// of the application handler and its middleware chain: both endpoints are
// registered ahead of auth/authorization/rate-limit middleware so they are
// always reachable without credentials, which is what orchestrators expect
// from liveness/readiness probes.
type healthState struct {
	store            *store.Store
	registry         *provider.Registry
	allowNoProviders bool
	pingTimeout      time.Duration
}

func newHealthState(st *store.Store, registry *provider.Registry, allowNoProviders bool) *healthState {
	return &healthState{
		store:            st,
		registry:         registry,
		allowNoProviders: allowNoProviders,
		pingTimeout:      time.Second,
	}
}

// registerHealthRoutes wires /healthz and /readyz onto mux. Call this on a
// mux that sits in front of (not behind) the gateway's auth/authorization/
// rate-limit middleware.
func (h *healthState) registerHealthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.liveness)
	mux.HandleFunc("/readyz", h.readiness)
}

// liveness reports whether the process is up and serving HTTP. It never
// touches the store or provider registry so a slow or unreachable database
// cannot make an orchestrator kill an otherwise-healthy process.
func (h *healthState) liveness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeHealthJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// readiness reports whether the gateway can currently serve traffic: the
// store must answer a ping within the timeout, and at least one provider
// must be registered unless the operator explicitly allows a providerless
// deployment (OMNISWITCH_ALLOW_NO_PROVIDERS=true).
func (h *healthState) readiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var reasons []string

	ctx, cancel := context.WithTimeout(r.Context(), h.pingTimeout)
	defer cancel()
	if err := h.store.Ping(ctx); err != nil {
		reasons = append(reasons, "store: "+err.Error())
	}

	if !h.allowNoProviders && (h.registry == nil || len(h.registry.Names()) == 0) {
		reasons = append(reasons, "no providers configured")
	}

	if len(reasons) > 0 {
		writeHealthJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "not ready",
			"reasons": reasons,
		})
		return
	}
	writeHealthJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func writeHealthJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
