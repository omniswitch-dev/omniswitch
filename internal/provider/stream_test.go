package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAICompatibleStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q, want bearer key", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	chunks, meta, err := streamOpenAICompatible(context.Background(), server.Client(), server.URL, "test-key", ChatRequest{Model: "test-model"}, "test", nil)
	if err != nil {
		t.Fatalf("streamOpenAICompatible() error = %v", err)
	}
	if meta.Provider != "test" || meta.Model != "test-model" {
		t.Fatalf("meta = %+v, want provider/model", meta)
	}
	got := collectChunks(chunks)
	if len(got) != 1 || got[0].Choices[0].Delta.Content != "hello" {
		t.Fatalf("chunks = %+v, want one hello chunk", got)
	}
}

func sseBody(lines ...string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(strings.Join(lines, "\n\n") + "\n\n"))
}

func lastErr(t *testing.T, got []ChatResponseChunk) error {
	t.Helper()
	for i, chunk := range got {
		if chunk.Err != nil && i != len(got)-1 {
			t.Fatalf("Err chunk at %d is not last (%d chunks)", i, len(got))
		}
	}
	if len(got) == 0 {
		return nil
	}
	last := got[len(got)-1]
	if last.Err != nil && len(last.Choices) != 0 {
		t.Fatalf("Err chunk must carry no choices: %+v", last)
	}
	return last.Err
}

func TestOpenAIStreamMovesUsageToFinalChunk(t *testing.T) {
	got := collectChunks(streamOpenAIEvents(context.Background(), sseBody(
		`data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
		`data: {"id":"c","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		`data: [DONE]`,
	), "test"))
	if err := lastErr(t, got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("chunks = %+v, want 2 choice chunks + usage chunk", got)
	}
	for _, chunk := range got[:2] {
		if chunk.Usage != nil {
			t.Fatalf("usage left on a choice chunk: %+v", chunk)
		}
	}
	if last := got[2]; len(last.Choices) != 0 || last.Usage == nil || last.Usage.TotalTokens != 4 || last.ID != "c" {
		t.Fatalf("final chunk = %+v", last)
	}
}

func TestOpenAIStreamUsesGroqUsageExtension(t *testing.T) {
	got := collectChunks(streamOpenAIEvents(context.Background(), sseBody(
		`data: {"id":"c","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"x_groq":{"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}}`,
		`data: [DONE]`,
	), "groq"))
	if last := got[len(got)-1]; last.Usage == nil || last.Usage.TotalTokens != 7 {
		t.Fatalf("final chunk = %+v", last)
	}
}

func TestOpenAIStreamErrorEventEndsStream(t *testing.T) {
	got := collectChunks(streamOpenAIEvents(context.Background(), sseBody(
		`data: {"id":"c","choices":[{"index":0,"delta":{"content":"par"}}]}`,
		`data: {"error":{"message":"overloaded","type":"server_error"}}`,
		`data: {"id":"c","choices":[{"index":0,"delta":{"content":"never"}}]}`,
	), "openai"))
	err := lastErr(t, got)
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("err = %v, chunks = %+v", err, got)
	}
	if len(got) != 2 {
		t.Fatalf("chunks after error were forwarded: %+v", got)
	}
}

func TestOpenAIStreamTruncatedAndMalformed(t *testing.T) {
	got := collectChunks(streamOpenAIEvents(context.Background(), sseBody(
		`data: {"id":"c","choices":[{"index":0,"delta":{"content":"par"}}]}`,
	), "openai"))
	if err := lastErr(t, got); !errors.Is(err, ErrStreamTruncated) {
		t.Fatalf("truncated stream err = %v", err)
	}
	got = collectChunks(streamOpenAIEvents(context.Background(), sseBody(`data: {not json`), "openai"))
	if err := lastErr(t, got); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed err = %v", err)
	}
}

func TestOpenAIStreamAllowsLargeEvents(t *testing.T) {
	big := strings.Repeat("a", 2<<20)
	got := collectChunks(streamOpenAIEvents(context.Background(), sseBody(
		`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"`+big+`"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	), "openai"))
	if err := lastErr(t, got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Choices[0].Delta.ToolCalls[0].Function.Arguments) != len(big) {
		t.Fatalf("large event lost")
	}
}

type errReader struct{ data io.Reader }

func (r *errReader) Read(p []byte) (int, error) {
	n, err := r.data.Read(p)
	if err == io.EOF {
		return n, errors.New("connection reset")
	}
	return n, err
}

func TestOpenAIStreamReadErrorSurfaces(t *testing.T) {
	body := io.NopCloser(&errReader{data: strings.NewReader("data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n")})
	got := collectChunks(streamOpenAIEvents(context.Background(), body, "openai"))
	if err := lastErr(t, got); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamProducersStopWhenContextCancelled(t *testing.T) {
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, `data: {"id":"c","choices":[{"index":0,"delta":{"content":"x"}}]}`)
	}
	producers := map[string]func(ctx context.Context) <-chan ChatResponseChunk{
		"openai": func(ctx context.Context) <-chan ChatResponseChunk {
			return streamOpenAIEvents(ctx, sseBody(lines...), "openai")
		},
		"fromResponse": func(ctx context.Context) <-chan ChatResponseChunk {
			return StreamFromResponse(ctx, ChatResponse{Choices: []Choice{{Message: Message{Content: strings.Repeat("w ", 50)}}}})
		},
	}
	for name, start := range producers {
		ctx, cancel := context.WithCancel(context.Background())
		chunks := start(ctx)
		<-chunks
		cancel()
		// The producer must close the channel without anyone reading the
		// remaining chunks; give it a moment, then drain with a deadline.
		deadline := time.After(2 * time.Second)
		closed := false
		for !closed {
			select {
			case _, ok := <-chunks:
				if !ok {
					closed = true
				}
			case <-deadline:
				t.Fatalf("%s: producer did not stop after cancellation", name)
			}
		}
	}
}

func TestStreamFromResponseShape(t *testing.T) {
	got := collectChunks(StreamFromResponse(context.Background(), ChatResponse{ID: "r", Model: "m",
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "a b"}, FinishReason: "length"}},
		Usage:   Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}}))
	if got[0].Choices[0].Delta.Role != "assistant" {
		t.Fatalf("first chunk must carry the role: %+v", got[0])
	}
	finish := got[len(got)-2]
	if finish.Choices[0].FinishReason != "length" || finish.Usage != nil {
		t.Fatalf("finish chunk = %+v", finish)
	}
	if last := got[len(got)-1]; len(last.Choices) != 0 || last.Usage == nil || last.Usage.TotalTokens != 3 {
		t.Fatalf("usage chunk = %+v", last)
	}
}

func TestAnthropicStreamNormalization(t *testing.T) {
	got := collectChunks(streamAnthropic(context.Background(), sseBody(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-haiku-20241022","usage":{"input_tokens":4}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`,
	), "fallback-model"))
	if err := lastErr(t, got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("chunks = %+v, want role, 2 text, finish, usage", got)
	}
	if got[0].ID != "msg_1" || got[0].Model != "claude-3-5-haiku-20241022" || got[0].Choices[0].Delta.Role != "assistant" {
		t.Fatalf("first chunk = %+v", got[0])
	}
	if got[1].Choices[0].Delta.Role != "" || got[1].Choices[0].Delta.Content+got[2].Choices[0].Delta.Content != "hello" {
		t.Fatalf("content chunks = %+v", got[1:3])
	}
	if got[3].Choices[0].FinishReason != "stop" || got[3].Usage != nil {
		t.Fatalf("finish chunk = %+v", got[3])
	}
	last := got[4]
	if len(last.Choices) != 0 || last.Usage == nil || last.Usage.PromptTokens != 4 || last.Usage.CompletionTokens != 2 || last.Usage.TotalTokens != 6 {
		t.Fatalf("usage chunk = %+v", last)
	}
}

func TestAnthropicStreamToolOrdinalsAndThinking(t *testing.T) {
	got := collectChunks(streamAnthropic(context.Background(), sseBody(
		`data: {"type":"message_start","message":{"id":"m","usage":{"input_tokens":1}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_a","name":"f","input":{}}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"x\":1}"}}`,
		`data: {"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_b","name":"g","input":{}}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		`data: {"type":"message_stop"}`,
	), "m"))
	if err := lastErr(t, got); err != nil {
		t.Fatal(err)
	}
	var indexes []int
	reasoning := ""
	finish := ""
	for _, chunk := range got {
		for _, choice := range chunk.Choices {
			reasoning += choice.Delta.ReasoningContent
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			for _, call := range choice.Delta.ToolCalls {
				indexes = append(indexes, *call.Index)
			}
		}
	}
	if fmt.Sprint(indexes) != "[0 0 1]" {
		t.Fatalf("tool indexes = %v, want [0 0 1] (ordinals, not block indexes)", indexes)
	}
	if reasoning != "hmm" || finish != "tool_calls" {
		t.Fatalf("reasoning=%q finish=%q", reasoning, finish)
	}
}

func TestAnthropicStreamErrorEventAndTruncation(t *testing.T) {
	got := collectChunks(streamAnthropic(context.Background(), sseBody(
		`data: {"type":"message_start","message":{"id":"m"}}`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	), "m"))
	if err := lastErr(t, got); err == nil || !strings.Contains(err.Error(), "Overloaded") {
		t.Fatalf("err = %v", err)
	}
	got = collectChunks(streamAnthropic(context.Background(), sseBody(
		`data: {"type":"message_start","message":{"id":"m"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut"}}`,
	), "m"))
	if err := lastErr(t, got); !errors.Is(err, ErrStreamTruncated) {
		t.Fatalf("err = %v", err)
	}
}

func TestGeminiStreamNormalization(t *testing.T) {
	got := collectChunks(streamGemini(context.Background(), sseBody(
		`data: {"candidates":[{"content":{"parts":[{"text":"hel"}],"role":"model"}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"lo"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"thoughtsTokenCount":4,"totalTokenCount":9}}`,
	), "gemini-2.5-flash"))
	if err := lastErr(t, got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("chunks = %+v, want 2 content chunks + usage chunk", got)
	}
	if got[0].Choices[0].Delta.Role != "assistant" || got[1].Choices[0].Delta.Role != "" {
		t.Fatalf("role must be on the first chunk only")
	}
	if got[0].Choices[0].Delta.Content+got[1].Choices[0].Delta.Content != "hello" || got[1].Choices[0].FinishReason != "stop" || got[1].Usage != nil {
		t.Fatalf("content chunks = %+v", got[:2])
	}
	last := got[2]
	if len(last.Choices) != 0 || last.Usage == nil || last.Usage.PromptTokens != 3 || last.Usage.CompletionTokens != 6 || last.Usage.TotalTokens != 9 {
		t.Fatalf("usage chunk = %+v", last.Usage)
	}
	if last.Usage.CompletionTokensDetails == nil || last.Usage.CompletionTokensDetails.ReasoningTokens != 4 {
		t.Fatalf("reasoning tokens missing: %+v", last.Usage)
	}
}

func TestGeminiStreamToolCallsUniqueAcrossChunks(t *testing.T) {
	got := collectChunks(streamGemini(context.Background(), sseBody(
		`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"a","args":{}}}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"b","args":{}}}]},"finishReason":"STOP"}]}`,
	), "g"))
	var calls []ToolCall
	for _, chunk := range got {
		for _, choice := range chunk.Choices {
			calls = append(calls, choice.Delta.ToolCalls...)
		}
	}
	if len(calls) != 2 || *calls[0].Index != 0 || *calls[1].Index != 1 || calls[0].ID == calls[1].ID {
		raw, _ := json.Marshal(calls)
		t.Fatalf("tool calls = %s", raw)
	}
	if got[1].Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", got[1].Choices[0].FinishReason)
	}
}

func TestGeminiStreamErrorsAndSafety(t *testing.T) {
	got := collectChunks(streamGemini(context.Background(), sseBody(
		`data: {"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`,
	), "g"))
	if err := lastErr(t, got); err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("err = %v", err)
	}
	got = collectChunks(streamGemini(context.Background(), sseBody(
		`data: {"candidates":[{"content":{"parts":[{"text":"x"}]}}]}`,
	), "g"))
	if err := lastErr(t, got); !errors.Is(err, ErrStreamTruncated) {
		t.Fatalf("err = %v", err)
	}
	got = collectChunks(streamGemini(context.Background(), sseBody(
		`data: {"candidates":[{"content":{"parts":[]},"finishReason":"SAFETY"}]}`,
	), "g"))
	if got[0].Choices[0].FinishReason != "content_filter" {
		t.Fatalf("finish = %q", got[0].Choices[0].FinishReason)
	}
}

func TestStreamingAdaptersAlwaysRequestUsage(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	req := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}
	o := NewOpenAI("k")
	o.baseURL = server.URL
	adapters := []Provider{o, NewCustom("local", server.URL, ""), NewAzureOpenAI("az", server.URL, "k", "", nil)}
	for _, p := range adapters {
		chunks, _, err := p.(interface {
			ChatCompletionStream(context.Context, ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error)
		}).ChatCompletionStream(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		collectChunks(chunks)
	}
	mistral := NewCustom("mistral", server.URL+"/api.mistral.ai", "")
	chunks, _, _ := mistral.ChatCompletionStream(context.Background(), req)
	collectChunks(chunks)

	if len(bodies) != 4 {
		t.Fatalf("got %d upstream requests", len(bodies))
	}
	for i, body := range bodies[:3] {
		opts, _ := body["stream_options"].(map[string]any)
		if opts["include_usage"] != true {
			t.Fatalf("request %d did not ask for usage: %v", i, body)
		}
	}
	if _, ok := bodies[3]["stream_options"]; ok {
		t.Fatalf("mistral must not receive stream_options: %v", bodies[3])
	}
	if req.StreamOptions != nil {
		t.Fatal("caller's request was mutated")
	}
}

func collectChunks(chunks <-chan ChatResponseChunk) []ChatResponseChunk {
	var got []ChatResponseChunk
	for chunk := range chunks {
		got = append(got, chunk)
	}
	return got
}
