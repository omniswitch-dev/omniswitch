package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompatibleStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want bearer key", r.Header.Get("Authorization"))
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

	var got []ChatResponseChunk
	for chunk := range chunks {
		got = append(got, chunk)
	}
	if len(got) != 1 || got[0].Choices[0].Delta.Content != "hello" {
		t.Fatalf("chunks = %+v, want one hello chunk", got)
	}
}

func TestAnthropicStreamNormalization(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-haiku-20241022","usage":{"input_tokens":4}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hel"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	got := collectChunks(streamAnthropic(context.Background(), io.NopCloser(strings.NewReader(body)), "fallback-model"))
	if len(got) != 3 {
		t.Fatalf("chunks = %+v, want two text chunks and final chunk", got)
	}
	if got[0].ID != "msg_1" || got[0].Model != "claude-3-5-haiku-20241022" {
		t.Fatalf("first chunk id/model = %q/%q, want Anthropic message metadata", got[0].ID, got[0].Model)
	}
	if got[0].Choices[0].Delta.Content+got[1].Choices[0].Delta.Content != "hello" {
		t.Fatalf("content chunks = %+v, want hello", got[:2])
	}
	if got[2].Choices[0].FinishReason != "stop" {
		t.Fatalf("finish reason = %q, want stop", got[2].Choices[0].FinishReason)
	}
	if got[2].Usage == nil || got[2].Usage.PromptTokens != 4 || got[2].Usage.CompletionTokens != 2 || got[2].Usage.TotalTokens != 6 {
		t.Fatalf("usage = %+v, want 4/2/6", got[2].Usage)
	}
}

func TestGeminiStreamNormalization(t *testing.T) {
	body := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"hel"}],"role":"model"}}]}`,
		``,
		`data: {"candidates":[{"content":{"parts":[{"text":"lo"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`,
		``,
	}, "\n")

	got := collectChunks(streamGemini(context.Background(), io.NopCloser(strings.NewReader(body)), "gemini-2.5-flash"))
	if len(got) != 2 {
		t.Fatalf("chunks = %+v, want two Gemini chunks", got)
	}
	if got[0].Choices[0].Delta.Content+got[1].Choices[0].Delta.Content != "hello" {
		t.Fatalf("content chunks = %+v, want hello", got)
	}
	if got[1].Choices[0].FinishReason != "stop" {
		t.Fatalf("finish reason = %q, want stop", got[1].Choices[0].FinishReason)
	}
	if got[1].Usage == nil || got[1].Usage.PromptTokens != 3 || got[1].Usage.CompletionTokens != 2 || got[1].Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v, want 3/2/5", got[1].Usage)
	}
}

func collectChunks(chunks <-chan ChatResponseChunk) []ChatResponseChunk {
	var got []ChatResponseChunk
	for chunk := range chunks {
		got = append(got, chunk)
	}
	return got
}
