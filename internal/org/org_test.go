package org

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sentinel/internal/store"
)

func TestOrganizationWorkspaceUserAndMemberHandlers(t *testing.T) {
	st := newOrgTestStore(t)
	handler := New(st)

	orgRec := httptest.NewRecorder()
	handler.CreateOrganization(orgRec, httptest.NewRequest(http.MethodPost, "/api/orgs", strings.NewReader(`{"name":"Acme"}`)))
	if orgRec.Code != http.StatusCreated {
		t.Fatalf("CreateOrganization status = %d, body = %s", orgRec.Code, orgRec.Body.String())
	}
	var createdOrg store.Organization
	if err := json.Unmarshal(orgRec.Body.Bytes(), &createdOrg); err != nil {
		t.Fatalf("decode organization: %v", err)
	}

	workspaceRec := httptest.NewRecorder()
	handler.CreateWorkspace(workspaceRec, httptest.NewRequest(http.MethodPost, "/api/workspaces", strings.NewReader(`{
		"organization_id":"`+createdOrg.ID+`",
		"name":"Production"
	}`)))
	if workspaceRec.Code != http.StatusCreated {
		t.Fatalf("CreateWorkspace status = %d, body = %s", workspaceRec.Code, workspaceRec.Body.String())
	}
	var createdWorkspace store.Workspace
	if err := json.Unmarshal(workspaceRec.Body.Bytes(), &createdWorkspace); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}

	userRec := httptest.NewRecorder()
	handler.CreateUser(userRec, httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"email":"Ada@Example.com","name":"Ada"}`)))
	if userRec.Code != http.StatusCreated {
		t.Fatalf("CreateUser status = %d, body = %s", userRec.Code, userRec.Body.String())
	}
	var createdUser store.User
	if err := json.Unmarshal(userRec.Body.Bytes(), &createdUser); err != nil {
		t.Fatalf("decode user: %v", err)
	}
	if createdUser.Email != "ada@example.com" {
		t.Fatalf("created email = %q, want lowercase email", createdUser.Email)
	}

	memberRec := httptest.NewRecorder()
	handler.UpsertWorkspaceMember(memberRec, httptest.NewRequest(http.MethodPost, "/api/workspace-members", strings.NewReader(`{
		"workspace_id":"`+createdWorkspace.ID+`",
		"user_id":"`+createdUser.ID+`",
		"role":"admin"
	}`)))
	if memberRec.Code != http.StatusOK {
		t.Fatalf("UpsertWorkspaceMember status = %d, body = %s", memberRec.Code, memberRec.Body.String())
	}

	listRec := httptest.NewRecorder()
	handler.ListWorkspaceMembers(listRec, httptest.NewRequest(http.MethodGet, "/api/workspace-members?workspace_id="+createdWorkspace.ID, nil))
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), `"role":"admin"`) {
		t.Fatalf("ListWorkspaceMembers status/body = %d/%s", listRec.Code, listRec.Body.String())
	}
}

func TestCreateOrganizationValidation(t *testing.T) {
	handler := New(newOrgTestStore(t))
	rec := httptest.NewRecorder()
	handler.CreateOrganization(rec, httptest.NewRequest(http.MethodPost, "/api/orgs", strings.NewReader(`{"name":""}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func newOrgTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
