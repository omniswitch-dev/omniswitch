package policy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/adapter/mcp"
	"github.com/omniswitch-dev/omniswitch/internal/model"
)

func evalDeny(t *testing.T, expression string, req model.ToolRequest) (bool, error) {
	t.Helper()
	engine, err := NewEngine(Rule{Name: "rule", Expression: expression, Reason: "denied"})
	if err != nil {
		t.Fatalf("NewEngine(%q) error = %v", expression, err)
	}
	decision, err := engine.Evaluate(context.Background(), req)
	return !decision.Allowed, err
}

// TestArgsCaseVariantsCannotBypassRule is the review repro (finding M1): a
// rule on args.table must also deny "Table" and "TABLE", because Go MCP
// servers decoding into structs read those as the same field.
func TestArgsCaseVariantsCannotBypassRule(t *testing.T) {
	const rule = `has(args.table) && args.table == "users"`
	for _, name := range []string{"table", "Table", "TABLE", "tAbLe"} {
		t.Run(name, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"db.query","arguments":{"` + name + `":"users"}}}`
			parsed, err := mcp.Parse([]byte(body))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			req := mcp.BuildToolRequest(parsed.Params.Name, parsed.Params.Arguments, mcp.ToolContext{})
			if req.Resource.Name != "users" {
				t.Fatalf("resource.name = %q, want users", req.Resource.Name)
			}
			denied, err := evalDeny(t, rule, req)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if !denied {
				t.Fatalf("arguments {%q:\"users\"} were allowed, want deny", name)
			}
		})
	}
}

func TestArgsFoldedLookupSemantics(t *testing.T) {
	req := model.ToolRequest{Args: map[string]any{
		"Table": "users",
		"options": map[string]any{
			"Mode": "drop",
			"Inner": map[string]any{
				"FORCE": true,
			},
		},
		"Items": []any{map[string]any{"Name": "a"}},
		"count": json.Number("12345678901234567"),
		"ratio": json.Number("0.5"),
		"big":   json.Number("1e400"),
		"none":  nil,
	}}
	tests := []struct {
		name       string
		expression string
		wantDeny   bool
		wantErr    bool
	}{
		{name: "field select", expression: `args.table == "users"`, wantDeny: true},
		{name: "index", expression: `args["TABLE"] == "users"`, wantDeny: true},
		{name: "has", expression: `has(args.table)`, wantDeny: true},
		{name: "in", expression: `"table" in args`, wantDeny: true},
		{name: "nested object", expression: `args.Options.mode == "drop"`, wantDeny: true},
		{name: "nested has", expression: `has(args.options.mode)`, wantDeny: true},
		{name: "deeply nested", expression: `args.OPTIONS.inner.force == true`, wantDeny: true},
		{name: "object in list", expression: `args.items[0].name == "a"`, wantDeny: true},
		{name: "exists over list", expression: `args.items.exists(i, i.NAME == "a")`, wantDeny: true},
		{name: "has missing key", expression: `has(args.missing)`, wantDeny: false},
		{name: "has missing nested key", expression: `has(args.options.missing)`, wantDeny: false},
		{name: "in missing key", expression: `"missing" in args`, wantDeny: false},
		{name: "missing key fails closed", expression: `args.missing == "x"`, wantErr: true},
		{name: "size unchanged", expression: `size(args) == 7`, wantDeny: true},
		{name: "nested size unchanged", expression: `size(args.options) == 2`, wantDeny: true},
		{name: "iteration sees original keys", expression: `args.exists(k, k == "Table") && !args.exists(k, k == "table")`, wantDeny: true},
		{name: "all keys original", expression: `args.all(k, k in ["Table", "options", "Items", "count", "ratio", "big", "none"])`, wantDeny: true},
		{name: "exact integer", expression: `args.COUNT == 12345678901234567`, wantDeny: true},
		{name: "integer type", expression: `type(args.count) == int`, wantDeny: true},
		{name: "float", expression: `args.Ratio == 0.5`, wantDeny: true},
		{name: "float type", expression: `type(args.ratio) == double`, wantDeny: true},
		{name: "unrepresentable number stays string", expression: `type(args.big) == string`, wantDeny: true},
		{name: "null", expression: `args.NONE == null`, wantDeny: true},
		{name: "equality args left", expression: `args.options.inner == {"force": true}`, wantDeny: true},
		{name: "equality args right", expression: `{"force": true} == args.options.inner`, wantDeny: true},
		{name: "equality size differs", expression: `args.options == {"mode": "drop"}`, wantDeny: false},
		{name: "equality value differs", expression: `args.options.inner == {"force": false}`, wantDeny: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			denied, err := evalDeny(t, tt.expression, req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Evaluate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !denied {
					t.Fatalf("evaluation error must deny")
				}
				return
			}
			if denied != tt.wantDeny {
				t.Fatalf("deny = %v, want %v", denied, tt.wantDeny)
			}
		})
	}
}

// TestArgsExactKeyWins covers maps passed directly to the engine (mcp.Parse
// rejects names equal under case folding): an exact match is preferred, and
// a folded-only match picks the lexicographically smallest original key.
func TestArgsExactKeyWins(t *testing.T) {
	req := model.ToolRequest{Args: map[string]any{
		"table": "exact-lower",
		"Table": "exact-title",
		"MODE":  "upper",
		"Mode":  "title",
	}}
	tests := []struct {
		expression string
		wantDeny   bool
	}{
		{`args.table == "exact-lower"`, true},
		{`args.Table == "exact-title"`, true},
		{`args["TABLE"] == "exact-title"`, true}, // "Table" < "table"
		{`args.mode == "upper"`, true},           // "MODE" < "Mode"
		{`args.Mode == "title"`, true},
		{`size(args) == 4`, true},
	}
	for _, tt := range tests {
		t.Run(tt.expression, func(t *testing.T) {
			denied, err := evalDeny(t, tt.expression, req)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if denied != tt.wantDeny {
				t.Fatalf("deny = %v, want %v", denied, tt.wantDeny)
			}
		})
	}
}

func TestArgsNilAndEmpty(t *testing.T) {
	for _, args := range []map[string]any{nil, {}} {
		denied, err := evalDeny(t, `size(args) == 0 && !has(args.table)`, model.ToolRequest{Args: args})
		if err != nil {
			t.Fatalf("Evaluate() error = %v", err)
		}
		if !denied {
			t.Fatalf("args %v: want empty map", args)
		}
	}
}
