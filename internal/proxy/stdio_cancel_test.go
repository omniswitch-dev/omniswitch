package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestStdioCancelHelper is a stdio MCP server whose "wait" tool only
// answers once it receives a notifications/cancelled for that request id.
func TestStdioCancelHelper(t *testing.T) {
	if os.Getenv("OMNISWITCH_TEST_CANCEL_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	waiting := map[string]bool{}
	for scanner.Scan() {
		var message map[string]json.RawMessage
		_ = json.Unmarshal(scanner.Bytes(), &message)
		var method string
		_ = json.Unmarshal(message["method"], &method)
		id, hasID := message["id"]
		switch {
		case method == "notifications/cancelled":
			var params struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			_ = json.Unmarshal(message["params"], &params)
			if waiting[string(params.RequestID)] {
				delete(waiting, string(params.RequestID))
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"cancelled":true}}`+"\n", params.RequestID)
			}
		case !hasID:
		case method == "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{}}`+"\n", id)
		default:
			waiting[string(id)] = true
		}
	}
	os.Exit(0)
}

func TestStdioClientCancellationIsMappedToBridgeID(t *testing.T) {
	h, err := NewMultiHandler(defaultEngine(t), nil, []TargetConfig{{
		Name: "s", Transport: "stdio", Command: os.Args[0],
		Args:        []string{"-test.run=^TestStdioCancelHelper$"},
		Environment: map[string]string{"OMNISWITCH_TEST_CANCEL_HELPER": "1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	headers := []string{"x-omniswitch-key-id", "key-1", "Mcp-Session-Id", "sess-1"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"s__wait","arguments":{}}}`)).WithContext(ctx)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		done <- rec
	}()
	client := h.targets["s"].stdio
	deadline := time.Now().Add(5 * time.Second)
	for {
		client.inflightMu.Lock()
		count := len(client.inflight)
		client.inflightMu.Unlock()
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("call never became in flight")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Another caller's cancellation with the same id must not match.
	rec := serve(h, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":42}}`, "x-omniswitch-key-id", "key-2", "Mcp-Session-Id", "sess-1")
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Fatalf("foreign cancel: %d %q", rec.Code, rec.Body.String())
	}
	select {
	case got := <-done:
		t.Fatalf("call ended on a foreign cancellation: %s", got.Body.String())
	case <-time.After(200 * time.Millisecond):
	}

	rec = serve(h, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":42,"reason":"user"}}`, headers...)
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Fatalf("cancel: %d %q", rec.Code, rec.Body.String())
	}
	got := <-done
	if !strings.Contains(got.Body.String(), `"cancelled":true`) || !strings.Contains(got.Body.String(), `"id":42`) {
		t.Fatalf("server did not see the cancellation: %d %s", got.Code, got.Body.String())
	}
	client.inflightMu.Lock()
	defer client.inflightMu.Unlock()
	if len(client.inflight) != 0 {
		t.Fatalf("finished call still registered: %v", client.inflight)
	}
}
