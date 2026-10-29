package dashboard

import (
	"net/http"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// SetMetricsPublic records whether /metrics is public
// (OMNISWITCH_METRICS_PUBLIC). When it is not, tenant-scoped callers may
// not read the gateway-wide Prometheus aggregates. Call it before
// RegisterPrometheus.
func (h *Handler) SetMetricsPublic(public bool) {
	h.metricsPublic = public
}

// logScope is what the caller of r may read from the request logs: the
// caller's workspace or organization, and its own API key for callers
// below admin.
func logScope(r *http.Request) store.LogScope {
	tenant := gateway.ScopeFromContext(r.Context())
	return store.LogScope{
		APIKeyID:       scopedAPIKeyID(r),
		WorkspaceID:    tenant.WorkspaceID,
		OrganizationID: tenant.OrganizationID,
	}
}

// globalOnly limits a gateway-wide endpoint to global callers: stored
// routing configs and the raw config file apply to every tenant, so a
// workspace- or organization-scoped caller may neither read nor change
// them.
func globalOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !gateway.ScopeFromContext(r.Context()).Global() {
			writeError(w, http.StatusForbidden, "gateway configuration applies to every tenant; it needs a caller without a workspace or organization")
			return
		}
		next(w, r)
	}
}

// prometheusAccess limits the gateway-wide Prometheus metrics to global
// callers unless metrics are public.
func (h *Handler) prometheusAccess(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.metricsPublic && !gateway.ScopeFromContext(r.Context()).Global() {
			writeError(w, http.StatusForbidden, "gateway-wide metrics need a caller without a workspace or organization (or OMNISWITCH_METRICS_PUBLIC=true)")
			return
		}
		next(w, r)
	}
}
