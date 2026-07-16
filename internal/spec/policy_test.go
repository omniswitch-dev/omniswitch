package spec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPolicy(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantExpr   string
		wantReason string
	}{
		{
			name: "match policy",
			content: `apiVersion: omniswitch.dev/v1
kind: Policy
metadata:
  name: production-delete
spec:
  match:
    tool: github
    action: delete
    environment: production
  effect: deny
  reason: Production deletes are forbidden.
`,
			wantExpr:   "tool.name == \"github\" &&\naction.name == \"delete\" &&\nresource.environment == \"production\"",
			wantReason: "Production deletes are forbidden.",
		},
		{
			name: "cel policy",
			content: `apiVersion: omniswitch.dev/v1
kind: Policy
metadata:
  name: custom-cel
spec:
  cel: tool.name == "postgres" && action.name == "drop_table"
  effect: deny
  reason: Drop table is forbidden.
`,
			wantExpr:   `tool.name == "postgres" && action.name == "drop_table"`,
			wantReason: "Drop table is forbidden.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "policy.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			document, err := LoadPolicy(path)
			if err != nil {
				t.Fatalf("LoadPolicy() error = %v", err)
			}
			expression, err := document.Expression()
			if err != nil {
				t.Fatalf("Expression() error = %v", err)
			}
			if expression != tt.wantExpr {
				t.Fatalf("Expression() = %q, want %q", expression, tt.wantExpr)
			}
			if document.Spec.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %q", document.Spec.Reason, tt.wantReason)
			}
		})
	}
}
