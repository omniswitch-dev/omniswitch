package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/internal/model"
)

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name        string
		req         model.ToolRequest
		wantAllowed bool
		wantRule    string
		wantReason  string
	}{
		{
			name: "denies matching violation",
			req: model.ToolRequest{
				Tool:     model.Tool{Name: "github"},
				Action:   model.Action{Name: "delete"},
				Resource: model.Resource{Name: "payments-prod", Environment: "production"},
			},
			wantAllowed: false,
			wantRule:    "production-delete",
			wantReason:  "Repository payments-prod is protected.",
		},
		{
			name: "allows nonmatching request",
			req: model.ToolRequest{
				Tool:     model.Tool{Name: "github"},
				Action:   model.Action{Name: "delete"},
				Resource: model.Resource{Name: "payments-staging", Environment: "staging"},
			},
			wantAllowed: true,
			wantRule:    "none",
			wantReason:  "No policy violation detected.",
		},
	}

	engine, err := NewEngine(Rule{
		Name:       "production-delete",
		Expression: `resource.environment == "production" && action.name == "delete"`,
		Reason:     "Repository {{resource.name}} is protected.",
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, err := engine.Evaluate(context.Background(), tt.req)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if decision.Allowed != tt.wantAllowed {
				t.Fatalf("Allowed = %v, want %v", decision.Allowed, tt.wantAllowed)
			}
			if decision.Rule != tt.wantRule {
				t.Fatalf("Rule = %q, want %q", decision.Rule, tt.wantRule)
			}
			if decision.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %q", decision.Reason, tt.wantReason)
			}
			if decision.EvaluationTime <= 0 {
				t.Fatalf("EvaluationTime = %s, want positive duration", decision.EvaluationTime)
			}
		})
	}
}

func TestRuleFromFile(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantName   string
		wantReason string
		wantExpr   string
	}{
		{
			name: "metadata comments",
			content: `// name: production-delete
// reason: Repository {{resource.name}} is protected.
resource.environment == "production"`,
			wantName:   "production-delete",
			wantReason: "Repository {{resource.name}} is protected.",
			wantExpr:   `resource.environment == "production"`,
		},
		{
			name:       "fallback name",
			content:    `tool.name == "github"`,
			wantName:   "policy",
			wantReason: "",
			wantExpr:   `tool.name == "github"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "policy.cel")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			rule, err := RuleFromFile(path)
			if err != nil {
				t.Fatalf("RuleFromFile() error = %v", err)
			}
			if rule.Name != tt.wantName {
				t.Fatalf("Name = %q, want %q", rule.Name, tt.wantName)
			}
			if rule.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %q", rule.Reason, tt.wantReason)
			}
			if strings.TrimSpace(rule.Expression) != tt.wantExpr {
				t.Fatalf("Expression = %q, want %q", rule.Expression, tt.wantExpr)
			}
		})
	}
}
