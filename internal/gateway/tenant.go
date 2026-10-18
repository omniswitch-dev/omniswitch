package gateway

import "context"

// TenantScope is the part of the control plane that a caller may read and
// change. Control-plane handlers use it to keep tenants that share one
// gateway apart.
//
// The zero value is global. A request is global when authentication is off
// (no identity is attached) or when the caller's identity has neither a
// workspace nor an organization. Role checks still apply to global callers.
type TenantScope struct {
	// WorkspaceID limits the caller to one workspace. A workspace-scoped
	// caller may also have an OrganizationID: the organization that owns
	// the workspace.
	WorkspaceID string
	// OrganizationID limits a caller without a workspace to one
	// organization and the workspaces in it.
	OrganizationID string
}

// ScopeFromContext returns the tenant scope of the caller of a request.
func ScopeFromContext(ctx context.Context) TenantScope {
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		return TenantScope{}
	}
	return TenantScope{WorkspaceID: identity.WorkspaceID, OrganizationID: identity.OrganizationID}
}

// Global reports whether the caller may see every tenant.
func (s TenantScope) Global() bool {
	return s.WorkspaceID == "" && s.OrganizationID == ""
}

// Allows reports whether the caller may see a record that belongs to the
// given workspace and organization. For a record of a workspace, pass the
// organization that owns that workspace when it is known. A scoped caller
// never sees records that belong to no tenant.
func (s TenantScope) Allows(workspaceID, organizationID string) bool {
	switch {
	case s.Global():
		return true
	case s.WorkspaceID != "":
		return workspaceID == s.WorkspaceID
	default:
		return organizationID == s.OrganizationID
	}
}
