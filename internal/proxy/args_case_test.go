package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/audit"
	"github.com/omniswitch-dev/omniswitch/internal/policy"
)

// TestHandlerArgsRuleIgnoresArgumentNameCase is the end-to-end regression
// for finding M1: a deny rule on args.<name> must block a tools/call that
// spells the argument name with different case (including nested names),
// because Go MCP servers decode such names into the same struct field.
func TestHandlerArgsRuleIgnoresArgumentNameCase(t *testing.T) {
	tests := []struct {
		name          string
		arguments     string
		wantForwarded bool
	}{
		{name: "exact name", arguments: `{"table":"users"}`},
		{name: "title case", arguments: `{"Table":"users"}`},
		{name: "upper case", arguments: `{"TABLE":"users"}`},
		{name: "nested title case", arguments: `{"table":"orders","Options":{"Mode":"drop"}}`},
		{name: "other table allowed", arguments: `{"Table":"orders"}`, wantForwarded: true},
	}

	engine, err := policy.NewEngine(
		policy.Rule{
			Name:       "protect-users",
			Expression: `has(args.table) && args.table == "users"`,
			Reason:     "users table is protected",
		},
		policy.Rule{
			Name:       "no-drop-mode",
			Expression: `has(args.options) && has(args.options.mode) && args.options.mode == "drop"`,
			Reason:     "drop mode is not allowed",
		},
	)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var forwarded atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded.Store(true)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"upstream ok"}]}}`))
			}))
			defer upstream.Close()

			var auditBuf bytes.Buffer
			handler, err := NewHandler(engine, audit.NewStdoutLogger(&auditBuf), upstream.URL)
			if err != nil {
				t.Fatalf("NewHandler() error = %v", err)
			}

			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"db.query","arguments":` + tt.arguments + `}}`
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			if forwarded.Load() != tt.wantForwarded {
				t.Fatalf("forwarded = %v, want %v; body = %s", forwarded.Load(), tt.wantForwarded, rec.Body.String())
			}
			if tt.wantForwarded {
				if !strings.Contains(rec.Body.String(), "upstream ok") {
					t.Fatalf("body = %q, want upstream response", rec.Body.String())
				}
				return
			}
			if !strings.Contains(rec.Body.String(), "DENIED") {
				t.Fatalf("body = %q, want DENIED", rec.Body.String())
			}
		})
	}
}
