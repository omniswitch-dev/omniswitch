package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/audit"
	"github.com/omniswitch-dev/omniswitch/internal/policy"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type recordingUpstream struct {
	mu     sync.Mutex
	bodies []string
	header []http.Header
}

func (u *recordingUpstream) server(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, string(body))
		u.header = append(u.header, r.Header.Clone())
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"EXECUTED"}]}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func (u *recordingUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}

func loadEngine(t *testing.T, path string) policy.Engine {
	t.Helper()
	engine, err := policy.NewEngineFromFiles(path)
	if err != nil {
		t.Fatalf("NewEngineFromFiles(%s) error = %v", path, err)
	}
	return engine
}

func serve(h http.Handler, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestProductionDeleteDeniesCaseAndArgumentBypasses(t *testing.T) {
	for _, policyPath := range []string{"../../policies/production-delete.yaml", "../../cmd/gateway/embedded/production-delete.yaml"} {
		for name, body := range map[string]string{
			"baseline":             `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github.delete_repo","arguments":{"repo":"acme-prod"}}}`,
			"environment argument": `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github.delete_repo","arguments":{"repo":"acme-prod","environment":"dev"}}}`,
			"action argument":      `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github.delete_repo","arguments":{"repo":"acme-prod","action":"read"}}}`,
		} {
			t.Run(policyPath+"/"+name, func(t *testing.T) {
				upstream := &recordingUpstream{}
				h, err := NewHandler(loadEngine(t, policyPath), nil, upstream.server(t).URL)
				if err != nil {
					t.Fatal(err)
				}
				rec := serve(h, body)
				if upstream.count() != 0 || !strings.Contains(rec.Body.String(), `"isError":true`) {
					t.Fatalf("call was forwarded (%d) or not denied: %s", upstream.count(), rec.Body.String())
				}
			})
		}
	}
}

func TestCaseVariantDuplicateMembersAreRejected(t *testing.T) {
	for name, body := range map[string]string{
		"Method":    `{"jsonrpc":"2.0","id":1,"method":"tools/call","Method":"ping","params":{"name":"github.delete_repo","arguments":{"repo":"acme-prod"}}}`,
		"Arguments": `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github.delete_repo","arguments":{"repo":"acme-prod"},"Arguments":{"repo":"acme-staging"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			upstream := &recordingUpstream{}
			h, err := NewHandler(loadEngine(t, "../../policies/production-delete.yaml"), nil, upstream.server(t).URL)
			if err != nil {
				t.Fatal(err)
			}
			rec := serve(h, body)
			if upstream.count() != 0 {
				t.Fatalf("ambiguous request was forwarded: %v", upstream.bodies)
			}
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"code":-32600`) || !strings.Contains(rec.Body.String(), `"id":1`) {
				t.Fatalf("status/body = %d/%s, want -32600 echoing id", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestFederatedBareDestructiveNameIsDenied(t *testing.T) {
	upstream := &recordingUpstream{}
	server := upstream.server(t)
	h, err := NewMultiHandler(loadEngine(t, "../../cmd/gateway/embedded/production-delete.yaml"), nil, []TargetConfig{
		{Name: "default", Upstream: server.URL},
		{Name: "github", Upstream: server.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github__delete_repo","arguments":{"repo":"acme-prod"}}}`)
	if upstream.count() != 0 || !strings.Contains(rec.Body.String(), `"isError":true`) {
		t.Fatalf("federated delete was forwarded (%d): %s", upstream.count(), rec.Body.String())
	}
	// A non-destructive call on the same target is still forwarded.
	serve(h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"github__get_repo","arguments":{"repo":"acme-prod"}}}`)
	if upstream.count() != 1 {
		t.Fatalf("read call forwarded %d times, want 1", upstream.count())
	}
}

func TestForwardPreservesNumbersAndUnknownFields(t *testing.T) {
	upstream := &recordingUpstream{}
	h, err := NewMultiHandler(defaultEngine(t), nil, []TargetConfig{{Name: "a", Upstream: upstream.server(t).URL}})
	if err != nil {
		t.Fatal(err)
	}
	serve(h, `{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"a__transfer","arguments":{"account_id":12345678901234567},"_meta":{"progressToken":"p1"}}}`)
	if upstream.count() != 1 {
		t.Fatalf("forwarded %d requests, want 1", upstream.count())
	}
	body := upstream.bodies[0]
	for _, want := range []string{`9007199254740993`, `12345678901234567`, `"name":"transfer"`, `"progressToken":"p1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("forwarded body %s lacks %s", body, want)
		}
	}
}

func TestParseErrorsAndMethodNotAllowed(t *testing.T) {
	upstream := &recordingUpstream{}
	h, err := NewHandler(defaultEngine(t), nil, upstream.server(t).URL)
	if err != nil {
		t.Fatal(err)
	}
	if rec := serve(h, `{not json`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `-32700`) {
		t.Fatalf("malformed JSON: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(h, `{"jsonrpc":"2.0","id":"x","method":"tools/call","params":{"name":7}}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `-32602`) || !strings.Contains(rec.Body.String(), `"id":"x"`) {
		t.Fatalf("invalid params: %d %s", rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET: %d Allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
	if upstream.count() != 0 {
		t.Fatalf("invalid requests were forwarded")
	}
}

type memoryAuditor struct {
	mu     sync.Mutex
	events []audit.Event
}

func (a *memoryAuditor) Log(_ context.Context, event audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, event)
	return nil
}

func TestAuditCoversEveryMethodWithAttribution(t *testing.T) {
	upstream := &recordingUpstream{}
	auditor := &memoryAuditor{}
	h, err := NewHandler(defaultEngine(t), auditor, upstream.server(t).URL)
	if err != nil {
		t.Fatal(err)
	}
	headers := []string{"x-omniswitch-key-id", "key-1", "x-omniswitch-workspace-id", "ws-1", "Mcp-Session-Id", "sess-1"}
	serve(h, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"file:///etc"}}`, headers...)
	serve(h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{}}}`, headers...)
	if len(auditor.events) != 2 {
		t.Fatalf("audit events = %d, want 2", len(auditor.events))
	}
	for index, method := range []string{"resources/read", "tools/call"} {
		event := auditor.events[index]
		if event.Method != method || event.KeyID != "key-1" || event.Workspace != "ws-1" || event.Session != "sess-1" || event.Target != "default" {
			t.Fatalf("event %d = %+v", index, event)
		}
	}
}

func TestServerSpanContinuesIncomingTrace(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousProvider, previousPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})

	upstream := &recordingUpstream{}
	auditor := &memoryAuditor{}
	h, err := NewHandler(defaultEngine(t), auditor, upstream.server(t).URL)
	if err != nil {
		t.Fatal(err)
	}
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	// Trace context in params._meta (SEP-414).
	serve(h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{},"_meta":{"traceparent":"00-`+traceID+`-00f067aa0ba902b7-01"}}}`)
	// Trace context in the W3C header.
	serve(h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{}}}`, "traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(spans))
	}
	for _, span := range spans {
		if span.SpanContext().TraceID().String() != traceID || span.Parent().SpanID().String() != "00f067aa0ba902b7" {
			t.Fatalf("span %s trace=%s parent=%s", span.Name(), span.SpanContext().TraceID(), span.Parent().SpanID())
		}
	}
	if upstream.count() != 2 || !strings.Contains(upstream.header[1].Get("traceparent"), traceID) || !strings.Contains(upstream.bodies[1], traceID) {
		t.Fatalf("trace context not propagated upstream: %v %v", upstream.header, upstream.bodies)
	}
	if auditor.events[0].TraceID != traceID {
		t.Fatalf("audit trace id = %q", auditor.events[0].TraceID)
	}
}

func TestFederatedListParsesSSEAndFollowsCursor(t *testing.T) {
	var sessions []string
	var mu sync.Mutex
	paged := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		mu.Lock()
		sessions = append(sessions, r.Header.Get("Mcp-Session-Id"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if request.Params.Cursor == "" {
			_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"tools\":[{\"name\":\"t1\"}],\"nextCursor\":\"p2\"}}\n\n", request.ID)
			return
		}
		_, _ = fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"tools\":[{\"name\":\"t2\"}]}}\n\n", request.ID)
	}))
	defer paged.Close()
	plain := &recordingUpstream{}
	h, err := NewMultiHandler(defaultEngine(t), nil, []TargetConfig{
		{Name: "main", Upstream: plain.server(t).URL},
		{Name: "a", Upstream: paged.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "Mcp-Session-Id", "client-session")
	body := rec.Body.String()
	if !strings.Contains(body, `"a__t1"`) || !strings.Contains(body, `"a__t2"`) {
		t.Fatalf("federated list = %s, want a__t1 and a__t2", body)
	}
	for _, session := range sessions {
		if session != "" {
			t.Fatalf("client session id leaked to non-default target: %q", session)
		}
	}
	if len(plain.header) != 1 || plain.header[0].Get("Mcp-Session-Id") != "client-session" {
		t.Fatalf("default target did not receive the client session id")
	}
}

// TestStdioRegressionHelper is a stdio MCP server used by the tests below.
func TestStdioRegressionHelper(t *testing.T) {
	if os.Getenv("OMNISWITCH_TEST_REGRESSION_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var message map[string]json.RawMessage
		_ = json.Unmarshal(scanner.Bytes(), &message)
		var method string
		_ = json.Unmarshal(message["method"], &method)
		id, hasID := message["id"]
		if !hasID {
			if method == "notifications/custom" {
				fmt.Println(`{"jsonrpc":"2.0","method":"notifications/message","params":{"seen":"custom"}}`)
			}
			continue
		}
		if method == "initialize" {
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"serverInfo":{"name":"helper"}}}`+"\n", id)
			continue
		}
		var params struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(message["params"], &params)
		switch params.Name {
		case "slow":
			time.Sleep(300 * time.Millisecond)
		case "hang":
			time.Sleep(time.Hour)
		}
		fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"answer":%q}}`+"\n", id, params.Name)
	}
	os.Exit(0)
}

func regressionStdioHandler(t *testing.T) *Handler {
	t.Helper()
	h, err := NewMultiHandler(defaultEngine(t), nil, []TargetConfig{{
		Name: "s", Transport: "stdio", Command: os.Args[0],
		Args:        []string{"-test.run=^TestStdioRegressionHelper$"},
		Environment: map[string]string{"OMNISWITCH_TEST_REGRESSION_HELPER": "1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}

func TestStdioAbandonedResponseIsNotDeliveredToNextCaller(t *testing.T) {
	h := regressionStdioHandler(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"s__slow","arguments":{}}}`)).WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)
	time.Sleep(400 * time.Millisecond)
	rec := serve(h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"s__fast","arguments":{}}}`)
	if !strings.Contains(rec.Body.String(), `"answer":"fast"`) || !strings.Contains(rec.Body.String(), `"id":1`) {
		t.Fatalf("second caller got %s, want its own answer", rec.Body.String())
	}
}

func TestStdioHungCallHonorsContextAndDoesNotBlockOthers(t *testing.T) {
	h := regressionStdioHandler(t)
	serve(h, `{"jsonrpc":"2.0","id":0,"method":"tools/call","params":{"name":"s__warm","arguments":{}}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"s__hang","arguments":{}}}`)).WithContext(ctx))
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("hung call returned after %s", elapsed)
	}
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"id":1`) {
		t.Fatalf("hung call: %d %s", rec.Code, rec.Body.String())
	}
	// The helper is single-threaded and still asleep, so the next call also
	// times out, but on its own deadline instead of waiting behind a lock.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	started = time.Now()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"s__fast","arguments":{}}}`)).WithContext(ctx2))
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("second call blocked for %s", elapsed)
	}
}

func TestStdioNotificationsAndInitialize(t *testing.T) {
	h := regressionStdioHandler(t)
	rec := serve(h, `{"jsonrpc":"2.0","method":"notifications/custom"}`)
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Fatalf("notification: %d %q, want 202 with empty body", rec.Code, rec.Body.String())
	}
	rec = serve(h, `{"jsonrpc":"2.0","id":"init-1","method":"initialize","params":{}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"init-1"`) || !strings.Contains(rec.Body.String(), `"helper"`) {
		t.Fatalf("initialize: %d %s", rec.Code, rec.Body.String())
	}
	// Server-sent notifications between responses must not break later calls.
	rec = serve(h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"s__ok","arguments":{}}}`)
	if !strings.Contains(rec.Body.String(), `"answer":"ok"`) {
		t.Fatalf("call after notification: %s", rec.Body.String())
	}
}

func defaultEngine(t *testing.T) policy.Engine {
	t.Helper()
	engine, err := policy.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}
