package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// scriptedProvider is a configurable test provider. Non-streaming calls
// return content/finishReason/usage; streaming calls emit chunks. When
// holdOpen is set the stream stays open until the context is cancelled.
type scriptedProvider struct {
	name         string
	model        string
	content      string
	finishReason string
	usage        provider.Usage
	chunks       []provider.ChatResponseChunk
	holdOpen     bool
	failChat     bool

	calls     *int64
	mu        *sync.Mutex
	lastMsgs  *[]provider.Message
	started   chan struct{}
	cancelled chan struct{}
	onChat    func()
}

func (p scriptedProvider) Name() string { return p.name }

func (p scriptedProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: p.model, Object: "model", OwnedBy: p.name, Provider: p.name}}
}

func (p scriptedProvider) record(req provider.ChatRequest) {
	if p.calls != nil {
		atomic.AddInt64(p.calls, 1)
	}
	if p.mu != nil && p.lastMsgs != nil {
		p.mu.Lock()
		*p.lastMsgs = append([]provider.Message(nil), req.Messages...)
		p.mu.Unlock()
	}
}

func (p scriptedProvider) ChatCompletion(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	p.record(req)
	if p.onChat != nil {
		p.onChat()
	}
	if p.failChat {
		return provider.ChatResponse{}, provider.ProviderMeta{Provider: p.name, Model: req.Model}, errors.New("upstream exploded")
	}
	finish := p.finishReason
	if finish == "" {
		finish = "stop"
	}
	return provider.ChatResponse{
		ID: "chat_" + p.name, Object: "chat.completion", Created: time.Now().Unix(), Model: req.Model,
		Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: p.content}, FinishReason: finish}},
		Usage:   p.usage,
	}, provider.ProviderMeta{Provider: p.name, Model: req.Model, Latency: time.Millisecond, Timestamp: time.Now().UTC(), Cost: provider.EstimateCost(p.name, req.Model, p.usage)}, nil
}

func (p scriptedProvider) ChatCompletionStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatResponseChunk, provider.ProviderMeta, error) {
	p.record(req)
	ch := make(chan provider.ChatResponseChunk)
	go func() {
		defer close(ch)
		for _, chunk := range p.chunks {
			chunk.Model = req.Model
			select {
			case ch <- chunk:
			case <-ctx.Done():
				return
			}
		}
		if p.holdOpen {
			if p.started != nil {
				close(p.started)
			}
			<-ctx.Done()
			if p.cancelled != nil {
				close(p.cancelled)
			}
		}
	}()
	return ch, provider.ProviderMeta{Provider: p.name, Model: req.Model, Latency: time.Millisecond, Timestamp: time.Now().UTC()}, nil
}

func contentChunk(index int, text string) provider.ChatResponseChunk {
	return provider.ChatResponseChunk{ID: "chunk", Object: "chat.completion.chunk", Choices: []provider.ChunkChoice{{Index: index, Delta: provider.Message{Content: text}}}}
}

func finishChunk(indices ...int) provider.ChatResponseChunk {
	chunk := provider.ChatResponseChunk{ID: "chunk", Object: "chat.completion.chunk"}
	for _, i := range indices {
		chunk.Choices = append(chunk.Choices, provider.ChunkChoice{Index: i, FinishReason: "stop"})
	}
	return chunk
}

func newHardeningHandler(t *testing.T, gr *guardrail.Engine, providers ...provider.Provider) (*Handler, *store.Store) {
	t.Helper()
	st := newGatewayTestStore(t)
	reg := provider.NewRegistry()
	for _, p := range providers {
		reg.Register(p)
	}
	if gr == nil {
		gr = guardrail.NewEngine()
	}
	return New(reg, router.New(reg), st, gr), st
}

func doRequest(h http.HandlerFunc, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h(rec, req)
	return rec
}

func onlyLog(t *testing.T, st *store.Store, status string) store.RequestLog {
	t.Helper()
	logs, total, err := st.ListLogs(context.Background(), 10, 0, "", status)
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("logs with status %q = %+v (total %d), want exactly one", status, logs, total)
	}
	return logs[0]
}

// streamDeltas concatenates the delta content of every data event per choice.
func streamDeltas(t *testing.T, body string) map[int]string {
	t.Helper()
	out := map[int]string{}
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var chunk provider.ChatResponseChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			out[choice.Index] += choice.Delta.Content
		}
	}
	return out
}

// GW-01: buffered stream guardrails must see every choice, not just index 0.
func TestStreamGuardrailsInspectEveryChoice(t *testing.T) {
	gr := guardrail.NewEngineWithConfig(guardrail.Config{Rules: []guardrail.Rule{{Name: "secret", Stage: "output", Pattern: "TOPSECRET", Action: "deny"}}})
	p := scriptedProvider{name: "test", model: "test-model", chunks: []provider.ChatResponseChunk{
		contentChunk(0, "harmless"), contentChunk(1, "TOPSECRET"), finishChunk(0, 1),
	}}
	h, _ := newHardeningHandler(t, gr, p)
	rec := doRequest(h.ChatCompletions, "/v1/chat/completions", `{"model":"test-model","n":2,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if strings.Contains(rec.Body.String(), "TOPSECRET") {
		t.Fatalf("denied content from choice 1 reached the client: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "guardrail") {
		t.Fatalf("body = %s, want a guardrail denial", rec.Body.String())
	}
}

// GW-01: without guardrail findings both choices are replayed.
func TestStreamAggregatesMultipleChoices(t *testing.T) {
	p := scriptedProvider{name: "test", model: "test-model", chunks: []provider.ChatResponseChunk{
		contentChunk(0, "first "), contentChunk(1, "second "), contentChunk(0, "answer"), contentChunk(1, "answer"), finishChunk(0, 1),
	}}
	h, st := newHardeningHandler(t, nil, p)
	rec := doRequest(h.ChatCompletions, "/v1/chat/completions", `{"model":"test-model","n":2,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	deltas := streamDeltas(t, rec.Body.String())
	if deltas[0] != "first answer" || deltas[1] != "second answer" {
		t.Fatalf("deltas = %#v, want both choices", deltas)
	}
	entry := onlyLog(t, st, statusSuccess)
	if !strings.Contains(entry.ResponseBody, "first answer") || !strings.Contains(entry.ResponseBody, "second answer") {
		t.Fatalf("logged response = %s, want both choices", entry.ResponseBody)
	}
}

// GW-02: a pii:redact action must scrub the prompt before it goes upstream.
func TestInputRedactScrubsUpstreamPrompt(t *testing.T) {
	gr := guardrail.NewEngineWithConfig(guardrail.Config{Actions: map[string]string{"pii": "redact"}})
	var mu sync.Mutex
	var last []provider.Message
	p := scriptedProvider{name: "test", model: "test-model", content: "ok", mu: &mu, lastMsgs: &last}
	h, st := newHardeningHandler(t, gr, p)
	rec := doRequest(h.ChatCompletions, "/v1/chat/completions", `{"model":"test-model","messages":[{"role":"system","content":"be nice"},{"role":"user","content":"my email is john.doe@example.com"}]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(last) != 2 {
		t.Fatalf("upstream messages = %+v", last)
	}
	if strings.Contains(last[1].Content, "john.doe@example.com") || last[1].Content != redactedContent {
		t.Fatalf("upstream user message = %q, want redacted", last[1].Content)
	}
	if last[0].Content != "be nice" {
		t.Fatalf("system message = %q, want unchanged", last[0].Content)
	}
	entry := onlyLog(t, st, statusRedacted)
	if entry.Decision != "ALLOW" {
		t.Fatalf("decision = %q, want ALLOW", entry.Decision)
	}
}

// GW-03: requests that differ in tools or response_format must not share a
// semantic cache entry.
func TestSemanticCacheKeyedOnRequestParameters(t *testing.T) {
	var calls int64
	p := scriptedProvider{name: "test", model: "test-model", content: "sunny", calls: &calls}
	h, _ := newHardeningHandler(t, nil, p)
	base := `{"model":"test-model","messages":[{"role":"user","content":"weather in paris"}]}`
	doRequest(h.ChatCompletions, "/v1/chat/completions", base, nil)
	rec := doRequest(h.ChatCompletions, "/v1/chat/completions", base, nil)
	if rec.Header().Get("x-omniswitch-cache") != "HIT" || calls != 1 {
		t.Fatalf("identical request: cache=%q calls=%d, want HIT and 1 call", rec.Header().Get("x-omniswitch-cache"), calls)
	}
	withTools := `{"model":"test-model","messages":[{"role":"user","content":"weather in paris"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{}}}]}`
	rec = doRequest(h.ChatCompletions, "/v1/chat/completions", withTools, nil)
	if rec.Header().Get("x-omniswitch-cache") == "HIT" || calls != 2 {
		t.Fatalf("tools request served from cache (calls=%d)", calls)
	}
	withFormat := `{"model":"test-model","messages":[{"role":"user","content":"weather in paris"}],"response_format":{"type":"json_object"}}`
	rec = doRequest(h.ChatCompletions, "/v1/chat/completions", withFormat, nil)
	if rec.Header().Get("x-omniswitch-cache") == "HIT" || calls != 3 {
		t.Fatalf("response_format request served from cache (calls=%d)", calls)
	}
}

// GW-07, GW-22: compat streaming works and headers are not duplicated.
func TestCompatStreamingTranslatesEvents(t *testing.T) {
	p := scriptedProvider{name: "test", model: "test-model", chunks: []provider.ChatResponseChunk{
		contentChunk(0, "hello "), contentChunk(0, "world"), finishChunk(0),
	}}
	h, _ := newHardeningHandler(t, nil, p)
	for _, tc := range []struct {
		path, body string
		fn         http.HandlerFunc
		want       []string
	}{
		{"/v1/messages", `{"model":"test-model","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, h.Messages,
			[]string{"event: message_start", "event: content_block_delta", "hello ", "world", `"stop_reason":"end_turn"`, "event: message_stop"}},
		{"/v1/responses", `{"model":"test-model","stream":true,"input":"hi"}`, h.Responses,
			[]string{"event: response.created", "event: response.output_text.delta", "hello ", "world", "event: response.completed"}},
	} {
		rec := doRequest(tc.fn, tc.path, tc.body, map[string]string{"x-omniswitch-trace-id": "trace_compat"})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body = %s", tc.path, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
			t.Fatalf("%s Content-Type = %q", tc.path, ct)
		}
		for key, values := range rec.Header() {
			if len(values) > 1 {
				t.Fatalf("%s header %s duplicated: %v", tc.path, key, values)
			}
		}
		for _, want := range tc.want {
			if !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("%s body missing %q: %s", tc.path, want, rec.Body.String())
			}
		}
		if strings.Contains(rec.Body.String(), "chat.completion.chunk") {
			t.Fatalf("%s leaked raw chat chunks: %s", tc.path, rec.Body.String())
		}
	}
}

// GW-11: non-stream Messages maps OpenAI finish reasons to Anthropic ones.
func TestMessagesMapsStopReason(t *testing.T) {
	for finish, want := range map[string]string{"stop": "end_turn", "length": "max_tokens"} {
		p := scriptedProvider{name: "test", model: "test-model", content: "x", finishReason: finish}
		h, _ := newHardeningHandler(t, nil, p)
		rec := doRequest(h.Messages, "/v1/messages", `{"model":"test-model","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
		if !strings.Contains(rec.Body.String(), `"stop_reason":"`+want+`"`) {
			t.Fatalf("finish %q: body = %s, want stop_reason %q", finish, rec.Body.String(), want)
		}
	}
}

// GW-09: hot-reload setters may run while requests are in flight.
func TestHotReloadSettersAreRaceFree(t *testing.T) {
	p := scriptedProvider{name: "test", model: "test-model", content: "ok", chunks: []provider.ChatResponseChunk{contentChunk(0, "ok"), finishChunk(0)}}
	h, _ := newHardeningHandler(t, nil, p)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			h.SetCacheTTL(time.Duration(i+1) * time.Minute)
			h.SetCacheScope("global")
			h.SetSemanticCache(0.9)
			h.SetGuardrails(guardrail.NewEngine())
			h.SetStreamGuardrailBuffer(i%2 == 0)
			h.SetMaxRequestBytes(int64(1<<20 + i))
			h.SetLogPayloads(i%2 == 0)
			h.SetShadowProvider("")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			stream := "false"
			if i%2 == 0 {
				stream = "true"
			}
			doRequest(h.ChatCompletions, "/v1/chat/completions", `{"model":"test-model","stream":`+stream+`,"messages":[{"role":"user","content":"race `+string(rune('a'+i))+`"}]}`, nil)
		}
	}()
	wg.Wait()
}

// GW-12: when retries are exhausted and the output still violates, the
// gateway denies instead of returning it, and charges for every attempt.
func TestRetryExhaustionDeniesAndSumsCost(t *testing.T) {
	gr := guardrail.NewEngineWithConfig(guardrail.Config{Rules: []guardrail.Rule{{Name: "bad", Stage: "output", Pattern: "BADWORD", Action: "retry", MaxRetries: 1}}})
	var calls int64
	usage := provider.Usage{PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000}
	p := scriptedProvider{name: "openai", model: "gpt-4o-mini", content: "BADWORD again", usage: usage, calls: &calls}
	h, st := newHardeningHandler(t, gr, p)
	rec := doRequest(h.ChatCompletions, "/v1/chat/completions", `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "BADWORD") {
		t.Fatalf("status = %d body = %s, want 403 without the violating output", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	logs, _, err := st.ListLogs(context.Background(), 10, 0, "", "")
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs = %+v err = %v", logs, err)
	}
	want := 2 * provider.EstimateCost("openai", "gpt-4o-mini", usage)
	if diff := logs[0].Cost - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("logged cost = %v, want %v (sum of both attempts)", logs[0].Cost, want)
	}
	if logs[0].TotalTokens != 4000 {
		t.Fatalf("logged tokens = %d, want 4000", logs[0].TotalTokens)
	}
}

// GW-13: the usage-only chunk is used for cost but forwarded only on request.
func TestStreamUsageChunkAccounting(t *testing.T) {
	usage := provider.Usage{PromptTokens: 1000, CompletionTokens: 2000, TotalTokens: 3000}
	chunks := []provider.ChatResponseChunk{contentChunk(0, "priced"), finishChunk(0), {ID: "chunk", Object: "chat.completion.chunk", Choices: []provider.ChunkChoice{}, Usage: &usage}}
	want := provider.EstimateCost("openai", "gpt-4o-mini", usage)
	for _, includeUsage := range []bool{false, true} {
		p := scriptedProvider{name: "openai", model: "gpt-4o-mini", chunks: chunks}
		h, st := newHardeningHandler(t, nil, p)
		h.SetStreamGuardrailBuffer(false)
		body := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hello"}]}`
		if includeUsage {
			body = `{"model":"gpt-4o-mini","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hello"}]}`
		}
		rec := doRequest(h.ChatCompletions, "/v1/chat/completions", body, nil)
		if got := strings.Contains(rec.Body.String(), `"prompt_tokens":1000`); got != includeUsage {
			t.Fatalf("include_usage=%v: usage chunk forwarded=%v body=%s", includeUsage, got, rec.Body.String())
		}
		entry := onlyLog(t, st, statusSuccess)
		if entry.Cost != want || entry.TotalTokens != 3000 {
			t.Fatalf("include_usage=%v: cost/tokens = %v/%d, want %v/3000", includeUsage, entry.Cost, entry.TotalTokens, want)
		}
	}
}

// GW-13: with no usage from upstream, cost falls back to an estimate.
func TestStreamCostEstimatedWithoutUsage(t *testing.T) {
	p := scriptedProvider{name: "openai", model: "gpt-4o-mini", chunks: []provider.ChatResponseChunk{contentChunk(0, strings.Repeat("token ", 50)), finishChunk(0)}}
	h, st := newHardeningHandler(t, nil, p)
	doRequest(h.ChatCompletions, "/v1/chat/completions", `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hello there"}]}`, nil)
	entry := onlyLog(t, st, statusSuccess)
	if entry.Cost <= 0 || entry.OutputTokens <= 0 {
		t.Fatalf("cost/output tokens = %v/%d, want an estimate", entry.Cost, entry.OutputTokens)
	}
}

// Stream Err contract: an upstream error ends the stream with an error event,
// no [DONE], no cache entry and an error log.
func TestStreamErrorChunkEndsStream(t *testing.T) {
	var calls int64
	chunks := []provider.ChatResponseChunk{contentChunk(0, "partial"), {Err: errors.New("upstream reset")}}
	for _, buffered := range []bool{true, false} {
		calls = 0
		p := scriptedProvider{name: "test", model: "test-model", chunks: chunks, calls: &calls}
		h, st := newHardeningHandler(t, nil, p)
		h.SetStreamGuardrailBuffer(buffered)
		body := `{"model":"test-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`
		rec := doRequest(h.ChatCompletions, "/v1/chat/completions", body, nil)
		out := rec.Body.String()
		if !strings.Contains(out, "upstream_stream_error") || !strings.Contains(out, "upstream reset") || strings.Contains(out, "[DONE]") {
			t.Fatalf("buffered=%v body = %s, want error event without [DONE]", buffered, out)
		}
		entry := onlyLog(t, st, statusError)
		if !strings.Contains(entry.ErrorMessage, "upstream reset") {
			t.Fatalf("buffered=%v error message = %q", buffered, entry.ErrorMessage)
		}
		doRequest(h.ChatCompletions, "/v1/chat/completions", body, nil)
		if calls != 2 {
			t.Fatalf("buffered=%v: failed stream was cached (calls=%d)", buffered, calls)
		}
	}
}

// Stream Err contract on the compat endpoints.
func TestCompatStreamErrorTranslation(t *testing.T) {
	p := scriptedProvider{name: "test", model: "test-model", chunks: []provider.ChatResponseChunk{contentChunk(0, "partial"), {Err: errors.New("upstream reset")}}}
	h, _ := newHardeningHandler(t, nil, p)
	rec := doRequest(h.Responses, "/v1/responses", `{"model":"test-model","stream":true,"input":"hi"}`, nil)
	if !strings.Contains(rec.Body.String(), "event: response.failed") || strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatalf("responses body = %s, want response.failed", rec.Body.String())
	}
	rec = doRequest(h.Messages, "/v1/messages", `{"model":"test-model","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if !strings.Contains(rec.Body.String(), "event: error") || strings.Contains(rec.Body.String(), "message_stop") {
		t.Fatalf("messages body = %s, want an error event", rec.Body.String())
	}
}

// GW-15: a client disconnect cancels the upstream stream.
func TestClientDisconnectCancelsUpstream(t *testing.T) {
	for _, buffered := range []bool{true, false} {
		started := make(chan struct{})
		cancelled := make(chan struct{})
		p := scriptedProvider{name: "test", model: "test-model", chunks: []provider.ChatResponseChunk{contentChunk(0, "partial")}, holdOpen: true, started: started, cancelled: cancelled}
		h, st := newHardeningHandler(t, nil, p)
		h.SetStreamGuardrailBuffer(buffered)
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.ChatCompletions(rec, req)
		}()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("stream never started")
		}
		cancel()
		select {
		case <-cancelled:
		case <-time.After(5 * time.Second):
			t.Fatalf("buffered=%v: upstream context was not cancelled after client disconnect", buffered)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("buffered=%v: handler did not return", buffered)
		}
		entry := onlyLog(t, st, statusError)
		if !strings.Contains(entry.ErrorMessage, "client disconnected") {
			t.Fatalf("buffered=%v error message = %q", buffered, entry.ErrorMessage)
		}
	}
}

// GW-16: shadow requests are bounded and use their own circuit breaker.
func TestShadowTrafficBoundedAndIsolated(t *testing.T) {
	var shadowCalls int64
	primary := scriptedProvider{name: "primary", model: "test-model", content: "ok"}
	shadow := scriptedProvider{name: "shadow", model: "test-model", failChat: true, calls: &shadowCalls}
	st := newGatewayTestStore(t)
	reg := provider.NewRegistry()
	reg.Register(primary)
	reg.Register(shadow)
	rt := router.New(reg)
	prodBreaker := router.NewCircuitBreaker(1, time.Minute)
	rt.SetCircuitBreaker(prodBreaker)
	h := New(reg, rt, st, guardrail.NewEngine())

	// Full semaphore: the shadow request is dropped.
	for i := 0; i < cap(h.shadowSlots); i++ {
		h.shadowSlots <- struct{}{}
	}
	h.shadow(rt, "req_dropped", "trace", "primary", provider.ChatRequest{Model: "test-model"}, "shadow")
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt64(&shadowCalls) != 0 {
		t.Fatalf("shadow call ran with a full semaphore")
	}
	for i := 0; i < cap(h.shadowSlots); i++ {
		<-h.shadowSlots
	}

	// A failing shadow provider must not open the production breaker.
	h.shadow(rt, "req_shadow", "trace", "primary", provider.ChatRequest{Model: "test-model", Messages: []provider.Message{{Role: "user", Content: "hi"}}}, "shadow")
	var logs []store.ShadowLog
	for i := 0; i < 200 && len(logs) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		var err error
		if logs, err = st.ListShadowLogs(context.Background(), "req_shadow"); err != nil {
			t.Fatalf("ListShadowLogs() error = %v", err)
		}
	}
	if len(logs) != 1 || logs[0].Status != statusError {
		t.Fatalf("shadow logs = %+v, want one error row", logs)
	}
	if state := prodBreaker.State("shadow"); state != router.CircuitClosed {
		t.Fatalf("production breaker state for shadow = %q, want closed", state)
	}
	if !prodBreaker.Available("shadow") {
		t.Fatalf("production breaker blocks the shadow provider after a shadow failure")
	}
}

// GW-21: cached replay keeps whitespace and UTF-8 intact.
func TestSplitRunesPreservesText(t *testing.T) {
	text := "héllo   wörld\n\n  ✓ tabs\tand  spaces  " + strings.Repeat("日本語", 10)
	parts := splitRunes(text, 4)
	if strings.Join(parts, "") != text {
		t.Fatalf("join(parts) = %q, want %q", strings.Join(parts, ""), text)
	}
	for _, part := range parts {
		if !utf8.ValidString(part) || utf8.RuneCountInString(part) > 4 {
			t.Fatalf("bad part %q", part)
		}
	}
	if got := splitRunes("", 4); len(got) != 0 {
		t.Fatalf("splitRunes(\"\") = %q", got)
	}
}

func TestCachedStreamReplayPreservesWhitespace(t *testing.T) {
	content := "line one\n\n  indented   text ✓ with  double  spaces\tand tab"
	p := scriptedProvider{name: "test", model: "test-model", content: content}
	h, _ := newHardeningHandler(t, nil, p)
	doRequest(h.ChatCompletions, "/v1/chat/completions", `{"model":"test-model","messages":[{"role":"user","content":"replay me"}]}`, nil)
	rec := doRequest(h.ChatCompletions, "/v1/chat/completions", `{"model":"test-model","stream":true,"messages":[{"role":"user","content":"replay me"}]}`, nil)
	if rec.Header().Get("x-omniswitch-cache") != "HIT" {
		t.Fatalf("cache header = %q, want HIT", rec.Header().Get("x-omniswitch-cache"))
	}
	if got := streamDeltas(t, rec.Body.String())[0]; got != content {
		t.Fatalf("replayed content = %q, want %q", got, content)
	}
	if !strings.HasSuffix(strings.TrimSpace(rec.Body.String()), "data: [DONE]") {
		t.Fatalf("replay does not end with [DONE]: %s", rec.Body.String())
	}
}

// GW-22: embeddings and rerank honor x-omniswitch-config.
func TestEmbeddingsAndRerankHonorDynamicConfig(t *testing.T) {
	h, _ := newHardeningHandler(t, nil, embeddingGatewayProvider{}, rerankGatewayProvider{})
	headers := map[string]string{"x-omniswitch-config": "missing-config"}
	rec := doRequest(h.Embeddings, "/v1/embeddings", `{"model":"text-embedding-3-small","input":"hi"}`, headers)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "missing-config") {
		t.Fatalf("embeddings status = %d body = %s, want 400 for unknown config", rec.Code, rec.Body.String())
	}
	rec = doRequest(h.Rerank, "/v1/rerank", `{"model":"rerank-model","query":"q","documents":["a"]}`, headers)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "missing-config") {
		t.Fatalf("rerank status = %d body = %s, want 400 for unknown config", rec.Code, rec.Body.String())
	}
}

// STORE-12: request logs are persisted even when the request context is
// cancelled before logging.
func TestLogPersistedAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := scriptedProvider{name: "test", model: "test-model", content: "ok", onChat: cancel}
	h, st := newHardeningHandler(t, nil, p)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	h.ChatCompletions(httptest.NewRecorder(), req)
	logs, total, err := st.ListLogs(context.Background(), 10, 0, "", "")
	if err != nil || total != 1 {
		t.Fatalf("logs = %+v total = %d err = %v, want one log despite cancelled request", logs, total, err)
	}
	if err := persistContextErr(ctx); err != nil {
		t.Fatalf("persistContext() inherited cancellation: %v", err)
	}
}

func persistContextErr(parent context.Context) error {
	ctx, cancel := persistContext(parent)
	defer cancel()
	return ctx.Err()
}
