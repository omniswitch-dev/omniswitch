package gateway

import (
	"context"
	"testing"
)

func TestScopeFromContext(t *testing.T) {
	if scope := ScopeFromContext(context.Background()); !scope.Global() {
		t.Fatalf("scope without identity = %+v, want global", scope)
	}
	ctx := context.WithValue(context.Background(), identityContextKey, Identity{WorkspaceID: "ws-a", OrganizationID: "org-a"})
	scope := ScopeFromContext(ctx)
	if scope.Global() || scope.WorkspaceID != "ws-a" || scope.OrganizationID != "org-a" {
		t.Fatalf("scope = %+v, want ws-a in org-a", scope)
	}
}

func TestTenantScopeAllows(t *testing.T) {
	global := TenantScope{}
	workspace := TenantScope{WorkspaceID: "ws-a", OrganizationID: "org-a"}
	organization := TenantScope{OrganizationID: "org-a"}
	cases := []struct {
		name      string
		scope     TenantScope
		workspace string
		org       string
		want      bool
	}{
		{"global sees a workspace", global, "ws-b", "org-b", true},
		{"global sees unowned records", global, "", "", true},
		{"workspace sees its own", workspace, "ws-a", "org-a", true},
		{"workspace ignores the record organization", workspace, "ws-a", "", true},
		{"workspace does not see a sibling workspace", workspace, "ws-b", "org-a", false},
		{"workspace does not see unowned records", workspace, "", "", false},
		{"organization sees its workspaces", organization, "ws-b", "org-a", true},
		{"organization does not see another organization", organization, "ws-c", "org-b", false},
		{"organization does not see unowned records", organization, "", "", false},
	}
	for _, tc := range cases {
		if got := tc.scope.Allows(tc.workspace, tc.org); got != tc.want {
			t.Errorf("%s: Allows(%q, %q) = %v, want %v", tc.name, tc.workspace, tc.org, got, tc.want)
		}
	}
}
