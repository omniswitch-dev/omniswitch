package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/audit"
	"github.com/omniswitch-dev/omniswitch/internal/policy"
)

func TestHandlerServeHTTP(t *testing.T) {
	tests := []struct {
		name              string
		body              string
		wantStatus        int
		wantBodySubstring string
		wantForwarded     bool
	}{
		{
			name: "deny path",
			body: `{
				"jsonrpc":"2.0",
				"id":1,
				"method":"tools/call",
				"params":{"name":"github.delete_repo","arguments":{"repo":"payments-prod"}}
			}`,
			wantStatus:        http.StatusOK,
			wantBodySubstring: "DENIED",
			wantForwarded:     false,
		},
		{
			name: "allow path",
			body: `{
				"jsonrpc":"2.0",
				"id":2,
				"method":"tools/call",
				"params":{"name":"github.delete_repo","arguments":{"repo":"payments-staging"}}
			}`,
			wantStatus:        http.StatusOK,
			wantBodySubstring: "upstream ok",
			wantForwarded:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			forwarded := false
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded = true
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"upstream ok"}]}}`))
			}))
			defer upstream.Close()

			engine, err := policy.NewEngine(policy.Rule{
				Name:       "production-delete",
				Expression: `resource.environment == "production" && action.name == "delete"`,
				Reason:     "Repository {{resource.name}} is protected.",
			})
			if err != nil {
				t.Fatalf("NewEngine() error = %v", err)
			}

			var auditBuf bytes.Buffer
			handler, err := NewHandler(engine, audit.NewStdoutLogger(&auditBuf), upstream.URL)
			if err != nil {
				t.Fatalf("NewHandler() error = %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBodySubstring) {
				t.Fatalf("body = %q, want substring %q", rec.Body.String(), tt.wantBodySubstring)
			}
			if forwarded != tt.wantForwarded {
				t.Fatalf("forwarded = %v, want %v", forwarded, tt.wantForwarded)
			}
			if !strings.Contains(auditBuf.String(), `"rule":"production-delete"`) && !strings.Contains(auditBuf.String(), `"rule":"none"`) {
				t.Fatalf("audit output = %q, want a rule field", auditBuf.String())
			}
		})
	}
}

func TestMultiHandlerFederatesToolLists(t *testing.T) {
	newUpstream := func(tool string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"` + tool + `"}]}}`))
		}))
	}
	first := newUpstream("search")
	defer first.Close()
	second := newUpstream("deploy")
	defer second.Close()
	engine, err := policy.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	handler, err := NewMultiHandler(engine, nil, []TargetConfig{
		{Name: "docs", Upstream: first.URL},
		{Name: "ops", Upstream: second.URL},
	})
	if err != nil {
		t.Fatalf("NewMultiHandler() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "docs__search") || !strings.Contains(rec.Body.String(), "ops__deploy") {
		t.Fatalf("status/body = %d/%s, want both prefixed tools", rec.Code, rec.Body.String())
	}
}
