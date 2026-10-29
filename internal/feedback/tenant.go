package feedback

import (
	"net/http"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// feedbackScope is the part of the feedback and request logs that a caller
// with this tenant scope may see.
func feedbackScope(tenant gateway.TenantScope) store.LogScope {
	return store.LogScope{WorkspaceID: tenant.WorkspaceID, OrganizationID: tenant.OrganizationID}
}

// referencesInScope checks that a tenant-scoped caller names only a request
// or trace that it can see. A request or trace of another tenant gets the
// same 404 as a missing one, so its existence is not confirmed. Global
// callers may name any request or trace, including one not logged yet.
// It writes the error response and returns false when the check fails.
func (h *Handler) referencesInScope(w http.ResponseWriter, r *http.Request, tenant gateway.TenantScope, req CreateRequest) bool {
	if tenant.Global() {
		return true
	}
	scope := feedbackScope(tenant)
	if req.RequestID != "" {
		ok, err := h.store.RequestLogInScope(r.Context(), req.RequestID, scope)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return false
		}
		if !ok {
			writeError(w, http.StatusNotFound, "request not found")
			return false
		}
	}
	if req.TraceID != "" {
		ok, err := h.store.TraceInScope(r.Context(), req.TraceID, scope)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return false
		}
		if !ok {
			writeError(w, http.StatusNotFound, "trace not found")
			return false
		}
	}
	return true
}
