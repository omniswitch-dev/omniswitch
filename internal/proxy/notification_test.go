package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestNotificationsGetNoJSONRPCResponse covers MCP streamable HTTP rules: an
// accepted notification gets HTTP 202 with no body, a rejected or denied one
// an HTTP 4xx status whose body (if any) is a JSON-RPC error without an id.
func TestNotificationsGetNoJSONRPCResponse(t *testing.T) {
	upstream := &recordingUpstream{}
	auditor := &memoryAuditor{}
	h, err := NewMultiHandler(loadEngine(t, "../../policies/production-delete.yaml"), auditor, []TargetConfig{
		{Name: "github", Upstream: upstream.server(t).URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantEmpty  bool
		wantCode   int
		wantAudit  string
	}{
		{name: "denied tools/call", body: `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"github__delete_repo","arguments":{"repo":"prod-api"}}}`, wantStatus: http.StatusForbidden, wantCode: -32001, wantAudit: "tools/call"},
		{name: "tools/list", body: `{"jsonrpc":"2.0","method":"tools/list"}`, wantStatus: http.StatusAccepted, wantEmpty: true, wantAudit: "tools/list"},
		{name: "invalid params", body: `{"jsonrpc":"2.0","method":"tools/call","params":{"name":1}}`, wantStatus: http.StatusBadRequest, wantCode: -32602},
		{name: "invalid request", body: `{"jsonrpc":"1.0","method":"notifications/initialized"}`, wantStatus: http.StatusBadRequest, wantCode: -32600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(auditor.events)
			rec := serve(h, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantEmpty {
				if rec.Body.Len() != 0 {
					t.Fatalf("body = %q, want empty", rec.Body.String())
				}
			} else {
				var body map[string]json.RawMessage
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode %q: %v", rec.Body.String(), err)
				}
				if _, hasID := body["id"]; hasID {
					t.Fatalf("notification error carries an id: %s", rec.Body.String())
				}
				var rpcErr struct {
					Code int `json:"code"`
				}
				if err := json.Unmarshal(body["error"], &rpcErr); err != nil || rpcErr.Code != tc.wantCode {
					t.Fatalf("error = %s, want code %d", body["error"], tc.wantCode)
				}
			}
			if tc.wantAudit != "" {
				if len(auditor.events) != before+1 || auditor.events[before].Method != tc.wantAudit {
					t.Fatalf("audit events = %+v, want one %s", auditor.events[before:], tc.wantAudit)
				}
			}
		})
	}
	if upstream.count() != 0 {
		t.Fatalf("upstream called %d times: %v", upstream.count(), upstream.bodies)
	}

	// Requests with an id keep their JSON-RPC responses.
	rec := serve(h, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"github__delete_repo","arguments":{"repo":"prod-api"}}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":7`) || !strings.Contains(rec.Body.String(), `"isError":true`) {
		t.Fatalf("request response = %d %s", rec.Code, rec.Body.String())
	}
}
