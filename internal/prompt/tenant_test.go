package prompt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

var (
	globalCaller = &gateway.Identity{Role: "admin"}
	orgCaller    = &gateway.Identity{Role: "member", OrganizationID: "org_a"}
	wsA          = &gateway.Identity{Role: "member", WorkspaceID: "ws_a", OrganizationID: "org_a"}
	wsB          = &gateway.Identity{Role: "member", WorkspaceID: "ws_b", OrganizationID: "org_b"}
)

func promptCall(caller *gateway.Identity, fn http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if caller != nil {
		r = r.WithContext(gateway.ContextWithIdentity(r.Context(), *caller))
	}
	rec := httptest.NewRecorder()
	fn(rec, r)
	return rec
}

func decodePrompt(t *testing.T, rec *httptest.ResponseRecorder) store.Prompt {
	t.Helper()
	if rec.Code != http.StatusCreated {
		t.Fatalf("CreatePrompt status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var p store.Prompt
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func promptIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Prompts []store.Prompt `json:"prompts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, p := range body.Prompts {
		ids = append(ids, p.WorkspaceID+":"+p.Name+":"+strconv.Itoa(p.Version))
	}
	sort.Strings(ids)
	return ids
}

func seedPromptTenants(t *testing.T) (*Handler, map[string]store.Prompt) {
	t.Helper()
	st := newPromptTestStore(t)
	for _, ws := range []store.Workspace{{ID: "ws_a", OrganizationID: "org_a"}, {ID: "ws_a2", OrganizationID: "org_a"}, {ID: "ws_b", OrganizationID: "org_b"}} {
		ws.Name = ws.ID
		if err := st.InsertWorkspace(t.Context(), ws); err != nil {
			t.Fatal(err)
		}
	}
	h := New(st)
	created := map[string]store.Prompt{
		"global": decodePrompt(t, promptCall(globalCaller, h.CreatePrompt, http.MethodPost, "/api/prompts", `{"name":"support","template":"global {{x}}"}`)),
		"a":      decodePrompt(t, promptCall(wsA, h.CreatePrompt, http.MethodPost, "/api/prompts", `{"name":"support","template":"a {{x}}"}`)),
		"b":      decodePrompt(t, promptCall(wsB, h.CreatePrompt, http.MethodPost, "/api/prompts", `{"name":"support","template":"b {{x}}"}`)),
	}
	return h, created
}

func TestPromptNamespacesPerWorkspace(t *testing.T) {
	h, created := seedPromptTenants(t)
	for key, want := range map[string]string{"global": "", "a": "ws_a", "b": "ws_b"} {
		if p := created[key]; p.WorkspaceID != want || p.Version != 1 {
			t.Fatalf("prompt %s = workspace %q version %d, want %q version 1", key, p.WorkspaceID, p.Version, want)
		}
	}
	// A second version in ws_a does not touch ws_b's prompt of the same name.
	v2 := decodePrompt(t, promptCall(wsA, h.CreatePrompt, http.MethodPost, "/api/prompts", `{"name":"support","template":"a2"}`))
	if v2.Version != 2 || v2.WorkspaceID != "ws_a" {
		t.Fatalf("ws_a v2 = %+v", v2)
	}

	cases := []struct {
		name   string
		caller *gateway.Identity
		query  string
		want   string
	}{
		{"auth off", nil, "", ":support:1"},
		{"global default namespace", globalCaller, "", ":support:1"},
		{"global names a workspace", globalCaller, "?workspace_id=ws_b", "ws_b:support:1"},
		{"workspace a", wsA, "", "ws_a:support:1,ws_a:support:2"},
		{"workspace a names itself", wsA, "?workspace_id=ws_a", "ws_a:support:1,ws_a:support:2"},
		{"workspace b", wsB, "", "ws_b:support:1"},
		{"organization names its workspace", orgCaller, "?workspace_id=ws_a", "ws_a:support:1,ws_a:support:2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(promptIDs(t, promptCall(tc.caller, h.ListPrompts, http.MethodGet, "/api/prompts"+tc.query, "")), ",")
			if got != tc.want {
				t.Fatalf("ListPrompts = %s, want %s", got, tc.want)
			}
			sep := "?"
			if tc.query != "" {
				sep = "&"
			}
			got = strings.Join(promptIDs(t, promptCall(tc.caller, h.ListPromptVersions, http.MethodGet, "/api/prompts/versions"+tc.query+sep+"name=support", "")), ",")
			if got != tc.want {
				t.Fatalf("ListPromptVersions = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPromptCrossTenantAccessIsRejected(t *testing.T) {
	h, created := seedPromptTenants(t)
	render := func(id, extra string) string {
		return `{"prompt_id":"` + id + `","variables":{"x":"1"}` + extra + `}`
	}
	cases := []struct {
		name   string
		caller *gateway.Identity
		fn     func() http.HandlerFunc
		method string
		target string
		body   string
		status int
	}{
		{"workspace lists another workspace", wsA, func() http.HandlerFunc { return h.ListPrompts }, http.MethodGet, "/api/prompts?workspace_id=ws_b", "", http.StatusNotFound},
		{"workspace lists versions of another workspace", wsA, func() http.HandlerFunc { return h.ListPromptVersions }, http.MethodGet, "/api/prompts/versions?name=support&workspace_id=ws_b", "", http.StatusNotFound},
		{"workspace creates in another workspace", wsA, func() http.HandlerFunc { return h.CreatePrompt }, http.MethodPost, "/api/prompts", `{"name":"support","template":"x","workspace_id":"ws_b"}`, http.StatusNotFound},
		{"workspace creates in global namespace", wsA, func() http.HandlerFunc { return h.CreatePrompt }, http.MethodPost, "/api/prompts", `{"name":"support","template":"x","workspace_id":" "}`, http.StatusCreated},
		{"workspace renders another workspace's prompt", wsA, func() http.HandlerFunc { return h.RenderPrompt }, http.MethodPost, "/api/prompts/render", render(created["b"].ID, ""), http.StatusNotFound},
		{"workspace renders a global prompt", wsA, func() http.HandlerFunc { return h.RenderPrompt }, http.MethodPost, "/api/prompts/render", render(created["global"].ID, ""), http.StatusNotFound},
		{"workspace forges workspace_id on render", wsA, func() http.HandlerFunc { return h.RenderPrompt }, http.MethodPost, "/api/prompts/render", render(created["b"].ID, `,"workspace_id":"ws_b"`), http.StatusNotFound},
		{"workspace renders own prompt", wsA, func() http.HandlerFunc { return h.RenderPrompt }, http.MethodPost, "/api/prompts/render", render(created["a"].ID, ""), http.StatusOK},
		{"organization without workspace", orgCaller, func() http.HandlerFunc { return h.ListPrompts }, http.MethodGet, "/api/prompts", "", http.StatusBadRequest},
		{"organization creates in other organization", orgCaller, func() http.HandlerFunc { return h.CreatePrompt }, http.MethodPost, "/api/prompts", `{"name":"n","template":"x","workspace_id":"ws_b"}`, http.StatusNotFound},
		{"organization creates in unknown workspace", orgCaller, func() http.HandlerFunc { return h.CreatePrompt }, http.MethodPost, "/api/prompts", `{"name":"n","template":"x","workspace_id":"ws_zz"}`, http.StatusNotFound},
		{"organization creates in its workspace", orgCaller, func() http.HandlerFunc { return h.CreatePrompt }, http.MethodPost, "/api/prompts", `{"name":"n","template":"x","workspace_id":"ws_a2"}`, http.StatusCreated},
		{"organization renders other organization's prompt", orgCaller, func() http.HandlerFunc { return h.RenderPrompt }, http.MethodPost, "/api/prompts/render", render(created["b"].ID, `,"workspace_id":"ws_a"`), http.StatusNotFound},
		{"organization renders its prompt", orgCaller, func() http.HandlerFunc { return h.RenderPrompt }, http.MethodPost, "/api/prompts/render", render(created["a"].ID, `,"workspace_id":"ws_a"`), http.StatusOK},
		{"global renders any prompt by id", globalCaller, func() http.HandlerFunc { return h.RenderPrompt }, http.MethodPost, "/api/prompts/render", render(created["b"].ID, ""), http.StatusOK},
		{"global renders with wrong namespace", globalCaller, func() http.HandlerFunc { return h.RenderPrompt }, http.MethodPost, "/api/prompts/render", render(created["b"].ID, `,"workspace_id":"ws_a"`), http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := promptCall(tc.caller, tc.fn(), tc.method, tc.target, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.status, rec.Body.String())
			}
		})
	}

	// The forged creates above must not have added a version to ws_b.
	got := strings.Join(promptIDs(t, promptCall(wsB, h.ListPrompts, http.MethodGet, "/api/prompts", "")), ",")
	if got != "ws_b:support:1" {
		t.Fatalf("ws_b prompts after forged writes = %s", got)
	}
	// The blank workspace_id from ws_a landed in ws_a, not in the global
	// namespace.
	got = strings.Join(promptIDs(t, promptCall(globalCaller, h.ListPrompts, http.MethodGet, "/api/prompts", "")), ",")
	if got != ":support:1" {
		t.Fatalf("global prompts after scoped writes = %s", got)
	}
}
