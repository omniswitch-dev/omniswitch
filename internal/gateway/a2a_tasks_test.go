package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

func newA2ATestHandler(t *testing.T) *Handler {
	t.Helper()
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model", content: "hello from a2a"})
	return New(registry, router.New(registry), st, guardrail.NewEngine())
}

func a2aCall(t *testing.T, h *Handler, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.A2A(rec, req)
	return rec
}

func TestA2ATaskLifecycle(t *testing.T) {
	h := newA2ATestHandler(t)

	// SendMessage returns a completed task with an id.
	rec := a2aCall(t, h, `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"contextId":"ctx_9","parts":[{"text":"hi"}]},"metadata":{"model":"test-model"}}}`)
	var sent struct {
		Result struct {
			ID     string `json:"id"`
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sent); err != nil {
		t.Fatalf("unmarshal send: %v %s", err, rec.Body.String())
	}
	if !strings.HasPrefix(sent.Result.ID, "task_") || sent.Result.Status.State != "completed" {
		t.Fatalf("send result = %+v", sent.Result)
	}

	// GetTask retrieves it.
	rec = a2aCall(t, h, `{"jsonrpc":"2.0","id":2,"method":"GetTask","params":{"id":"`+sent.Result.ID+`"}}`)
	var got struct {
		Result struct {
			ID        string `json:"id"`
			ContextID string `json:"contextId"`
			Status    struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if got.Result.ID != sent.Result.ID || got.Result.ContextID != "ctx_9" || got.Result.Status.State != "completed" {
		t.Fatalf("get result = %+v", got.Result)
	}

	// GetTask on unknown id errors.
	rec = a2aCall(t, h, `{"jsonrpc":"2.0","id":3,"method":"GetTask","params":{"id":"task_missing"}}`)
	if !strings.Contains(rec.Body.String(), "-32001") {
		t.Fatalf("expected task-not-found error, got %s", rec.Body.String())
	}

	// CancelTask transitions a non-terminal task to canceled.
	pending := &a2aTask{ID: newA2ATaskID(), ContextID: "ctx_c", CreatedAt: time.Now().UTC()}
	setState(pending, a2aStateWorking)
	a2aTasks.Store(pending.ID, pending)
	rec = a2aCall(t, h, `{"jsonrpc":"2.0","id":4,"method":"CancelTask","params":{"id":"`+pending.ID+`"}}`)
	var cancel struct {
		Result struct {
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cancel); err != nil {
		t.Fatalf("unmarshal cancel: %v", err)
	}
	if cancel.Result.Status.State != "canceled" {
		t.Fatalf("cancel state = %q, want canceled", cancel.Result.Status.State)
	}
}

func TestA2AStreamEmitsSSEEvents(t *testing.T) {
	h := newA2ATestHandler(t)
	rec := a2aCall(t, h, `{"jsonrpc":"2.0","id":1,"method":"message/stream","params":{"message":{"parts":[{"text":"hello"}]},"metadata":{"model":"test-model"}}}`)
	body := rec.Body.String()
	if !strings.Contains(body, "event: status-update") || !strings.Contains(body, `"working"`) {
		t.Fatalf("missing working event: %s", body)
	}
	if !strings.Contains(body, "artifact-update") || !strings.Contains(body, "hello from a2a") {
		t.Fatalf("missing artifact content: %s", body)
	}
	if !strings.Contains(body, `"completed"`) {
		t.Fatalf("missing final completion event: %s", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	// The streamed task must be retrievable afterwards.
	idStart := strings.Index(body, "task_")
	if idStart < 0 {
		t.Fatalf("no task id in stream")
	}
	taskID := body[idStart : idStart+5+24]
	get := a2aCall(t, h, `{"jsonrpc":"2.0","id":2,"method":"GetTask","params":{"id":"`+taskID+`"}}`)
	if !strings.Contains(get.Body.String(), "completed") {
		t.Fatalf("streamed task not retrievable: %s", get.Body.String())
	}
}
