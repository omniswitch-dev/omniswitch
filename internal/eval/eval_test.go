package eval

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplayPolicies(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "production-delete.cel")
	if err := os.WriteFile(policyPath, []byte(`
// name: production-delete
// reason: Production deletes are blocked.
resource.environment == "production" && action.name == "delete"
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	handler := New()
	rec := httptest.NewRecorder()
	handler.ReplayPolicies(rec, httptest.NewRequest(http.MethodPost, "/api/evals/policy", strings.NewReader(`{
		"policy_paths":["`+strings.ReplaceAll(policyPath, `\`, `\\`)+`"],
		"requests":[
			{
				"agent":{"id":"coder"},
				"tool":{"name":"github"},
				"action":{"name":"delete"},
				"resource":{"type":"repo","name":"payments","environment":"production"}
			},
			{
				"agent":{"id":"coder"},
				"tool":{"name":"github"},
				"action":{"name":"read"},
				"resource":{"type":"repo","name":"payments","environment":"production"}
			}
		]
	}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("ReplayPolicies status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"total":2`) || !strings.Contains(body, `"allowed":1`) || !strings.Contains(body, `"denied":1`) {
		t.Fatalf("body = %s, want one allowed and one denied decision", body)
	}
}

func TestReplayPoliciesValidation(t *testing.T) {
	handler := New()
	rec := httptest.NewRecorder()
	handler.ReplayPolicies(rec, httptest.NewRequest(http.MethodPost, "/api/evals/policy", strings.NewReader(`{"requests":[]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
