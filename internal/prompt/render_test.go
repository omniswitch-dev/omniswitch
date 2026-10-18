package prompt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestRenderTemplateSinglePass(t *testing.T) {
	cases := []struct {
		name, template string
		vars           map[string]string
		want           string
	}{
		{"basic", "Hi {{name}}, {{name}}!", map[string]string{"name": "Ana"}, "Hi Ana, Ana!"},
		{"unknown kept", "{{a}} {{b}}", map[string]string{"a": "x"}, "x {{b}}"},
		{"no recursion", "{{a}}", map[string]string{"a": "{{b}}{{b}}", "b": "boom"}, "{{b}}{{b}}"},
		{"value with own placeholder", "{{a}}", map[string]string{"a": "{{a}}"}, "{{a}}"},
		{"empty template", "", map[string]string{"a": "x"}, ""},
		// Names outside \w were filled by earlier releases and still are.
		{"hyphenated name", "Dear {{first-name}}", map[string]string{"first-name": "Ana"}, "Dear Ana"},
		{"dotted name", "{{user.name}}", map[string]string{"user.name": "Ana"}, "Ana"},
		{"non-ASCII name", "Bonjour {{prénom}}", map[string]string{"prénom": "Zoé"}, "Bonjour Zoé"},
		{"exact lookup", "{{ name }} {{name}}", map[string]string{"name": "Ana"}, "{{ name }} Ana"},
		{"spaced name", "{{ name }}", map[string]string{" name ": "Ana"}, "Ana"},
		{"extra braces", "{{{name}}}", map[string]string{"name": "Ana"}, "{Ana}"},
	}
	for _, tc := range cases {
		got, err := renderTemplate(tc.template, tc.vars, MaxRenderedBytes)
		if err != nil || got != tc.want {
			t.Errorf("%s: renderTemplate = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	if _, err := renderTemplate(strings.Repeat("{{a}}", 10), map[string]string{"a": "0123456789"}, 99); err == nil {
		t.Fatal("expected size limit error")
	}
	if got, err := renderTemplate(strings.Repeat("{{a}}", 10), map[string]string{"a": "0123456789"}, 100); err != nil || len(got) != 100 {
		t.Fatalf("exact limit: len=%d err=%v", len(got), err)
	}
}

func TestRenderPromptBlocksExpansionAmplification(t *testing.T) {
	st := newPromptTestStore(t)
	if err := st.InsertPrompt(context.Background(), store.Prompt{
		ID: "p1", Name: "amp", Version: 1, Template: "{{a}}", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	handler := New(st)
	vars := map[string]string{
		"a": strings.Repeat("{{b}}", 200),
		"b": strings.Repeat("{{c}}", 200),
		"c": strings.Repeat("{{d}}", 200),
		"d": strings.Repeat("x", 10),
	}
	body, _ := json.Marshal(RenderRequest{PromptID: "p1", Variables: vars})
	for range 20 {
		rec := httptest.NewRecorder()
		handler.RenderPrompt(rec, httptest.NewRequest(http.MethodPost, "/api/prompts/render", strings.NewReader(string(body))))
		if rec.Code != http.StatusOK || rec.Body.Len() > 4096 {
			t.Fatalf("status = %d, len = %d", rec.Code, rec.Body.Len())
		}
	}
}

func TestRenderPromptRejectsOversizedVariables(t *testing.T) {
	handler := New(newPromptTestStore(t))
	tooMany := map[string]string{}
	for i := range MaxRenderVariables + 1 {
		tooMany[fmt.Sprintf("v%d", i)] = "x"
	}
	for name, vars := range map[string]map[string]string{
		"too many": tooMany,
		"too big":  {"a": strings.Repeat("x", MaxRenderVariableBytes+1)},
	} {
		body, _ := json.Marshal(RenderRequest{PromptID: "p1", Variables: vars})
		rec := httptest.NewRecorder()
		handler.RenderPrompt(rec, httptest.NewRequest(http.MethodPost, "/api/prompts/render", strings.NewReader(string(body))))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
}
