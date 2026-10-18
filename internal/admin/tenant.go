package admin

import (
	"context"
	"database/sql"
	"errors"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
)

// keyInScope reports whether a key in workspaceID is visible to scope. A
// global scope sees every key. A scoped caller never sees a key without a
// workspace, because such a key is global. An organization scope sees the
// keys of the workspaces in its organization; the workspace's organization
// is read from the store, not from the caller's claims.
func (h *Handler) keyInScope(ctx context.Context, scope gateway.TenantScope, workspaceID string) (bool, error) {
	switch {
	case scope.Global():
		return true, nil
	case workspaceID == "":
		return false, nil
	case scope.WorkspaceID != "":
		return workspaceID == scope.WorkspaceID, nil
	}
	workspace, err := h.store.GetWorkspaceByID(ctx, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return scope.Allows(workspace.ID, workspace.OrganizationID), nil
}
