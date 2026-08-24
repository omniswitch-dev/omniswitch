package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// benchProvider answers chat completions instantly, isolating gateway overhead
// from provider latency.
type benchProvider struct {
	name string
}

func (m *benchProvider) Name() string { return m.name }

func (m *benchProvider) ChatCompletion(_ context.Context, _ provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	return provider.ChatResponse{
		ID:      "chat-bench",
		Object:  "chat.completion",
		Created: 1,
		Model:   "mock-model",
		Choices: []provider.Choice{{Index: 0, Message: provider.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
	}, provider.ProviderMeta{Provider: m.name, Model: "mock-model"}, nil
}

func (m *benchProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "mock-model", Object: "model", OwnedBy: m.name}}
}

// BenchmarkChatCompletionsProxyOverhead measures end-to-end added latency of a
// non-streaming chat completion through the full pipeline: budget check,
// input guardrails, cache lookup, routing, and SQLite logging. The backend
// responds in zero time, so the result is the gateway's own cost per request.
// Run with: go test ./internal/gateway -bench ProxyOverhead -benchtime 5s
func BenchmarkChatCompletionsProxyOverhead(b *testing.B) {
	st, err := store.New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	registry := provider.NewRegistry()
	registry.Register(&benchProvider{name: "mock"})

	rtr := router.New(registry)
	gr := guardrail.NewEngine()
	h := New(registry, rtr, st, gr)
	h.SetLogPayloads(false)

	payload := `{"model":"mock-model","messages":[{"role":"user","content":"hello"}],"max_tokens":5}`

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ChatCompletions(rec, req)
		if rec.Code >= 400 {
			b.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
		}
	}
}
