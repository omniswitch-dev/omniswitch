package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadToolRequest(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{
			name: "plain json",
			content: []byte(`{
				"tool":{"name":"github"},
				"action":{"name":"delete"},
				"resource":{"name":"payments-prod","environment":"production"}
			}`),
			want: "github",
		},
		{
			name: "utf8 bom json",
			content: append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{
				"tool":{"name":"postgres"},
				"action":{"name":"drop_table"},
				"resource":{"name":"ledger","environment":"production"}
			}`)...),
			want: "postgres",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "request.json")
			if err := os.WriteFile(path, tt.content, 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			req, err := ReadToolRequest(path)
			if err != nil {
				t.Fatalf("ReadToolRequest() error = %v", err)
			}
			if req.Tool.Name != tt.want {
				t.Fatalf("Tool.Name = %q, want %q", req.Tool.Name, tt.want)
			}
		})
	}
}

func TestRootCommandTrace(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.yaml")
	requestPath := filepath.Join(dir, "request.json")
	if err := os.WriteFile(policyPath, []byte(`apiVersion: omniswitch.dev/v1
kind: Policy
metadata:
  name: production-delete
spec:
  match:
    tool: github
    action: delete
    environment: production
  effect: deny
  reason: Repository {{resource.name}} is protected.
`), 0o600); err != nil {
		t.Fatalf("WriteFile(policy) error = %v", err)
	}
	if err := os.WriteFile(requestPath, []byte(`{
		"tool":{"name":"github"},
		"action":{"name":"delete"},
		"resource":{"name":"payments-prod","environment":"production"}
	}`), 0o600); err != nil {
		t.Fatalf("WriteFile(request) error = %v", err)
	}

	var output strings.Builder
	cmd := NewRootCommand("omniswitch")
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"trace", policyPath, requestPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := output.String()
	for _, want := range []string{"apiVersion: omniswitch.dev/v1", "kind: DecisionTrace", "decisionId:", "production-delete"} {
		if !strings.Contains(got, want) {
			t.Fatalf("trace output = %q, want substring %q", got, want)
		}
	}
}
