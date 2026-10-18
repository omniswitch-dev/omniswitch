package org

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// Callers used by the tenant tests. org_a owns ws_a1 and ws_a2; org_b owns
// ws_b.
var (
	globalAdmin = &gateway.Identity{APIKeyID: "g", Role: "admin"}
	orgAdmin    = &gateway.Identity{APIKeyID: "o", Role: "admin", OrganizationID: "org_a"}
	wsAdmin     = &gateway.Identity{APIKeyID: "w", Role: "admin", WorkspaceID: "ws_a1", OrganizationID: "org_a"}
	wsMember    = &gateway.Identity{APIKeyID: "m", Role: "member", WorkspaceID: "ws_a1", OrganizationID: "org_a"}
)

func seedOrgTenants(t *testing.T) (*store.Store, *Handler) {
	t.Helper()
	st := newOrgTestStore(t)
	ctx := t.Context()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"org_a", "org_b"} {
		if err := st.InsertOrganization(ctx, store.Organization{ID: id, Name: id, CreatedAt: base}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ws := range []store.Workspace{{ID: "ws_a1", OrganizationID: "org_a"}, {ID: "ws_a2", OrganizationID: "org_a"}, {ID: "ws_b", OrganizationID: "org_b"}} {
		ws.Name, ws.CreatedAt = ws.ID, base
		if err := st.InsertWorkspace(ctx, ws); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"ua", "ua2", "ub", "shared", "boss"} {
		if err := st.InsertUser(ctx, store.User{ID: id, Email: id + "@example.com", Name: id, CreatedAt: base}); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []store.WorkspaceMember{
		{WorkspaceID: "ws_a1", UserID: "ua", Role: "viewer"}, {WorkspaceID: "ws_a2", UserID: "ua2", Role: "viewer"},
		{WorkspaceID: "ws_b", UserID: "ub", Role: "viewer"}, {WorkspaceID: "ws_a1", UserID: "shared", Role: "viewer"},
		{WorkspaceID: "ws_b", UserID: "shared", Role: "admin"}, {WorkspaceID: "ws_a1", UserID: "boss", Role: "owner"},
	} {
		if err := st.UpsertWorkspaceMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	return st, New(st)
}

func call(caller *gateway.Identity, fn http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if caller != nil {
		r = r.WithContext(gateway.ContextWithIdentity(r.Context(), *caller))
	}
	rec := httptest.NewRecorder()
	fn(rec, r)
	return rec
}

// listIDs decodes {"<key>": [{"id"...}]} (or workspace_id/user_id pairs for
// members) and returns the sorted ids.
func listIDs(t *testing.T, rec *httptest.ResponseRecorder, key string) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string][]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, item := range body[key] {
		if id, ok := item["id"].(string); ok {
			ids = append(ids, id)
			continue
		}
		ids = append(ids, item["workspace_id"].(string)+"/"+item["user_id"].(string))
	}
	sort.Strings(ids)
	return ids
}

func TestOrgListsAreTenantScoped(t *testing.T) {
	_, h := seedOrgTenants(t)
	cases := []struct {
		name                string
		caller              *gateway.Identity
		orgs, ws, users, ms string
		wsQuery, memberQ    string
	}{
		{name: "auth off", caller: nil, orgs: "org_a,org_b", ws: "ws_a1,ws_a2,ws_b", users: "boss,shared,ua,ua2,ub",
			ms: "ws_a1/boss,ws_a1/shared,ws_a1/ua,ws_a2/ua2,ws_b/shared,ws_b/ub"},
		{name: "global", caller: globalAdmin, orgs: "org_a,org_b", ws: "ws_a1,ws_a2,ws_b", users: "boss,shared,ua,ua2,ub",
			ms: "ws_a1/boss,ws_a1/shared,ws_a1/ua,ws_a2/ua2,ws_b/shared,ws_b/ub"},
		{name: "organization", caller: orgAdmin, orgs: "org_a", ws: "ws_a1,ws_a2", users: "boss,shared,ua,ua2",
			ms: "ws_a1/boss,ws_a1/shared,ws_a1/ua,ws_a2/ua2"},
		{name: "workspace", caller: wsAdmin, orgs: "org_a", ws: "ws_a1", users: "boss,shared,ua",
			ms: "ws_a1/boss,ws_a1/shared,ws_a1/ua"},
		{name: "organization asks for another organization", caller: orgAdmin, wsQuery: "?organization_id=org_b", memberQ: "?workspace_id=ws_b",
			orgs: "org_a", ws: "", users: "boss,shared,ua,ua2", ms: ""},
		{name: "workspace asks for a sibling workspace", caller: wsAdmin, wsQuery: "?organization_id=org_a", memberQ: "?workspace_id=ws_a2",
			orgs: "org_a", ws: "ws_a1", users: "boss,shared,ua", ms: ""},
	}
	split := func(s string) []string {
		if s == "" {
			return []string{}
		}
		return strings.Split(s, ",")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := func(what string, got []string, want string) {
				t.Helper()
				if strings.Join(got, ",") != strings.Join(split(want), ",") {
					t.Errorf("%s = %v, want %v", what, got, split(want))
				}
			}
			check("organizations", listIDs(t, call(tc.caller, h.ListOrganizations, http.MethodGet, "/api/orgs", ""), "organizations"), tc.orgs)
			check("workspaces", listIDs(t, call(tc.caller, h.ListWorkspaces, http.MethodGet, "/api/workspaces"+tc.wsQuery, ""), "workspaces"), tc.ws)
			check("users", listIDs(t, call(tc.caller, h.ListUsers, http.MethodGet, "/api/users", ""), "users"), tc.users)
			check("members", listIDs(t, call(tc.caller, h.ListWorkspaceMembers, http.MethodGet, "/api/workspace-members"+tc.memberQ, ""), "members"), tc.ms)
		})
	}
}

func TestOrgWritesAreTenantScoped(t *testing.T) {
	cases := []struct {
		name   string
		caller *gateway.Identity
		fn     func(h *Handler) http.HandlerFunc
		path   string
		body   string
		status int
	}{
		{"global creates organization", globalAdmin, func(h *Handler) http.HandlerFunc { return h.CreateOrganization }, "/api/orgs", `{"name":"x"}`, http.StatusCreated},
		{"organization cannot create organization", orgAdmin, func(h *Handler) http.HandlerFunc { return h.CreateOrganization }, "/api/orgs", `{"name":"x"}`, http.StatusForbidden},
		{"workspace cannot create organization", wsAdmin, func(h *Handler) http.HandlerFunc { return h.CreateOrganization }, "/api/orgs", `{"name":"x"}`, http.StatusForbidden},

		{"global creates workspace anywhere", globalAdmin, func(h *Handler) http.HandlerFunc { return h.CreateWorkspace }, "/api/workspaces", `{"organization_id":"org_b","name":"x"}`, http.StatusCreated},
		{"organization creates workspace in own organization", orgAdmin, func(h *Handler) http.HandlerFunc { return h.CreateWorkspace }, "/api/workspaces", `{"organization_id":"org_a","name":"x"}`, http.StatusCreated},
		{"organization defaults to own organization", orgAdmin, func(h *Handler) http.HandlerFunc { return h.CreateWorkspace }, "/api/workspaces", `{"name":"x"}`, http.StatusCreated},
		{"organization cannot forge organization_id", orgAdmin, func(h *Handler) http.HandlerFunc { return h.CreateWorkspace }, "/api/workspaces", `{"organization_id":"org_b","name":"x"}`, http.StatusNotFound},
		{"workspace cannot create workspace", wsAdmin, func(h *Handler) http.HandlerFunc { return h.CreateWorkspace }, "/api/workspaces", `{"organization_id":"org_a","name":"x"}`, http.StatusForbidden},

		{"workspace adds member to own workspace", wsAdmin, func(h *Handler) http.HandlerFunc { return h.UpsertWorkspaceMember }, "/api/workspace-members", `{"workspace_id":"ws_a1","user_id":"ub","role":"member"}`, http.StatusOK},
		{"workspace cannot add member to sibling", wsAdmin, func(h *Handler) http.HandlerFunc { return h.UpsertWorkspaceMember }, "/api/workspace-members", `{"workspace_id":"ws_a2","user_id":"ua","role":"viewer"}`, http.StatusNotFound},
		{"workspace cannot change member of other tenant", wsAdmin, func(h *Handler) http.HandlerFunc { return h.UpsertWorkspaceMember }, "/api/workspace-members", `{"workspace_id":"ws_b","user_id":"ub","role":"admin"}`, http.StatusNotFound},
		{"workspace cannot use unknown workspace", wsAdmin, func(h *Handler) http.HandlerFunc { return h.UpsertWorkspaceMember }, "/api/workspace-members", `{"workspace_id":"ws_zz","user_id":"ua","role":"viewer"}`, http.StatusNotFound},
		{"workspace cannot add unknown user", wsAdmin, func(h *Handler) http.HandlerFunc { return h.UpsertWorkspaceMember }, "/api/workspace-members", `{"workspace_id":"ws_a1","user_id":"nobody","role":"viewer"}`, http.StatusNotFound},
		{"workspace cannot grant above own role", wsAdmin, func(h *Handler) http.HandlerFunc { return h.UpsertWorkspaceMember }, "/api/workspace-members", `{"workspace_id":"ws_a1","user_id":"ua","role":"owner"}`, http.StatusForbidden},
		{"workspace cannot demote an owner", wsAdmin, func(h *Handler) http.HandlerFunc { return h.UpsertWorkspaceMember }, "/api/workspace-members", `{"workspace_id":"ws_a1","user_id":"boss","role":"viewer"}`, http.StatusForbidden},
		{"organization manages members of its workspaces", orgAdmin, func(h *Handler) http.HandlerFunc { return h.UpsertWorkspaceMember }, "/api/workspace-members", `{"workspace_id":"ws_a2","user_id":"ua","role":"member"}`, http.StatusOK},
		{"organization cannot manage other organization", orgAdmin, func(h *Handler) http.HandlerFunc { return h.UpsertWorkspaceMember }, "/api/workspace-members", `{"workspace_id":"ws_b","user_id":"ua","role":"member"}`, http.StatusNotFound},
		{"global manages any membership", globalAdmin, func(h *Handler) http.HandlerFunc { return h.UpsertWorkspaceMember }, "/api/workspace-members", `{"workspace_id":"ws_b","user_id":"ua","role":"owner"}`, http.StatusOK},

		{"workspace creates user in own workspace", wsAdmin, func(h *Handler) http.HandlerFunc { return h.CreateUser }, "/api/users", `{"email":"new@example.com"}`, http.StatusCreated},
		{"workspace cannot create user in other tenant", wsAdmin, func(h *Handler) http.HandlerFunc { return h.CreateUser }, "/api/users", `{"email":"new@example.com","workspace_id":"ws_b"}`, http.StatusNotFound},
		{"organization needs a workspace for a user", orgAdmin, func(h *Handler) http.HandlerFunc { return h.CreateUser }, "/api/users", `{"email":"new@example.com"}`, http.StatusBadRequest},
		{"organization creates user in its workspace", orgAdmin, func(h *Handler) http.HandlerFunc { return h.CreateUser }, "/api/users", `{"email":"new@example.com","workspace_id":"ws_a2"}`, http.StatusCreated},
		{"organization cannot create user in other organization", orgAdmin, func(h *Handler) http.HandlerFunc { return h.CreateUser }, "/api/users", `{"email":"new@example.com","workspace_id":"ws_b"}`, http.StatusNotFound},
		{"member cannot grant admin through user create", wsMember, func(h *Handler) http.HandlerFunc { return h.CreateUser }, "/api/users", `{"email":"new@example.com","role":"admin"}`, http.StatusForbidden},
		{"workspace cannot demote owner through user create", wsAdmin, func(h *Handler) http.HandlerFunc { return h.CreateUser }, "/api/users", `{"email":"boss@example.com","role":"viewer"}`, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, h := seedOrgTenants(t)
			rec := call(tc.caller, tc.fn(h), http.MethodPost, tc.path, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

func TestOrgCreatedRecordsStayInTenant(t *testing.T) {
	st, h := seedOrgTenants(t)
	rec := call(orgAdmin, h.CreateWorkspace, http.MethodPost, "/api/workspaces", `{"name":"new"}`)
	var ws store.Workspace
	if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil || ws.OrganizationID != "org_a" {
		t.Fatalf("created workspace = %+v, %v; want it in org_a", ws, err)
	}

	// A workspace admin "creating" a user that already exists elsewhere
	// adds the shared identity to its workspace but never renames it.
	rec = call(wsAdmin, h.CreateUser, http.MethodPost, "/api/users", `{"email":"ub@example.com","name":"hijacked","metadata":{"x":"y"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("CreateUser status = %d, body = %s", rec.Code, rec.Body.String())
	}
	user, err := st.GetUserByID(t.Context(), "ub")
	if err != nil || user.Name != "ub" || len(user.Metadata) != 0 {
		t.Fatalf("user ub = %+v, %v; want unchanged", user, err)
	}
	if m, err := st.GetWorkspaceMember(t.Context(), "ws_a1", "ub"); err != nil || m.Role != "viewer" {
		t.Fatalf("membership = %+v, %v; want viewer in ws_a1", m, err)
	}
	if _, err := st.GetWorkspaceMember(t.Context(), "ws_b", "ub"); err != nil {
		t.Fatalf("membership in ws_b lost: %v", err)
	}

	// A new user created by a workspace admin is visible to it and not to
	// another tenant.
	call(wsAdmin, h.CreateUser, http.MethodPost, "/api/users", `{"email":"fresh@example.com","name":"Fresh"}`)
	otherTenant := &gateway.Identity{Role: "admin", WorkspaceID: "ws_b", OrganizationID: "org_b"}
	for _, id := range listIDs(t, call(otherTenant, h.ListUsers, http.MethodGet, "/api/users", ""), "users") {
		if strings.HasPrefix(id, "user_") {
			t.Fatalf("ws_b sees user %s created in ws_a1", id)
		}
	}

	// Global callers keep the old upsert behavior.
	rec = call(globalAdmin, h.CreateUser, http.MethodPost, "/api/users", `{"email":"ub@example.com","name":"renamed"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("global CreateUser status = %d", rec.Code)
	}
	if user, _ := st.GetUserByID(t.Context(), "ub"); user.Name != "renamed" {
		t.Fatalf("global rename = %+v", user)
	}
}
