package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestTenantIsolationOrgVaultPrompts covers SEC-3 part A end to end through
// the real authentication middleware: two workspace-scoped admin keys and
// the global bootstrap key share one gateway.
func TestTenantIsolationOrgVaultPrompts(t *testing.T) {
	h := newSecurityTestServer(t, nil)
	global := routeTestKey

	decodeID := func(t *testing.T, path, body string) string {
		t.Helper()
		rec := doRequest(h, http.MethodPost, path, global, body, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST %s = %d, body = %s", path, rec.Code, rec.Body.String())
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.ID
	}
	orgA := decodeID(t, "/api/orgs", `{"name":"A"}`)
	orgB := decodeID(t, "/api/orgs", `{"name":"B"}`)
	wsA := decodeID(t, "/api/workspaces", `{"organization_id":"`+orgA+`","name":"a"}`)
	wsB := decodeID(t, "/api/workspaces", `{"organization_id":"`+orgB+`","name":"b"}`)
	keyA := createTestKey(t, h, global, `{"name":"a","workspace_id":"`+wsA+`","role":"admin"}`)
	keyB := createTestKey(t, h, global, `{"name":"b","workspace_id":"`+wsB+`","role":"admin"}`)

	get := func(key, path string) string {
		t.Helper()
		rec := doRequest(h, http.MethodGet, path, key, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, body = %s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// Organizations and workspaces.
	if body := get(keyA, "/api/orgs"); !strings.Contains(body, orgA) || strings.Contains(body, orgB) {
		t.Fatalf("workspace A sees organizations %s", body)
	}
	if body := get(keyA, "/api/workspaces"); !strings.Contains(body, wsA) || strings.Contains(body, wsB) {
		t.Fatalf("workspace A sees workspaces %s", body)
	}
	if body := get(global, "/api/workspaces"); !strings.Contains(body, wsA) || !strings.Contains(body, wsB) {
		t.Fatalf("global sees workspaces %s", body)
	}
	if rec := doRequest(h, http.MethodPost, "/api/orgs", keyA, `{"name":"evil"}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("workspace A POST /api/orgs = %d, want 403", rec.Code)
	}
	if rec := doRequest(h, http.MethodPost, "/api/workspaces", keyA, `{"organization_id":"`+orgB+`","name":"evil"}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("workspace A POST /api/workspaces = %d, want 403", rec.Code)
	}

	// Users and members.
	if rec := doRequest(h, http.MethodPost, "/api/users", keyB, `{"email":"bob@b.example","name":"Bob"}`, nil); rec.Code != http.StatusCreated {
		t.Fatalf("workspace B creates user = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body := get(keyA, "/api/users"); strings.Contains(body, "bob@b.example") {
		t.Fatalf("workspace A sees workspace B's user: %s", body)
	}
	if body := get(keyA, "/api/workspace-members?workspace_id="+wsB); strings.Contains(body, wsB) {
		t.Fatalf("workspace A sees workspace B's members: %s", body)
	}
	if body := get(global, "/api/users"); !strings.Contains(body, "bob@b.example") {
		t.Fatalf("global does not see Bob: %s", body)
	}
	if rec := doRequest(h, http.MethodPost, "/api/workspace-members", keyA, `{"workspace_id":"`+wsB+`","user_id":"x","role":"viewer"}`, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("workspace A adds member to B = %d, want 404", rec.Code)
	}
	if rec := doRequest(h, http.MethodPost, "/api/users", keyA, `{"email":"bob@b.example","name":"Hijacked","workspace_id":"`+wsB+`"}`, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("workspace A creates user in B = %d, want 404", rec.Code)
	}

	// Provider vault.
	if rec := doRequest(h, http.MethodGet, "/api/virtual-keys", keyA, "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("workspace A GET /api/virtual-keys = %d, want 403", rec.Code)
	}
	if rec := doRequest(h, http.MethodPost, "/api/virtual-keys/rotate", keyA, `{"name":"x","provider_key":"k"}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("workspace A rotate = %d, want 403", rec.Code)
	}
	get(global, "/api/virtual-keys")

	// Prompts: the same name lives in both workspaces independently.
	create := func(key, template string) (id string, version int) {
		t.Helper()
		rec := doRequest(h, http.MethodPost, "/api/prompts", key, `{"name":"support","template":"`+template+`"}`, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create prompt = %d, body = %s", rec.Code, rec.Body.String())
		}
		var p struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p.ID, p.Version
	}
	promptA, vA := create(keyA, "A says {{x}}")
	promptB, vB := create(keyB, "B says {{x}}")
	_, vG := create(global, "global {{x}}")
	if vA != 1 || vB != 1 || vG != 1 {
		t.Fatalf("versions = %d/%d/%d, want 1 in every namespace", vA, vB, vG)
	}
	if body := get(keyA, "/api/prompts/versions?name=support"); !strings.Contains(body, "A says") || strings.Contains(body, "B says") || strings.Contains(body, "global") {
		t.Fatalf("workspace A versions = %s", body)
	}
	if rec := doRequest(h, http.MethodPost, "/api/prompts", keyA, `{"name":"support","template":"x","workspace_id":"`+wsB+`"}`, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("workspace A publishes into B = %d, want 404", rec.Code)
	}
	if rec := doRequest(h, http.MethodPost, "/api/prompts/render", keyA, `{"prompt_id":"`+promptB+`","variables":{"x":"1"}}`, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("workspace A renders B's prompt = %d, want 404", rec.Code)
	}
	if rec := doRequest(h, http.MethodPost, "/api/prompts/render", keyA, `{"prompt_id":"`+promptA+`","variables":{"x":"1"}}`, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "A says 1") {
		t.Fatalf("workspace A renders own prompt = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body := get(global, "/api/prompts?workspace_id="+wsB); !strings.Contains(body, "B says") || strings.Contains(body, "A says") {
		t.Fatalf("global view of workspace B prompts = %s", body)
	}
}
