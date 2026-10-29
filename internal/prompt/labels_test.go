package prompt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestPromptPromotionRollbackAndLiteralVariables(t *testing.T) {
	st := newPromptTestStore(t)
	h := New(st)
	for index, template := range []string{"first {{name}} {{ticket}}", "second {{name}}"} {
		p := store.Prompt{ID: []string{"p1", "p2"}[index], Name: "support", Version: index + 1, Template: template, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := st.InsertPrompt(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ id, want string }{
		{"p1", "first {{ticket}} INC-1"},
		{"p2", "second {{ticket}}"},
		{"p1", "first {{ticket}} INC-1"},
	} {
		label := httptest.NewRecorder()
		h.Labels(label, httptest.NewRequest(http.MethodPost, "/api/prompts/labels", strings.NewReader(`{"name":"support","label":"production","prompt_id":"`+tc.id+`"}`)))
		if label.Code != 200 {
			t.Fatalf("label status/body = %d/%s", label.Code, label.Body)
		}
		render := httptest.NewRecorder()
		h.RenderPrompt(render, httptest.NewRequest(http.MethodPost, "/api/prompts/render", strings.NewReader(`{"name":"support","label":"production","variables":{"name":"{{ticket}}","ticket":"INC-1"}}`)))
		var rendered map[string]string
		if err := json.Unmarshal(render.Body.Bytes(), &rendered); err != nil {
			t.Fatal(err)
		}
		if render.Code != 200 || rendered["rendered"] != tc.want {
			t.Fatalf("render status/body = %d/%s", render.Code, render.Body)
		}
	}
	list := httptest.NewRecorder()
	h.Labels(list, httptest.NewRequest(http.MethodGet, "/api/prompts/labels?name=support", nil))
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"prompt_id":"p1"`) {
		t.Fatalf("labels = %d/%s", list.Code, list.Body)
	}
	versions, err := st.ListPromptVersions(context.Background(), "support")
	if err != nil || len(versions) != 2 || versions[0].Template != "second {{name}}" {
		t.Fatalf("version history changed: %v/%v", versions, err)
	}
	// A label can never point to a version belonging to another prompt.
	bad := httptest.NewRecorder()
	h.Labels(bad, httptest.NewRequest(http.MethodPost, "/api/prompts/labels", strings.NewReader(`{"name":"other","label":"production","prompt_id":"p1"}`)))
	if bad.Code != 404 {
		t.Fatalf("cross-prompt assignment = %d", bad.Code)
	}
}

func TestPromptLabelAndRenderValidation(t *testing.T) {
	h := New(newPromptTestStore(t))
	for _, tc := range []struct {
		method, url, body string
		status            int
	}{
		{"POST", "/api/prompts/labels", `{"name":"support","label":"bad label","prompt_id":"p1"}`, 400},
		{"POST", "/api/prompts/labels", `{"name":"support","label":"production","prompt_id":"missing"}`, 404},
		{"POST", "/api/prompts/labels", `{broken`, 400},
		{"GET", "/api/prompts/labels", ``, 400},
		{"DELETE", "/api/prompts/labels", ``, 405},
		{"POST", "/api/prompts/render", `{"prompt_id":"p1","name":"support","label":"production"}`, 400},
		{"POST", "/api/prompts/render", `{"name":"support"}`, 400},
		{"POST", "/api/prompts/render", `{"name":"support","label":"missing"}`, 404},
		{"GET", "/api/prompts/render", ``, 405},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.url, strings.NewReader(tc.body))
		if strings.Contains(tc.url, "labels") {
			h.Labels(rec, req)
		} else {
			h.RenderPrompt(rec, req)
		}
		if rec.Code != tc.status {
			t.Errorf("%s %s %s = %d/%s, want %d", tc.method, tc.url, tc.body, rec.Code, rec.Body, tc.status)
		}
	}
}
