package prompt

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestPromptLabelsRespectWorkspaceNamespaces(t *testing.T) {
	h, versions := seedPromptTenants(t)
	for _, tc := range []struct {
		name string
		key  string
	}{{"global", "global"}, {"workspace a", "a"}, {"workspace b", "b"}} {
		t.Run(tc.name, func(t *testing.T) {
			caller := globalCaller
			if tc.key == "a" {
				caller = wsA
			}
			if tc.key == "b" {
				caller = wsB
			}
			body := `{"name":"support","label":"production","prompt_id":"` + versions[tc.key].ID + `"}`
			rec := promptCall(caller, h.Labels, http.MethodPost, "/api/prompts/labels", body)
			if rec.Code != 200 {
				t.Fatalf("label = %d/%s", rec.Code, rec.Body)
			}
			list := promptCall(caller, h.Labels, http.MethodGet, "/api/prompts/labels?name=support", "")
			var response struct {
				Labels []store.PromptLabel `json:"labels"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if list.Code != 200 || len(response.Labels) != 1 || response.Labels[0].PromptID != versions[tc.key].ID {
				t.Fatalf("labels = %d/%s", list.Code, list.Body)
			}
			render := promptCall(caller, h.RenderPrompt, http.MethodPost, "/api/prompts/render", `{"name":"support","label":"production","variables":{"x":"7"}}`)
			want := map[string]string{"global": "global 7", "a": "a 7", "b": "b 7"}[tc.key]
			if render.Code != 200 || !strings.Contains(render.Body.String(), want) {
				t.Fatalf("render = %d/%s", render.Code, render.Body)
			}
		})
	}
	for _, tc := range []struct{ method, target, body string }{
		{"POST", "/api/prompts/labels", `{"name":"support","label":"production","prompt_id":"` + versions["b"].ID + `"}`},
		{"POST", "/api/prompts/labels", `{"name":"support","label":"production","workspace_id":"ws_b","prompt_id":"` + versions["b"].ID + `"}`},
		{"GET", "/api/prompts/labels?name=support&workspace_id=ws_b", ""},
		{"POST", "/api/prompts/render", `{"name":"support","label":"production","workspace_id":"ws_b"}`},
	} {
		fn := h.Labels
		if strings.Contains(tc.target, "render") {
			fn = h.RenderPrompt
		}
		rec := promptCall(wsA, fn, tc.method, tc.target, tc.body)
		if rec.Code != 404 {
			t.Errorf("cross-workspace %s %s = %d/%s", tc.method, tc.target, rec.Code, rec.Body)
		}
	}
	// An organization caller selects one of its workspaces; it cannot fall
	// back to the global namespace or a different organization's workspace.
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"", 400}, {"&workspace_id=ws_a", 200}, {"&workspace_id=ws_b", 404},
	} {
		rec := promptCall(orgCaller, h.Labels, http.MethodGet, "/api/prompts/labels?name=support"+tc.query, "")
		if rec.Code != tc.status {
			t.Errorf("organization list %s = %d/%s", tc.query, rec.Code, rec.Body)
		}
	}
	// A rejected move must leave the original workspace pointer untouched.
	rec := promptCall(wsA, h.RenderPrompt, http.MethodPost, "/api/prompts/render", `{"name":"support","label":"production","variables":{"x":"7"}}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "a 7") {
		t.Fatalf("rejected move changed label: %d/%s", rec.Code, rec.Body)
	}
}
