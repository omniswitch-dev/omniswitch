package prompt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestCreateListRenderPrompt(t *testing.T) {
	st := newPromptTestStore(t)
	handler := New(st)

	createRec := httptest.NewRecorder()
	handler.CreatePrompt(createRec, httptest.NewRequest(http.MethodPost, "/api/prompts", strings.NewReader(`{
		"name":"support",
		"template":"Hello {{name}}, your ticket is {{ticket}}. {{name}}"
	}`)))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("CreatePrompt status = %d, body = %s", createRec.Code, createRec.Body.String())
	}

	var created store.Prompt
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(created.Variables) != 2 {
		t.Fatalf("Variables = %+v, want two unique variables", created.Variables)
	}

	secondRec := httptest.NewRecorder()
	handler.CreatePrompt(secondRec, httptest.NewRequest(http.MethodPost, "/api/prompts", strings.NewReader(`{
		"name":"support",
		"template":"Updated {{name}}"
	}`)))
	if secondRec.Code != http.StatusCreated {
		t.Fatalf("second CreatePrompt status = %d, body = %s", secondRec.Code, secondRec.Body.String())
	}
	var second store.Prompt
	if err := json.Unmarshal(secondRec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second Version = %d, want 2", second.Version)
	}

	listRec := httptest.NewRecorder()
	handler.ListPrompts(listRec, httptest.NewRequest(http.MethodGet, "/api/prompts", nil))
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), `"prompts"`) {
		t.Fatalf("ListPrompts status/body = %d/%s", listRec.Code, listRec.Body.String())
	}

	versionsRec := httptest.NewRecorder()
	handler.ListPromptVersions(versionsRec, httptest.NewRequest(http.MethodGet, "/api/prompts/versions?name=support", nil))
	if versionsRec.Code != http.StatusOK || !strings.Contains(versionsRec.Body.String(), `"version":2`) {
		t.Fatalf("ListPromptVersions status/body = %d/%s", versionsRec.Code, versionsRec.Body.String())
	}

	renderRec := httptest.NewRecorder()
	handler.RenderPrompt(renderRec, httptest.NewRequest(http.MethodPost, "/api/prompts/render", strings.NewReader(`{
		"prompt_id":"`+created.ID+`",
		"variables":{"name":"Ada","ticket":"INC-1"}
	}`)))
	if renderRec.Code != http.StatusOK || !strings.Contains(renderRec.Body.String(), "Hello Ada, your ticket is INC-1. Ada") {
		t.Fatalf("RenderPrompt status/body = %d/%s", renderRec.Code, renderRec.Body.String())
	}
}

func TestRenderPromptNotFound(t *testing.T) {
	handler := New(newPromptTestStore(t))
	rec := httptest.NewRecorder()
	handler.RenderPrompt(rec, httptest.NewRequest(http.MethodPost, "/api/prompts/render", strings.NewReader(`{"prompt_id":"missing"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func newPromptTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
