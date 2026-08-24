package guardrail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookMaxRetriesOnServerError(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 { // fail twice with 500, then succeed
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"triggered":false}`))
	}))
	defer server.Close()

	check := webhookCheck{
		config: Webhook{Name: "retrying", URL: server.URL, Stage: "input", MaxRetries: 3},
		client: &http.Client{Timeout: 5 * time.Second},
	}
	result, err := check.Evaluate(context.Background(), GuardrailInput{IsInput: true, Response: "hello"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
	if result.Triggered {
		t.Fatalf("unexpected trigger: %+v", result)
	}

	// Exhausted retries surface the last error.
	exhausted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer exhausted.Close()
	failing := webhookCheck{
		config: Webhook{Name: "failing", URL: exhausted.URL, Stage: "input", MaxRetries: 2},
		client: &http.Client{Timeout: 5 * time.Second},
	}
	start := time.Now()
	if _, err := failing.Evaluate(context.Background(), GuardrailInput{IsInput: true}); err == nil {
		t.Fatalf("expected error after exhausting retries")
	}
	if elapsed := time.Since(start); elapsed < 2*250*time.Millisecond {
		t.Fatalf("expected backoff between retries, took %s", elapsed)
	}
}
