package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/policy"
)

// TestStdioMCPStrictHelperProcess refuses every method except initialize
// until the bridge performs the protocol handshake.
func TestStdioMCPStrictHelperProcess(t *testing.T) {
	if os.Getenv("OMNISWITCH_TEST_STDIO_STRICT_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	initialized := false
	for scanner.Scan() {
		var request map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		method := ""
		_ = json.Unmarshal(request["method"], &method)
		id, hasID := request["id"]
		if method == "initialize" {
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"strict"}}}`+"\n", id)
			initialized = true
			continue
		}
		if !initialized {
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32002,"message":"not initialized"}}`+"\n", id)
			os.Exit(3)
		}
		if hasID {
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"source":"strict-stdio"}}`+"\n", id)
		}
	}
	os.Exit(0)
}

func TestStdioBridgePerformsInitializeHandshake(t *testing.T) {
	engine, err := policy.NewEngine()
	if err != nil {
		t.Fatalf("policy engine: %v", err)
	}
	handler, err := NewMultiHandler(engine, nil, []TargetConfig{{
		Name:      "strict",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestStdioMCPStrictHelperProcess"},
		Environment: map[string]string{
			"OMNISWITCH_TEST_STDIO_STRICT_HELPER": "1",
			"OMNISWITCH_TEST_STDIO_HELPER":        "",
		},
	}})
	if err != nil {
		t.Fatalf("NewMultiHandler() error = %v", err)
	}
	t.Cleanup(func() { handler.targets["strict"].stdio.stop() })

	// The client calls a tool directly with no prior initialize: the bridge
	// must handshake behind the scenes for the strict server to respond.
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"ping","arguments":{}}}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"source":"strict-stdio"`) {
		t.Fatalf("status/body = %d/%q, want post-handshake response", recorder.Code, recorder.Body.String())
	}
}
