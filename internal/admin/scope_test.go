package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func asCaller(r *http.Request, role, workspace string) *http.Request {
	return r.WithContext(gateway.ContextWithIdentity(r.Context(), gateway.Identity{APIKeyID: "caller", Role: role, WorkspaceID: workspace}))
}

func createKey(t *testing.T, h *Handler, body, role, workspace string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(body))
	if role != "" {
		req = asCaller(req, role, workspace)
	}
	rec := httptest.NewRecorder()
	h.CreateAPIKey(rec, req)
	return rec
}

func TestCreateAPIKeyCannotEscalateRole(t *testing.T) {
	h := New(newAdminTestStore(t))
	if rec := createKey(t, h, `{"name":"x","role":"owner"}`, "admin", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("admin creating owner: status %d, want 403", rec.Code)
	}
	if rec := createKey(t, h, `{"name":"x","role":"admin"}`, "admin", ""); rec.Code != http.StatusCreated {
		t.Fatalf("admin creating admin: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := createKey(t, h, `{"name":"x","role":"owner"}`, "owner", ""); rec.Code != http.StatusCreated {
		t.Fatalf("owner creating owner: status %d", rec.Code)
	}
}

func TestCreateAPIKeyStaysInCallerWorkspace(t *testing.T) {
	st := newAdminTestStore(t)
	h := New(st)
	if rec := createKey(t, h, `{"name":"x","workspace_id":"ws_b"}`, "admin", "ws_a"); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-workspace create: status %d, want 403", rec.Code)
	}
	rec := createKey(t, h, `{"name":"x"}`, "admin", "ws_a")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
	var created CreateKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.APIKey.WorkspaceID != "ws_a" {
		t.Fatalf("workspace = %q, want ws_a", created.APIKey.WorkspaceID)
	}
}

func TestCreateAPIKeyRejectsInvalidExpiry(t *testing.T) {
	h := New(newAdminTestStore(t))
	for _, value := range []string{"30d", "-1h", "0s", "soon"} {
		if rec := createKey(t, h, `{"name":"x","expires_in":"`+value+`"}`, "", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("expires_in %q: status %d, want 400", value, rec.Code)
		}
	}
	rec := createKey(t, h, `{"name":"x","expires_in":"1h"}`, "", "")
	var created CreateKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.APIKey.ExpiresAt == nil {
		t.Fatalf("valid expiry: status %d, key %+v", rec.Code, created.APIKey)
	}
}

func TestListAndDeleteAPIKeysAreWorkspaceScoped(t *testing.T) {
	st := newAdminTestStore(t)
	h := New(st)
	ctx := context.Background()
	for _, key := range []store.APIKey{
		{ID: "key_a", Name: "a", KeyHash: "ha", KeyPrefix: "a", WorkspaceID: "ws_a", Role: "member", Enabled: true, RateLimit: 1},
		{ID: "key_b", Name: "b", KeyHash: "hb", KeyPrefix: "b", WorkspaceID: "ws_b", Role: "member", Enabled: true, RateLimit: 1},
		{ID: "key_owner", Name: "o", KeyHash: "ho", KeyPrefix: "o", WorkspaceID: "ws_a", Role: "owner", Enabled: true, RateLimit: 1},
	} {
		if err := st.InsertAPIKey(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	h.ListAPIKeys(rec, asCaller(httptest.NewRequest(http.MethodGet, "/api/keys", nil), "admin", "ws_a"))
	if strings.Contains(rec.Body.String(), "key_b") || !strings.Contains(rec.Body.String(), "key_a") {
		t.Fatalf("scoped list = %s", rec.Body.String())
	}

	del := func(id string) int {
		rec := httptest.NewRecorder()
		h.DeleteAPIKey(rec, asCaller(httptest.NewRequest(http.MethodDelete, "/api/keys?id="+id, nil), "admin", "ws_a"))
		return rec.Code
	}
	if code := del("key_b"); code != http.StatusNotFound {
		t.Fatalf("cross-workspace delete: %d, want 404", code)
	}
	if code := del("missing"); code != http.StatusNotFound {
		t.Fatalf("missing delete: %d, want 404", code)
	}
	if code := del("key_owner"); code != http.StatusForbidden {
		t.Fatalf("admin deleting owner key: %d, want 403", code)
	}
	if code := del("key_a"); code != http.StatusOK {
		t.Fatalf("same-workspace delete: %d, want 200", code)
	}
	if other, err := st.GetAPIKeyByID(ctx, "key_b"); err != nil || !other.Enabled {
		t.Fatalf("key_b = %+v, %v; want still enabled", other, err)
	}
}

// An organization-scoped caller (an OIDC token with an organization claim
// and no workspace claim) manages only the keys of its organization's
// workspaces. It cannot see, create or delete global keys or keys of
// another organization.
func TestAPIKeysAreOrganizationScoped(t *testing.T) {
	st := newAdminTestStore(t)
	h := New(st)
	ctx := context.Background()
	for _, org := range []string{"org_a", "org_b"} {
		if err := st.InsertOrganization(ctx, store.Organization{ID: org, Name: org}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ws := range []store.Workspace{{ID: "ws_a1", OrganizationID: "org_a"}, {ID: "ws_a2", OrganizationID: "org_a"}, {ID: "ws_b", OrganizationID: "org_b"}} {
		ws.Name = ws.ID
		if err := st.InsertWorkspace(ctx, ws); err != nil {
			t.Fatal(err)
		}
	}
	for _, ws := range []string{"", "ws_a1", "ws_a2", "ws_b"} {
		id := "key_global"
		if ws != "" {
			id = "key_" + ws
		}
		if err := st.InsertAPIKey(ctx, store.APIKey{ID: id, Name: id, KeyHash: "h" + id, KeyPrefix: id, WorkspaceID: ws, Role: "member", Enabled: true, RateLimit: 1}); err != nil {
			t.Fatal(err)
		}
	}
	orgCaller := func(r *http.Request) *http.Request {
		return r.WithContext(gateway.ContextWithIdentity(r.Context(), gateway.Identity{Subject: "alice", AuthMethod: "oidc", Role: "admin", OrganizationID: "org_a"}))
	}

	rec := httptest.NewRecorder()
	h.ListAPIKeys(rec, orgCaller(httptest.NewRequest(http.MethodGet, "/api/keys", nil)))
	var listed struct {
		Keys []store.APIKey `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list: %v %s", err, rec.Body.String())
	}
	var ids []string
	for _, key := range listed.Keys {
		ids = append(ids, key.ID)
	}
	if strings.Join(ids, ",") != "key_ws_a2,key_ws_a1" && strings.Join(ids, ",") != "key_ws_a1,key_ws_a2" {
		t.Fatalf("organization list = %v, want only the keys of org_a", ids)
	}

	create := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.CreateAPIKey(rec, orgCaller(httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(body))))
		return rec
	}
	if rec := create(`{"name":"no-workspace","role":"member"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("create without workspace = %d %s, want 400", rec.Code, rec.Body.String())
	}
	for _, ws := range []string{"ws_b", "ws_missing"} {
		if rec := create(`{"name":"other","role":"member","workspace_id":"` + ws + `"}`); rec.Code != http.StatusNotFound {
			t.Fatalf("create in %s = %d %s, want 404", ws, rec.Code, rec.Body.String())
		}
	}
	rec = create(`{"name":"mine","role":"member","workspace_id":"ws_a2"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create in own organization = %d %s, want 201", rec.Code, rec.Body.String())
	}
	var created CreateKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.APIKey.WorkspaceID != "ws_a2" {
		t.Fatalf("created key = %+v, %v; want workspace ws_a2", created.APIKey, err)
	}

	del := func(id string) int {
		rec := httptest.NewRecorder()
		h.DeleteAPIKey(rec, orgCaller(httptest.NewRequest(http.MethodDelete, "/api/keys?id="+id, nil)))
		return rec.Code
	}
	for _, id := range []string{"key_global", "key_ws_b"} {
		if code := del(id); code != http.StatusNotFound {
			t.Fatalf("delete %s = %d, want 404", id, code)
		}
		if key, err := st.GetAPIKeyByID(ctx, id); err != nil || !key.Enabled {
			t.Fatalf("%s = %+v, %v; want still enabled", id, key, err)
		}
	}
	if code := del("key_ws_a1"); code != http.StatusOK {
		t.Fatalf("delete key_ws_a1 = %d, want 200", code)
	}
}
