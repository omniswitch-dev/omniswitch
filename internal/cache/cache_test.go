package cache

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

func TestVectorizeSimilarity(t *testing.T) {
	tests := []struct {
		name    string
		left    string
		right   string
		minimum float64
	}{
		{name: "similar questions", left: "summarize the payment incident", right: "please summarize payment incident", minimum: 0.80},
		{name: "different questions", left: "summarize the payment incident", right: "write a poem about mountains", minimum: 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Similarity(Vectorize(tt.left), Vectorize(tt.right))
			if got < tt.minimum {
				t.Fatalf("Similarity() = %.2f, want at least %.2f", got, tt.minimum)
			}
		})
	}
}

func TestPromptText(t *testing.T) {
	got := PromptText([]provider.Message{
		{Role: "system", Content: "Be concise."},
		{Role: "user", Content: "Hello"},
	})
	want := "system: Be concise.\nuser: Hello"
	if got != want {
		t.Fatalf("PromptText() = %q, want %q", got, want)
	}
}

func TestKeyIsStableAndIgnoresStreamFlag(t *testing.T) {
	req := provider.ChatRequest{
		Model:    "test-model",
		Stream:   true,
		Messages: []provider.Message{{Role: "user", Content: "Hello"}},
	}
	left, err := Key("openai", req)
	if err != nil {
		t.Fatalf("Key() error = %v", err)
	}
	req.Stream = false
	right, err := Key("openai", req)
	if err != nil {
		t.Fatalf("Key() error = %v", err)
	}
	if left != right {
		t.Fatalf("stream flag changed cache key: %q != %q", left, right)
	}
	other, err := Key("anthropic", req)
	if err != nil {
		t.Fatalf("Key(other provider) error = %v", err)
	}
	if other == left {
		t.Fatalf("provider name should be part of cache key")
	}
}

func TestSQLiteCacheRoundTripAndExpiration(t *testing.T) {
	c, err := NewSQLiteCache(filepath.Join(t.TempDir(), "nested", "cache"), 250*time.Millisecond)
	if err != nil {
		t.Fatalf("NewSQLiteCache() error = %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	// A fake clock keeps the test independent of how long the writes take.
	// The entry expires on a whole second, where RFC3339Nano strings used
	// to sort after later fractional times.
	clock := time.Date(2026, 10, 18, 12, 0, 0, 750_000_000, time.UTC)
	c.now = func() time.Time { return clock }

	resp := &provider.ChatResponse{
		ID:      "chat_cached",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   "test-model",
		Choices: []provider.Choice{{
			Index:        0,
			Message:      provider.Message{Role: "assistant", Content: "cached"},
			FinishReason: "stop",
		}},
		Usage: provider.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	}
	if err := c.Set("cache-key", resp); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	got, ok, err := c.Get("cache-key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok || got.ID != "chat_cached" || got.Choices[0].Message.Content != "cached" {
		t.Fatalf("Get() = %+v, %v, want cached response", got, ok)
	}

	clock = clock.Add(750 * time.Millisecond)
	_, ok, err = c.Get("cache-key")
	if err != nil {
		t.Fatalf("expired Get() error = %v", err)
	}
	if ok {
		t.Fatalf("expired cache entry returned ok")
	}
}

func TestVectorizeIsOrderAware(t *testing.T) {
	tests := []struct {
		name    string
		left    string
		right   string
		maximum float64
	}{
		{name: "swapped parties", left: "user: transfer 100 dollars from alice to bob", right: "user: transfer 100 dollars from bob to alice", maximum: 0.9},
		{name: "swapped subject and object", left: "user: alice pays bob", right: "user: bob pays alice", maximum: 0.9},
		{name: "same words different roles", left: "system: answer in french\nuser: hello there", right: "system: hello there\nuser: answer in french", maximum: 0.1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Similarity(Vectorize(tt.left), Vectorize(tt.right)); got > tt.maximum {
				t.Fatalf("Similarity() = %.3f, want at most %.2f", got, tt.maximum)
			}
		})
	}
	if got := Similarity(Vectorize("user: alice pays bob"), Vectorize("user: alice pays bob")); got < 0.999 {
		t.Fatalf("identical prompt similarity = %.3f, want 1", got)
	}
}

func visionMessage(caption, url string) []provider.Message {
	return []provider.Message{{
		Role: "user",
		ContentParts: []provider.ContentPart{
			{Type: "text", Text: caption},
			{Type: "image_url", ImageURL: &provider.ImageURL{URL: url}},
		},
	}}
}

func TestPromptTextDigestsNonTextParts(t *testing.T) {
	caption := "describe this picture in detail please, including colours, people and any text that appears in it"
	left := PromptText(visionMessage(caption, "https://example.com/cat.png"))
	right := PromptText(visionMessage(caption, "https://example.com/dog.png"))
	if left == right {
		t.Fatalf("PromptText() is identical for different images: %q", left)
	}
	if !strings.Contains(left, "[image:") {
		t.Fatalf("PromptText() = %q, want image digest token", left)
	}
	if strings.Contains(left, "cat.png") {
		t.Fatalf("PromptText() = %q, must not embed the raw image URL", left)
	}
	if got := Similarity(Vectorize(left), Vectorize(right)); got != 0 {
		t.Fatalf("different images similarity = %.3f, want 0", got)
	}
	same := PromptText(visionMessage(caption, "https://example.com/cat.png"))
	if got := Similarity(Vectorize(left), Vectorize(same)); got < 0.999 {
		t.Fatalf("same image similarity = %.3f, want 1", got)
	}
	textOnly := PromptText([]provider.Message{{Role: "user", Content: caption}})
	if got := Similarity(Vectorize(left), Vectorize(textOnly)); got != 0 {
		t.Fatalf("image vs text-only similarity = %.3f, want 0", got)
	}

	audio := func(data string) []provider.Message {
		return []provider.Message{{Role: "user", ContentParts: []provider.ContentPart{{Type: "input_audio", InputAudio: &provider.InputAudio{Data: data, Format: "wav"}}}}}
	}
	if got := Similarity(Vectorize(PromptText(audio("AAAA"))), Vectorize(PromptText(audio("BBBB")))); got != 0 {
		t.Fatalf("different audio similarity = %.3f, want 0", got)
	}
	if got := Similarity(Vectorize(PromptText(audio("AAAA"))), Vectorize(PromptText(audio("AAAA")))); got < 0.999 {
		t.Fatalf("same audio similarity = %.3f, want 1", got)
	}

	toolCall := func(args string) []provider.Message {
		return []provider.Message{
			{Role: "user", Content: "what is the weather"},
			{Role: "assistant", ToolCalls: []provider.ToolCall{{Type: "function", Function: provider.FunctionCall{Name: "weather", Arguments: args}}}},
			{Role: "tool", Content: "sunny"},
		}
	}
	if got := Similarity(Vectorize(PromptText(toolCall(`{"city":"paris"}`))), Vectorize(PromptText(toolCall(`{"city":"rome"}`)))); got != 0 {
		t.Fatalf("different tool call similarity = %.3f, want 0", got)
	}
}
