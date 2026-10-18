package proxy

import (
	"context"
	"encoding/json"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestInjectTraceMeta(t *testing.T) {
	tid, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatalf("trace id: %v", err)
	}
	sid, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatalf("span id: %v", err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"demo__echo","arguments":{"x":1}}}`)
	out := injectTraceMeta(ctx, body)

	var req struct {
		Params struct {
			Meta map[string]any `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if got := req.Params.Meta["traceparent"]; got != want {
		t.Fatalf("traceparent = %v, want %s", got, want)
	}

	// Caller-supplied context must win.
	withExisting := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"e","_meta":{"traceparent":"00-aabbccddeeff00112233445566778899-0011223344556677-01"}}}`)
	out = injectTraceMeta(ctx, withExisting)
	var req2 map[string]any
	if err := json.Unmarshal(out, &req2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	params := req2["params"].(map[string]any)
	meta := params["_meta"].(map[string]any)
	if meta["traceparent"] != "00-aabbccddeeff00112233445566778899-0011223344556677-01" {
		t.Fatalf("caller traceparent overwritten: %v", meta["traceparent"])
	}

	// No active span: unchanged.
	if got := injectTraceMeta(context.Background(), body); string(got) != string(body) {
		t.Fatalf("body modified without active span")
	}

	// Notifications without params: unchanged.
	noParams := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if got := injectTraceMeta(ctx, noParams); string(got) != string(noParams) {
		t.Fatalf("notification modified: %s", got)
	}
}

// injectTraceMeta applies injectMetaFields to a serialized request, the
// way forwardBody does for live traffic. Requests
// without params or without an active span are returned unchanged. Values are
// passed through as raw JSON, so numbers keep their exact representation.
func injectTraceMeta(ctx context.Context, body []byte) []byte {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return body
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return body
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request["params"], &params); err != nil || params == nil {
		return body
	}
	if !injectMetaFields(ctx, params) {
		return body
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return body
	}
	request["params"] = encoded
	out, err := json.Marshal(request)
	if err != nil {
		return body
	}
	return out
}
