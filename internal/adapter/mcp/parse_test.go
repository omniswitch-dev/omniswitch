package mcp

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/model"
)

func TestParseErrorCodes(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode int
		wantID   string
	}{
		{name: "invalid json", body: `{"jsonrpc":`, wantCode: CodeParseError},
		{name: "batch", body: `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, wantCode: CodeInvalidRequest},
		{name: "scalar", body: `42`, wantCode: CodeInvalidRequest},
		{name: "missing jsonrpc", body: `{"id":1,"method":"ping"}`, wantCode: CodeInvalidRequest, wantID: "1"},
		{name: "method not string", body: `{"jsonrpc":"2.0","id":2,"method":5}`, wantCode: CodeInvalidRequest, wantID: "2"},
		{name: "object id", body: `{"jsonrpc":"2.0","id":{},"method":"ping"}`, wantCode: CodeInvalidRequest},
		{name: "method case variant", body: `{"jsonrpc":"2.0","id":3,"method":"tools/call","Method":"ping","params":{"name":"x"}}`, wantCode: CodeInvalidRequest, wantID: "3"},
		{name: "params case variant", body: `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"x"},"Params":{"name":"y"}}`, wantCode: CodeInvalidRequest, wantID: "4"},
		{name: "arguments case variant", body: `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"x","arguments":{"repo":"a-prod"},"Arguments":{"repo":"a-staging"}}}`, wantCode: CodeInvalidRequest, wantID: "5"},
		{name: "name case variant", body: `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"x","Name":"y"}}`, wantCode: CodeInvalidRequest, wantID: "6"},
		{name: "nested argument case variant", body: `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"x","arguments":{"repo":"a-prod","Repo":"a-dev"}}}`, wantCode: CodeInvalidRequest, wantID: "7"},
		{name: "exact duplicate", body: `{"jsonrpc":"2.0","id":8,"method":"tools/call","method":"ping"}`, wantCode: CodeInvalidRequest, wantID: "8"},
		{name: "escaped case variant", body: `{"jsonrpc":"2.0","id":9,"method":"tools/call","\u004dethod":"ping","params":{"name":"x"}}`, wantCode: CodeInvalidRequest, wantID: "9"},
		{name: "kelvin fold", body: `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"x","arguments":{"k":"1","\u212a":"2"}}}`, wantCode: CodeInvalidRequest, wantID: "10"},
		{name: "params array", body: `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":["x"]}`, wantCode: CodeInvalidParams, wantID: "11"},
		{name: "arguments not object", body: `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"x","arguments":[1]}}`, wantCode: CodeInvalidParams, wantID: "12"},
		{name: "name not string", body: `{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":1}}`, wantCode: CodeInvalidParams, wantID: "13"},
		{name: "missing name", body: `{"jsonrpc":"2.0","id":"a","method":"tools/call","params":{}}`, wantCode: CodeInvalidParams, wantID: `"a"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.body))
			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("Parse() error = %v, want *ParseError", err)
			}
			if parseErr.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d (%v)", parseErr.Code, tt.wantCode, err)
			}
			if string(parseErr.ID) != tt.wantID {
				t.Fatalf("id = %q, want %q", parseErr.ID, tt.wantID)
			}
		})
	}
}

func TestParseAcceptsValidMessages(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantID    bool
		wantParam bool
	}{
		{name: "notification", body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
		{name: "null id", body: `{"jsonrpc":"2.0","id":null,"method":"ping"}`, wantID: true},
		{name: "client response", body: `{"jsonrpc":"2.0","id":"srv-1","result":{}}`, wantID: true},
		{name: "array params for other methods", body: `{"jsonrpc":"2.0","id":1,"method":"custom","params":[1,2]}`, wantID: true},
		{name: "tool call with meta", body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":{"n":12345678901234567},"_meta":{"progressToken":1}}}`, wantID: true, wantParam: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := Parse([]byte(tt.body))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if req.HasID != tt.wantID {
				t.Fatalf("HasID = %v, want %v", req.HasID, tt.wantID)
			}
			if tt.wantParam {
				if _, ok := req.ParamFields["_meta"]; !ok {
					t.Fatalf("ParamFields lost _meta: %v", req.ParamFields)
				}
				if got := req.Params.Arguments["n"]; got != json.Number("12345678901234567") {
					t.Fatalf("argument n = %#v, want exact json.Number", got)
				}
			}
		})
	}
}

func TestBuildToolRequestTrustedAttributesWin(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		args     map[string]any
		tc       ToolContext
		wantAct  string
		wantEnv  string
		wantTool string
	}{
		{name: "env argument cannot downgrade", tool: "github.delete_repo", args: map[string]any{"repo": "acme-prod", "environment": "dev"}, wantAct: "delete", wantEnv: "production", wantTool: "github"},
		{name: "action argument cannot downgrade", tool: "github.delete_repo", args: map[string]any{"repo": "acme-prod", "action": "read"}, wantAct: "delete", wantEnv: "production", wantTool: "github"},
		{name: "argument can escalate action", tool: "github.repo", args: map[string]any{"repo": "acme-prod", "operation": "delete"}, wantAct: "delete", wantEnv: "production", wantTool: "github"},
		{name: "argument can mark production", tool: "run", args: map[string]any{"env": "prod"}, wantAct: "call", wantEnv: "production", wantTool: "run"},
		{name: "bare delete name", tool: "delete_repo", args: map[string]any{"repo": "acme-prod"}, tc: ToolContext{Target: "github"}, wantAct: "delete", wantEnv: "production", wantTool: "delete_repo"},
		{name: "bare drop name", tool: "drop_table", args: map[string]any{"table": "orders"}, wantAct: "delete", wantEnv: "unknown", wantTool: "drop_table"},
		{name: "camel case destroy", tool: "destroyCluster", wantAct: "delete", wantEnv: "unknown", wantTool: "destroyCluster"},
		{name: "not destructive prefix", tool: "deleted_items_list", wantAct: "call", wantEnv: "unknown", wantTool: "deleted_items_list"},
		{name: "http delete", tool: "petById", tc: ToolContext{HTTPMethod: "DELETE"}, wantAct: "delete", wantEnv: "unknown", wantTool: "petById"},
		{name: "http get", tool: "listPets", tc: ToolContext{HTTPMethod: "GET"}, wantAct: "read", wantEnv: "unknown", wantTool: "listPets"},
		{name: "operator metadata wins", tool: "github.delete_repo", args: map[string]any{"repo": "acme-staging"}, tc: ToolContext{TargetMetadata: map[string]string{"environment": "production"}}, wantAct: "delete", wantEnv: "production", wantTool: "github"},
		{name: "target name signals production", tool: "delete_repo", args: map[string]any{"repo": "acme"}, tc: ToolContext{Target: "github-prod"}, wantAct: "delete", wantEnv: "production", wantTool: "delete_repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildToolRequest(tt.tool, tt.args, tt.tc)
			if got.Action.Name != tt.wantAct || got.Resource.Environment != tt.wantEnv || got.Tool.Name != tt.wantTool {
				t.Fatalf("got action=%q env=%q tool=%q, want %q %q %q", got.Action.Name, got.Resource.Environment, got.Tool.Name, tt.wantAct, tt.wantEnv, tt.wantTool)
			}
		})
	}
}

func TestAgentNeverComesFromArguments(t *testing.T) {
	req, err := Parse([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github.list","arguments":{"agent_id":"admin","agent_department":"security","agent_role":"admin","session_id":"s"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := req.ToToolRequestWithIdentity(model.Agent{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != (model.Agent{}) || got.Session.ID != "" {
		t.Fatalf("agent/session taken from arguments: %+v %+v", got.Agent, got.Session)
	}
	got, _ = req.ToToolRequestWithIdentity(model.Agent{ID: "key_1", Department: "finance"}, "sess")
	if got.Agent.ID != "key_1" || got.Agent.Department != "finance" || got.Session.ID != "sess" {
		t.Fatalf("trusted identity not applied: %+v", got)
	}
	if got.Args["agent_id"] != "admin" {
		t.Fatalf("raw args not kept: %v", got.Args)
	}
}
