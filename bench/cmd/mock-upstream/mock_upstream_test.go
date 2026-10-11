package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSeededTokensDeterministic(t *testing.T) {
	seed := requestSeed("bench-model", []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	a := seededTokens(seed, 16)
	b := seededTokens(seed, 16)
	if a != b {
		t.Fatalf("seededTokens not deterministic for identical seed: %q != %q", a, b)
	}

	other := requestSeed("bench-model", []byte(`{"messages":[{"role":"user","content":"bye"}]}`))
	c := seededTokens(other, 16)
	if a == c {
		t.Fatalf("seededTokens produced identical output for different seeds")
	}
}

func TestHealthz(t *testing.T) {
	srv := newServer(defaultConfig())
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.handleHealthz(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestChatCompletionsNonStream(t *testing.T) {
	srv := newServer(defaultConfig())
	body := `{"model":"bench-model","stream":false,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	srv.handleChatCompletions(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp["object"] != "chat.completion" {
		t.Fatalf("unexpected object field: %v", resp["object"])
	}
	choices, ok := resp["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("expected exactly one choice, got %v", resp["choices"])
	}

	snap := srv.stats.snapshot()
	if snap.TotalRequests != 1 {
		t.Fatalf("expected 1 recorded request, got %d", snap.TotalRequests)
	}
	if snap.ByAPIKey["test-key"] != 1 {
		t.Fatalf("expected api-key counter to be 1, got %d", snap.ByAPIKey["test-key"])
	}
}

func TestChatCompletionsFaultHeaderOverride(t *testing.T) {
	srv := newServer(defaultConfig())
	body := `{"model":"bench-model","stream":false,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Bench-Fault", "429")
	rec := httptest.NewRecorder()
	srv.handleChatCompletions(rec, req)
	if rec.Code != 429 {
		t.Fatalf("expected 429, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatalf("expected Retry-After header on injected 429")
	}
}

func TestEmbeddings(t *testing.T) {
	srv := newServer(defaultConfig())
	body := `{"model":"bench-embed","input":["hello","world"]}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleEmbeddings(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(resp.Data))
	}
	if len(resp.Data[0].Embedding) != embeddingDims {
		t.Fatalf("expected %d dims, got %d", embeddingDims, len(resp.Data[0].Embedding))
	}
}
