package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/model"
)

func TestToToolRequest(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		wantTool string
		wantAct  string
		wantRes  string
		wantEnv  string
	}{
		{
			name: "github delete repo",
			payload: `{
				"jsonrpc":"2.0",
				"id":1,
				"method":"tools/call",
				"params":{
					"name":"github.delete_repo",
					"arguments":{"repo":"payments-prod","agent_id":"agent-1"}
				}
			}`,
			wantTool: "github",
			wantAct:  "delete",
			wantRes:  "payments-prod",
			wantEnv:  "production",
		},
		{
			name: "postgres drop table",
			payload: `{
				"jsonrpc":"2.0",
				"id":2,
				"method":"tools/call",
				"params":{
					"name":"postgres.drop_table",
					"arguments":{"table":"ledger_staging"}
				}
			}`,
			wantTool: "postgres",
			wantAct:  "drop_table",
			wantRes:  "ledger_staging",
			wantEnv:  "staging",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := Decode(strings.NewReader(tt.payload))
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			toolReq, err := req.ToToolRequest()
			if err != nil {
				t.Fatalf("ToToolRequest() error = %v", err)
			}
			if toolReq.Tool.Name != tt.wantTool {
				t.Fatalf("Tool.Name = %q, want %q", toolReq.Tool.Name, tt.wantTool)
			}
			if toolReq.Action.Name != tt.wantAct {
				t.Fatalf("Action.Name = %q, want %q", toolReq.Action.Name, tt.wantAct)
			}
			if toolReq.Resource.Name != tt.wantRes {
				t.Fatalf("Resource.Name = %q, want %q", toolReq.Resource.Name, tt.wantRes)
			}
			if toolReq.Resource.Environment != tt.wantEnv {
				t.Fatalf("Resource.Environment = %q, want %q", toolReq.Resource.Environment, tt.wantEnv)
			}
		})
	}
}

func TestDeniedResponse(t *testing.T) {
	tests := []struct {
		name string
		id   json.RawMessage
	}{
		{name: "with id", id: json.RawMessage("1")},
		{name: "without id", id: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := DeniedResponse(tt.id, testDecision())
			if resp.JSONRPC != "2.0" {
				t.Fatalf("JSONRPC = %q, want 2.0", resp.JSONRPC)
			}
			result, ok := resp.Result.(ToolResult)
			if !ok {
				t.Fatalf("Result type = %T, want ToolResult", resp.Result)
			}
			if !result.IsError {
				t.Fatalf("IsError = false, want true")
			}
			if !strings.Contains(result.Content[0].Text, "DENIED") {
				t.Fatalf("denial text = %q, want DENIED", result.Content[0].Text)
			}
		})
	}
}

func testDecision() model.Decision {
	return model.Decision{Allowed: false, Rule: "production-delete", Reason: "Repository payments-prod is protected."}
}
