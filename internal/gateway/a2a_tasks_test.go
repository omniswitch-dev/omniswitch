package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

	// CancelTask on a completed task is TaskNotCancelableError.
	rec = a2aCall(t, h, `{"jsonrpc":"2.0","id":5,"method":"CancelTask","params":{"id":"`+sent.Result.ID+`"}}`)
	if !strings.Contains(rec.Body.String(), "-32002") {
		t.Fatalf("cancel of completed task = %s, want -32002", rec.Body.String())
	}

	// CancelTask transitions a non-terminal task to canceled.
	canceled := false
	pending := newA2ATask("ctx_c", "", "", func() { canceled = true })
	a2aTasks.put(pending)
	rec = a2aCall(t, h, `{"jsonrpc":"2.0","id":4,"method":"CancelTask","params":{"id":"`+pending.id+`"}}`)
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
	if cancel.Result.Status.State != "canceled" || !canceled {
		t.Fatalf("cancel state = %q (cancel called %v), want canceled", cancel.Result.Status.State, canceled)
	}
}

func TestA2AStreamEmitsSSEEvents(t *testing.T) {
	h := newA2ATestHandler(t)
	rec := a2aCall(t, h, `{"jsonrpc":"2.0","id":1,"method":"message/stream","params":{"message":{"parts":[{"text":"hello"}]},"metadata":{"model":"test-model"}}}`)
	body := rec.Body.String()
	if !strings.Contains(body, `"kind":"status-update"`) || !strings.Contains(body, `"working"`) {
		t.Fatalf("missing working event: %s", body)
	}
	if !strings.Contains(body, "artifact-update") || !strings.Contains(body, "hello from a2a") {
		t.Fatalf("missing artifact content: %s", body)
	}
	if !strings.Contains(body, `"completed"`) || !strings.Contains(body, `"final":true`) {
		t.Fatalf("missing final completion event: %s", body)
	}
	// Every SSE frame is a JSON-RPC response carrying the request id.
	for _, frame := range strings.Split(strings.TrimSpace(body), "\n\n") {
		data, ok := strings.CutPrefix(frame, "data: ")
		if !ok {
			t.Fatalf("frame %q is not a bare data frame", frame)
		}
		var message struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  struct {
				Kind string `json:"kind"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(data), &message); err != nil || message.JSONRPC != "2.0" || string(message.ID) != "1" || message.Result.Kind == "" {
			t.Fatalf("frame %q is not a JSON-RPC result: %v", data, err)
		}
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

type blockingA2AProvider struct {
	started  chan struct{}
	canceled chan struct{}
}

func (blockingA2AProvider) Name() string { return "blocking" }

func (blockingA2AProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "blocking-model", Object: "model", OwnedBy: "blocking", Provider: "blocking"}}
}

func (p blockingA2AProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	close(p.started)
	select {
	case <-ctx.Done():
		close(p.canceled)
		return provider.ChatResponse{}, provider.ProviderMeta{}, ctx.Err()
	case <-time.After(10 * time.Second):
		return provider.ChatResponse{ID: "late", Model: req.Model, Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "late"}, FinishReason: "stop"}}},
			provider.ProviderMeta{Provider: "blocking", Model: req.Model, Timestamp: time.Now()}, nil
	}
}

func a2aCallAs(t *testing.T, h *Handler, keyID, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(payload))
	req.Header.Set("x-omniswitch-key-id", keyID)
	req.Header.Set("x-omniswitch-workspace-id", "ws_1")
	rec := httptest.NewRecorder()
	h.A2A(rec, req)
	return rec
}

func findWorkingTask(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a2aTasks.mu.Lock()
		for id, task := range a2aTasks.tasks {
			if task.ownerKeyID == "key_owner" && !task.terminal() {
				a2aTasks.mu.Unlock()
				return id
			}
		}
		a2aTasks.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no working task appeared")
	return ""
}

func TestA2ACancelStopsInferenceAndSticks(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	blocking := blockingA2AProvider{started: make(chan struct{}), canceled: make(chan struct{})}
	registry.Register(blocking)
	h := New(registry, router.New(registry), st, guardrail.NewEngine())

	done := make(chan string, 1)
	go func() {
		rec := a2aCallAs(t, h, "key_owner", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"parts":[{"text":"hi"}]},"metadata":{"model":"blocking-model"}}}`)
		done <- rec.Body.String()
	}()
	<-blocking.started
	id := findWorkingTask(t)

	// Concurrent readers must not race with the writer.
	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				a2aCallAs(t, h, "key_owner", `{"jsonrpc":"2.0","id":3,"method":"GetTask","params":{"id":"`+id+`"}}`)
			}
		}
	}()

	// Another key cannot see or cancel the task.
	if rec := a2aCallAs(t, h, "key_other", `{"jsonrpc":"2.0","id":2,"method":"CancelTask","params":{"id":"`+id+`"}}`); !strings.Contains(rec.Body.String(), "-32001") {
		t.Fatalf("foreign cancel = %s, want task not found", rec.Body.String())
	}
	if rec := a2aCallAs(t, h, "key_other", `{"jsonrpc":"2.0","id":2,"method":"GetTask","params":{"id":"`+id+`"}}`); !strings.Contains(rec.Body.String(), "-32001") {
		t.Fatalf("foreign get = %s, want task not found", rec.Body.String())
	}

	rec := a2aCallAs(t, h, "key_owner", `{"jsonrpc":"2.0","id":2,"method":"CancelTask","params":{"id":"`+id+`"}}`)
	if !strings.Contains(rec.Body.String(), `"canceled"`) {
		t.Fatalf("cancel = %s", rec.Body.String())
	}
	select {
	case <-blocking.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("inference context was not canceled")
	}
	sendBody := <-done
	close(stop)
	readers.Wait()
	if !strings.Contains(sendBody, `"canceled"`) {
		t.Fatalf("SendMessage response = %s, want canceled task", sendBody)
	}
	task, ok := a2aTasks.get(id, "key_owner", "ws_1")
	if !ok || task.snapshot().Status.State != a2aStateCanceled {
		t.Fatalf("final task state is not canceled")
	}
}

func TestA2AMalformedJSONIsParseError(t *testing.T) {
	h := newA2ATestHandler(t)
	if rec := a2aCall(t, h, `{"jsonrpc":`); !strings.Contains(rec.Body.String(), "-32700") {
		t.Fatalf("malformed JSON = %s, want -32700", rec.Body.String())
	}
	if rec := a2aCall(t, h, `{"jsonrpc":"1.0","id":1,"method":"GetTask"}`); !strings.Contains(rec.Body.String(), "-32600") {
		t.Fatalf("bad version = %s, want -32600", rec.Body.String())
	}
}

func TestA2ATaskStoreIsBounded(t *testing.T) {
	store := newA2ATaskStore(3, time.Hour)
	var tasks []*a2aTask
	for i := 0; i < 5; i++ {
		task := newA2ATask("", "k", "w", nil)
		task.createdAt = time.Now().Add(time.Duration(i) * time.Millisecond)
		if i == 1 {
			task.complete(map[string]any{}, a2aArtifact{})
		}
		tasks = append(tasks, task)
		store.put(task)
	}
	if store.len() != 3 {
		t.Fatalf("store size = %d, want 3", store.len())
	}
	// The terminal task is evicted before older working tasks.
	if _, ok := store.get(tasks[1].id, "k", "w"); ok {
		t.Fatalf("terminal task was kept over working tasks")
	}
	if _, ok := store.get(tasks[4].id, "k", "w"); !ok {
		t.Fatalf("newest task was evicted")
	}

	expired := newA2ATaskStore(10, time.Minute)
	old := newA2ATask("", "k", "w", nil)
	old.createdAt = time.Now().Add(-2 * time.Minute)
	expired.put(old)
	if _, ok := expired.get(old.id, "k", "w"); ok {
		t.Fatalf("expired task is still returned")
	}
}

// TestA2AFollowsReloadedBodyLimit: /a2a read the body limit from the first
// SetMaxRequestBytes call, so a hot-reloaded max_request_bytes did not
// apply to it.
func TestA2AFollowsReloadedBodyLimit(t *testing.T) {
	h := newA2ATestHandler(t)
	payload := `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"parts":[{"text":"` +
		strings.Repeat("a", 2048) + `"}]},"metadata":{"model":"test-model"}}}`

	h.SetMaxRequestBytes(1 << 20)
	if rec := a2aCall(t, h, payload); strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("payload under the first limit failed: %s", rec.Body.String())
	}

	h.SetMaxRequestBytes(1024)
	rec := a2aCall(t, h, payload)
	if !strings.Contains(rec.Body.String(), `"code":-32700`) {
		t.Fatalf("payload over the reloaded limit = %d %s, want a parse error", rec.Code, rec.Body.String())
	}
}
