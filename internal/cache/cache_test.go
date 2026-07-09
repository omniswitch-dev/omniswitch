package cache

import (
	"path/filepath"
	"testing"
	"time"

	"sentinel/internal/provider"
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

	time.Sleep(300 * time.Millisecond)
	_, ok, err = c.Get("cache-key")
	if err != nil {
		t.Fatalf("expired Get() error = %v", err)
	}
	if ok {
		t.Fatalf("expired cache entry returned ok")
	}
}
