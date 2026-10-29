package proxy

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/omniswitch-dev/omniswitch/internal/adapter/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/omniswitch-dev/omniswitch/internal/proxy"

// textMapPropagator returns the process-wide OpenTelemetry propagator, or the
// W3C Trace Context propagator when none is configured, so MCP (SEP-414)
// propagation works even when the gateway's telemetry exporter is disabled.
func textMapPropagator() propagation.TextMapPropagator {
	propagator := otel.GetTextMapPropagator()
	if len(propagator.Fields()) == 0 {
		return propagation.TraceContext{}
	}
	return propagator
}

// startServerSpan starts the server span for one MCP request. The parent is
// taken from the W3C trace headers, or from params._meta (SEP-414) when the
// headers carry none.
func startServerSpan(r *http.Request, req mcp.Request) (context.Context, oteltrace.Span) {
	propagator := textMapPropagator()
	ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	if !oteltrace.SpanContextFromContext(ctx).IsValid() {
		if carrier := metaCarrier(req.ParamFields); len(carrier) > 0 {
			ctx = propagator.Extract(ctx, carrier)
		}
	}
	method := req.Method
	if method == "" {
		method = "response"
	}
	return otel.Tracer(tracerName).Start(ctx, "mcp "+method,
		oteltrace.WithSpanKind(oteltrace.SpanKindServer),
		oteltrace.WithAttributes(
			attribute.String("rpc.system", "jsonrpc"),
			attribute.String("rpc.method", method),
			attribute.String("mcp.method.name", method),
		),
	)
}

// metaCarrier exposes the string members of params._meta as a carrier.
func metaCarrier(params map[string]json.RawMessage) propagation.MapCarrier {
	raw, ok := params["_meta"]
	if !ok {
		return nil
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil
	}
	carrier := propagation.MapCarrier{}
	for key, value := range meta {
		var text string
		if json.Unmarshal(value, &text) == nil {
			carrier[key] = text
		}
	}
	return carrier
}

// injectTraceHeaders writes the active trace context into HTTP headers.
func injectTraceHeaders(ctx context.Context, header http.Header) {
	if !oteltrace.SpanContextFromContext(ctx).IsValid() {
		return
	}
	textMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
}

// injectMetaFields implements MCP distributed trace context propagation
// (SEP-414): the active span is written into params._meta (traceparent and
// friends) so downstream MCP servers can continue the trace. A caller-supplied
// traceparent wins, and a non-object _meta is left untouched. It reports
// whether params changed.
func injectMetaFields(ctx context.Context, params map[string]json.RawMessage) bool {
	if params == nil || !oteltrace.SpanContextFromContext(ctx).IsValid() {
		return false
	}
	meta := map[string]json.RawMessage{}
	if raw, ok := params["_meta"]; ok {
		if err := json.Unmarshal(raw, &meta); err != nil || meta == nil {
			return false
		}
	}
	if _, exists := meta["traceparent"]; exists {
		return false
	}
	carrier := propagation.MapCarrier{}
	textMapPropagator().Inject(ctx, carrier)
	if carrier["traceparent"] == "" {
		return false
	}
	for key, value := range carrier {
		if _, exists := meta[key]; exists {
			continue
		}
		encoded, _ := json.Marshal(value)
		meta[key] = encoded
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return false
	}
	params["_meta"] = encoded
	return true
}
