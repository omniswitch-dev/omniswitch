package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCustomChatURLPreservesExplicitChatCompletionsURL(t *testing.T) {
	baseURL := "https://example.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21"
	p := NewCustom("azure", baseURL, "test-key")
	if got := p.chatURL(); got != baseURL {
		t.Fatalf("chatURL() = %q, want %q", got, baseURL)
	}
}

func TestCustomProviderUsesAPIKeyHeaderWithoutBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/deployments/gpt-4o/chat/completions" {
			t.Fatalf("path = %q, want Azure chat completions path", r.URL.Path)
		}
		if r.URL.Query().Get("api-version") != "2024-10-21" {
			t.Fatalf("api-version = %q, want 2024-10-21", r.URL.Query().Get("api-version"))
		}
		if got := r.Header.Get("api-key"); got != "azure-secret" {
			t.Fatalf("api-key header = %q, want azure-secret", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization header = %q, want empty when api-key is configured", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResponse{
			ID:      "chat_1",
			Object:  "chat.completion",
			Created: 1,
			Model:   "gpt-4o",
			Choices: []Choice{{Message: Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
			Usage:   Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		})
	}))
	defer server.Close()

	p := NewCustom(
		"azure",
		server.URL+"/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21",
		"azure-secret",
		WithCustomHeaders(map[string]string{"api-key": "azure-secret"}),
	)
	resp, meta, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.ID != "chat_1" || meta.Provider != "azure" {
		t.Fatalf("response/meta = %+v/%+v, want successful azure response", resp, meta)
	}
}
