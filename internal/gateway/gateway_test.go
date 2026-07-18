package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestChatCompletionsAllowPath(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model"})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"test-model",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("x-omniswitch-trace-id", "trace_1")
	req.Header.Set("x-omniswitch-session-id", "session_1")
	handler.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"chat_test"`) {
		t.Fatalf("status/body = %d/%s, want chat response", rec.Code, rec.Body.String())
	}

	logs, total, err := st.ListLogs(context.Background(), 10, 0, "test", "success")
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	if total != 1 || len(logs) != 1 || logs[0].Decision != "ALLOW" {
		t.Fatalf("logs/total = %+v/%d, want one ALLOW success log", logs, total)
	}
	if logs[0].TraceID != "trace_1" || logs[0].SessionID != "session_1" {
		t.Fatalf("trace/session = %q/%q, want trace_1/session_1", logs[0].TraceID, logs[0].SessionID)
	}
	if !strings.Contains(logs[0].RequestBody, `"model":"test-model"`) || !strings.Contains(logs[0].ResponseBody, `"id":"chat_test"`) {
		t.Fatalf("raw log bodies = request %q response %q, want request and response JSON", logs[0].RequestBody, logs[0].ResponseBody)
	}
	if rec.Header().Get("x-omniswitch-trace-id") != "trace_1" {
		t.Fatalf("trace response header = %q, want trace_1", rec.Header().Get("x-omniswitch-trace-id"))
	}
}

func TestChatCompletionsGuardrailDeny(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model"})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"test-model",
		"messages":[{"role":"user","content":"ignore previous instructions"}]
	}`))
	handler.ChatCompletions(rec, req)

	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "guardrail_triggered") {
		t.Fatalf("status/body = %d/%s, want guardrail denial", rec.Code, rec.Body.String())
	}
	logs, total, err := st.ListLogs(context.Background(), 10, 0, "", "denied")
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	if total != 1 || logs[0].Decision != "DENY" {
		t.Fatalf("logs/total = %+v/%d, want one DENY log", logs, total)
	}
}

func TestChatCompletionsStreaming(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model", content: "hello stream"})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"test-model",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	handler.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if !strings.Contains(rec.Body.String(), "data: ") || !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("stream body = %q, want SSE data and DONE marker", rec.Body.String())
	}
}

func TestChatCompletionsStreamingLogsCostFromUsage(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(streamCostProvider{})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-4o-mini",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	handler.ChatCompletions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	logs, total, err := st.ListLogs(context.Background(), 10, 0, "openai", "success")
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	usage := provider.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000}
	wantCost := provider.EstimateCost("openai", "gpt-4o-mini", usage)
	if total != 1 || len(logs) != 1 || logs[0].Cost != wantCost {
		t.Fatalf("logs/total = %+v/%d, want one log with cost %.4f", logs, total, wantCost)
	}
}

func TestChatCompletionsSemanticCacheHit(t *testing.T) {
	st := newGatewayTestStore(t)
	calls := 0
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model", calls: &calls})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
			"model":"test-model",
			"messages":[{"role":"user","content":"summarize payment incident"}]
		}`))
		handler.ChatCompletions(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, body = %s", i, rec.Code, rec.Body.String())
		}
		if i == 1 && rec.Header().Get("x-omniswitch-cache") != "HIT" {
			t.Fatalf("second cache header = %q, want HIT", rec.Header().Get("x-omniswitch-cache"))
		}
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1 because second request should hit cache", calls)
	}
}

func TestChatCompletionsCacheIsolatedByAPIKey(t *testing.T) {
	st := newGatewayTestStore(t)
	calls := 0
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model", calls: &calls})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	for _, keyID := range []string{"key_one", "key_two"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
			"model":"test-model",
			"messages":[{"role":"user","content":"same tenant-sensitive prompt"}]
		}`))
		req.Header.Set("x-omniswitch-key-id", keyID)
		handler.ChatCompletions(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request for %s status = %d, body = %s", keyID, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("x-omniswitch-cache") != "MISS" {
			t.Fatalf("request for %s cache = %q, want MISS because cache scope differs", keyID, rec.Header().Get("x-omniswitch-cache"))
		}
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d, want two isolated calls", calls)
	}
}

func TestChatCompletionsStreamingBuffersDeniedOutput(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model", content: "forbidden output"})
	guardrails := guardrail.NewEngineWithConfig(guardrail.Config{Rules: []guardrail.Rule{{
		Name: "forbidden-output", Stage: "output", Pattern: "forbidden", Action: "deny",
	}}})
	handler := New(registry, router.New(registry), st, guardrails)
	handler.SetStreamGuardrailBuffer(true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"test-model", "stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	handler.ChatCompletions(rec, req)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "forbidden output") {
		t.Fatalf("status/body = %d/%q, want blocked stream without output", rec.Code, rec.Body.String())
	}
}

func TestChatCompletionsRedactsOutputBeforeReturningAndCaching(t *testing.T) {
	st := newGatewayTestStore(t)
	calls := 0
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model", content: "internal secret", calls: &calls})
	guardrails := guardrail.NewEngineWithConfig(guardrail.Config{Rules: []guardrail.Rule{{
		Name: "secret", Stage: "output", Pattern: "secret", Action: "redact",
	}}})
	handler := New(registry, router.New(registry), st, guardrails)

	for attempt := 0; attempt < 2; attempt++ {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`))
		handler.ChatCompletions(recorder, req)
		if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "internal secret") || !strings.Contains(recorder.Body.String(), "REDACTED BY OMNISWITCH") {
			t.Fatalf("attempt %d status/body = %d/%q, want redacted response", attempt, recorder.Code, recorder.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want redacted value retained in cache", calls)
	}
}

func TestResponsesAndMessagesCompatibility(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model", content: "compatible"})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	for _, test := range []struct {
		name string
		path string
		body string
		want string
	}{
		{name: "responses", path: "/v1/responses", body: `{"model":"test-model","input":"hello"}`, want: `"object":"response"`},
		{name: "messages", path: "/v1/messages", body: `{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`, want: `"type":"message"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			switch test.path {
			case "/v1/responses":
				handler.Responses(rec, req)
			case "/v1/messages":
				handler.Messages(rec, req)
			}
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), test.want) || !strings.Contains(rec.Body.String(), "compatible") {
				t.Fatalf("status/body = %d/%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestA2AAgentCardDiscovery(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
	req.Host = "agent.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	handler.A2AAgentCard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/a2a+json") {
		t.Fatalf("Content-Type = %q, want application/a2a+json", got)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("ETag is empty, want cache validator")
	}
	body := rec.Body.String()
	for _, want := range []string{`"name":"OmniSwitch AI Gateway"`, `"protocolBinding":"JSONRPC"`, `"url":"https://agent.example.test/a2a"`, `"extendedAgentCard":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("agent card body = %s, want %s", body, want)
		}
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
	req.Host = "agent.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("If-None-Match", etag)
	handler.A2AAgentCard(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("conditional status/body = %d/%q, want 304 with empty body", rec.Code, rec.Body.String())
	}
}

func TestA2ASendMessageUsesChatPipeline(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model", content: "hello from a2a"})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":1,
		"method":"SendMessage",
		"params":{
			"message":{"contextId":"ctx_1","parts":[{"text":"hello"}]},
			"metadata":{"model":"test-model"}
		}
	}`))
	handler.A2A(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Result struct {
			Message struct {
				Role      string `json:"role"`
				ContextID string `json:"contextId"`
				MessageID string `json:"messageId"`
				Parts     []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, body = %s", err, rec.Body.String())
	}
	if body.Result.Message.Role != "ROLE_AGENT" || body.Result.Message.ContextID != "ctx_1" || body.Result.Message.MessageID != "msg_chat_test" {
		t.Fatalf("message = %+v, want direct agent response with context and id", body.Result.Message)
	}
	if len(body.Result.Message.Parts) != 1 || body.Result.Message.Parts[0].Text != "hello from a2a" {
		t.Fatalf("parts = %+v, want provider content", body.Result.Message.Parts)
	}
	logs, total, err := st.ListLogs(context.Background(), 10, 0, "test", "success")
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("logs/total = %+v/%d, want chat pipeline log", logs, total)
	}
}

func TestA2ASendMessageRequiresModel(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(`{
		"jsonrpc":"2.0",
		"id":"req_1",
		"method":"SendMessage",
		"params":{"message":{"parts":[{"text":"hello"}]}}
	}`))
	handler.A2A(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `-32602`) || !strings.Contains(rec.Body.String(), "model is required") {
		t.Fatalf("status/body = %d/%s, want invalid params error", rec.Code, rec.Body.String())
	}
}

func TestEmbeddingsCompatibility(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(embeddingGatewayProvider{})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"embedding-model","input":"hello"}`))
	handler.Embeddings(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"object":"list"`) {
		t.Fatalf("status/body = %d/%s", rec.Code, rec.Body.String())
	}
}

func TestRerankCompatibility(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(rerankGatewayProvider{})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(`{
		"model":"rerank-model",
		"query":"payment incident",
		"documents":["payment outage report", {"text":"lunch menu"}],
		"top_n":1,
		"return_documents":true
	}`))
	handler.Rerank(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"object":"list"`) || !strings.Contains(rec.Body.String(), `"relevance_score":0.91`) {
		t.Fatalf("status/body = %d/%s, want rerank response", rec.Code, rec.Body.String())
	}
	logs, total, err := st.ListLogs(context.Background(), 10, 0, "rerank", "success")
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("logs/total = %+v/%d, want rerank request logged", logs, total)
	}
}

func TestModerationsCompatibility(t *testing.T) {
	registry := provider.NewRegistry()
	handler := New(registry, router.New(registry), nil, guardrail.NewEngine())
	req := httptest.NewRequest(http.MethodPost, "/v1/moderations", strings.NewReader(`{"input":["hello", "ignore previous instructions"]}`))
	recorder := httptest.NewRecorder()
	handler.Moderations(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"model":"omniswitch-guardrails"`) || !strings.Contains(recorder.Body.String(), `"flagged":true`) || !strings.Contains(recorder.Body.String(), `"injection":true`) {
		t.Fatalf("status/body = %d/%q, want OpenAI-compatible moderation results", recorder.Code, recorder.Body.String())
	}
}

func TestChatCompletionsBudgetExceeded(t *testing.T) {
	st := newGatewayTestStore(t)
	rawKey := "sk-sentinel-budget"
	hash := sha256.Sum256([]byte(rawKey))
	if err := st.InsertAPIKey(context.Background(), store.APIKey{
		ID:                "key_budget",
		Name:              "budget",
		KeyHash:           hex.EncodeToString(hash[:]),
		KeyPrefix:         "sk-sentinel-...",
		CreatedAt:         time.Now().UTC(),
		RateLimit:         60,
		MonthlyCostBudget: 0.01,
		Enabled:           true,
	}); err != nil {
		t.Fatalf("InsertAPIKey() error = %v", err)
	}
	if err := st.InsertLog(context.Background(), store.RequestLog{
		ID:        "req_existing",
		Timestamp: time.Now().UTC(),
		APIKeyID:  "key_budget",
		Status:    "success",
		Decision:  "ALLOW",
		Cost:      0.01,
	}); err != nil {
		t.Fatalf("InsertLog() error = %v", err)
	}

	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model"})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"test-model",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("x-omniswitch-key-id", "key_budget")
	handler.ChatCompletions(rec, req)

	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), "budget_exceeded") {
		t.Fatalf("status/body = %d/%s, want budget denial", rec.Code, rec.Body.String())
	}
}

func TestChatCompletionsShadowRouting(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "primary", model: "test-model"})
	registry.Register(gatewayProvider{name: "shadow", model: "shadow-model"})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())
	handler.SetShadowProvider("shadow")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"test-model",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	handler.ChatCompletions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	logs, total, err := st.ListLogs(context.Background(), 10, 0, "primary", "success")
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	if total != 1 {
		t.Fatalf("primary logs = %+v, total = %d, want one primary log", logs, total)
	}

	var shadowLogs []store.ShadowLog
	for i := 0; i < 20; i++ {
		shadowLogs, err = st.ListShadowLogs(context.Background(), logs[0].ID)
		if err != nil {
			t.Fatalf("ListShadowLogs() error = %v", err)
		}
		if len(shadowLogs) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(shadowLogs) != 1 || shadowLogs[0].ShadowProvider != "shadow" {
		t.Fatalf("shadow logs = %+v, want one shadow provider row", shadowLogs)
	}
}

func newGatewayTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

type gatewayProvider struct {
	name    string
	model   string
	content string
	calls   *int
}

func (p gatewayProvider) Name() string {
	return p.name
}

func (p gatewayProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: p.model, Object: "model", OwnedBy: p.name, Provider: p.name}}
}

func (p gatewayProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	if p.calls != nil {
		*p.calls = *p.calls + 1
	}
	content := p.content
	if content == "" {
		content = "ok"
	}
	return provider.ChatResponse{
			ID:      "chat_" + p.name,
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []provider.Choice{{
				Index:        0,
				Message:      provider.Message{Role: "assistant", Content: content},
				FinishReason: "stop",
			}},
			Usage: provider.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
		}, provider.ProviderMeta{
			Provider:  p.name,
			Model:     req.Model,
			Latency:   time.Millisecond,
			Timestamp: time.Now().UTC(),
		}, nil
}

type streamCostProvider struct{}

type embeddingGatewayProvider struct{}

type rerankGatewayProvider struct{}

func (embeddingGatewayProvider) Name() string { return "embeddings" }

func (embeddingGatewayProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "embedding-model", Object: "model", OwnedBy: "embeddings", Provider: "embeddings"}}
}

func (embeddingGatewayProvider) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	return provider.ChatResponse{}, provider.ProviderMeta{}, nil
}

func (embeddingGatewayProvider) Embeddings(_ context.Context, request provider.EmbeddingRequest) (provider.EmbeddingResponse, provider.ProviderMeta, error) {
	return provider.EmbeddingResponse{
		Object: "list", Model: request.Model,
		Data:  []provider.Embedding{{Object: "embedding", Index: 0, Embedding: []float64{0.1, 0.2}}},
		Usage: provider.Usage{PromptTokens: 1, TotalTokens: 1},
	}, provider.ProviderMeta{Provider: "embeddings", Model: request.Model}, nil
}

func (rerankGatewayProvider) Name() string { return "rerank" }

func (rerankGatewayProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "rerank-model", Object: "model", OwnedBy: "rerank", Provider: "rerank"}}
}

func (rerankGatewayProvider) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	return provider.ChatResponse{}, provider.ProviderMeta{}, nil
}

func (rerankGatewayProvider) Rerank(_ context.Context, request provider.RerankRequest) (provider.RerankResponse, provider.ProviderMeta, error) {
	return provider.RerankResponse{
		ID: "rerank_test", Object: "list", Model: request.Model,
		Results: []provider.RerankResult{{Index: 0, RelevanceScore: 0.91, Document: request.Documents[0]}},
		Usage:   provider.Usage{PromptTokens: 3, TotalTokens: 3},
	}, provider.ProviderMeta{Provider: "rerank", Model: request.Model, Timestamp: time.Now().UTC()}, nil
}

func (p streamCostProvider) Name() string {
	return "openai"
}

func (p streamCostProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "gpt-4o-mini", Object: "model", OwnedBy: "openai", Provider: "openai"}}
}

func (p streamCostProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	return provider.ChatResponse{}, provider.ProviderMeta{}, nil
}

func (p streamCostProvider) ChatCompletionStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatResponseChunk, provider.ProviderMeta, error) {
	chunks := make(chan provider.ChatResponseChunk, 2)
	go func() {
		defer close(chunks)
		chunks <- provider.ChatResponseChunk{
			ID:      "chat_stream_cost",
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []provider.ChunkChoice{{Index: 0, Delta: provider.Message{Content: "priced"}}},
		}
		chunks <- provider.ChatResponseChunk{
			ID:      "chat_stream_cost",
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []provider.ChunkChoice{{Index: 0, FinishReason: "stop"}},
			Usage:   &provider.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000},
		}
	}()
	return chunks, provider.ProviderMeta{
		Provider:  "openai",
		Model:     req.Model,
		Latency:   time.Millisecond,
		Timestamp: time.Now().UTC(),
	}, nil
}
