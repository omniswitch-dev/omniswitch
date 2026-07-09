package trace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/internal/model"
)

func TestDocumentRoundTrip(t *testing.T) {
	document := NewDocument(
		model.ToolRequest{
			Tool:     model.Tool{Name: "github"},
			Action:   model.Action{Name: "delete"},
			Resource: model.Resource{Name: "payments-prod", Environment: "production"},
		},
		model.Decision{
			ID:             "dec_test",
			Allowed:        false,
			Rule:           "production-delete",
			Reason:         "Repository payments-prod is protected.",
			EvaluationTime: time.Millisecond,
			Trace: []model.RuleTrace{
				{
					Rule:       "production-delete",
					Expression: `resource.environment == "production"`,
					Effect:     "deny",
					Matched:    true,
					Hash:       "sha256:test",
				},
			},
		},
	)

	data, err := document.MarshalYAMLBytes()
	if err != nil {
		t.Fatalf("MarshalYAMLBytes() error = %v", err)
	}
	if !strings.Contains(string(data), "kind: DecisionTrace") {
		t.Fatalf("trace yaml = %q, want DecisionTrace kind", string(data))
	}

	path := filepath.Join(t.TempDir(), "trace.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Metadata.DecisionID != "dec_test" {
		t.Fatalf("DecisionID = %q, want dec_test", loaded.Metadata.DecisionID)
	}
}

func TestDiff(t *testing.T) {
	left := Document{
		Spec: TraceSpec{
			Result:  Result{Allowed: true, Effect: "ALLOW", Reason: "ok"},
			Request: model.ToolRequest{Resource: model.Resource{Environment: "staging"}},
		},
	}
	right := Document{
		Spec: TraceSpec{
			Result:  Result{Allowed: false, Effect: "DENY", Reason: "blocked"},
			Request: model.ToolRequest{Resource: model.Resource{Environment: "production"}},
		},
	}

	diffs := Diff(left, right)
	for _, want := range []string{"result.allowed", "result.effect", "result.reason", "request.environment"} {
		found := false
		for _, diff := range diffs {
			found = found || strings.Contains(diff, want)
		}
		if !found {
			t.Fatalf("Diff() = %#v, want entry containing %q", diffs, want)
		}
	}
}
