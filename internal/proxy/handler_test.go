package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestHandlerStreamsSSEFromUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("Accept = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Mcp-Session-Id", "session-1")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\"}\n\n"))
	}))
	defer upstream.Close()
	engine, err := policy.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	handler, err := NewHandler(engine, nil, upstream.URL)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") || recorder.Header().Get("Mcp-Session-Id") != "session-1" || !strings.Contains(recorder.Body.String(), "event: message") {
		t.Fatalf("status/headers/body = %d/%v/%q, want SSE response", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestHandlerForwardsOIDCBearerOnlyWhenConfigured(t *testing.T) {
	for _, test := range []struct {
		name       string
		authMethod string
		wantBearer string
	}{
		{name: "OIDC token is delegated", authMethod: "oidc", wantBearer: "Bearer oidc-token"},
		{name: "API key is never delegated", authMethod: "api_key", wantBearer: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != test.wantBearer {
					t.Fatalf("Authorization = %q, want %q", got, test.wantBearer)
				}
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			}))
			defer upstream.Close()
			engine, err := policy.NewEngine()
			if err != nil {
				t.Fatalf("NewEngine() error = %v", err)
			}
			handler, err := NewMultiHandler(engine, nil, []TargetConfig{{Name: "default", Upstream: upstream.URL, ForwardBearerToken: true}})
			if err != nil {
				t.Fatalf("NewMultiHandler() error = %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
			req.Header.Set("Authorization", "Bearer oidc-token")
			req.Header.Set("x-omniswitch-auth-method", test.authMethod)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
		})
	}
}

func TestHandlerForwardsToStdioTarget(t *testing.T) {
	engine, err := policy.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	handler, err := NewMultiHandler(engine, nil, []TargetConfig{{
		Name: "local", Transport: "stdio", Command: os.Args[0],
		Args:        []string{"-test.run=TestStdioMCPHelperProcess"},
		Environment: map[string]string{"OMNISWITCH_TEST_STDIO_HELPER": "1"},
	}})
	if err != nil {
		t.Fatalf("NewMultiHandler() error = %v", err)
	}
	t.Cleanup(func() { handler.targets["local"].stdio.stop() })
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"initialize","params":{}}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"source":"stdio"`) {
		t.Fatalf("status/body = %d/%q, want stdio response", recorder.Code, recorder.Body.String())
	}
}

func TestStdioMCPHelperProcess(t *testing.T) {
	if os.Getenv("OMNISWITCH_TEST_STDIO_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"source":"stdio"}}`+"\n", request["id"])
	}
	os.Exit(0)
}
