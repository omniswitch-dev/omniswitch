package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sentinel/internal/store"
)

func TestAuthMiddlewareSetsKeyHeaders(t *testing.T) {
	st := newGatewayTestStore(t)
	rawKey := "sk-sentinel-auth-test"
	hash := sha256.Sum256([]byte(rawKey))
	if err := st.InsertAPIKey(context.Background(), store.APIKey{
		ID:        "key_1",
		Name:      "local",
		KeyHash:   hex.EncodeToString(hash[:]),
		KeyPrefix: "sk-sentinel-...",
		CreatedAt: time.Now().UTC(),
		RateLimit: 2,
		Enabled:   true,
	}); err != nil {
		t.Fatalf("InsertAPIKey() error = %v", err)
	}

	var gotKeyID, gotRateLimit string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKeyID = r.Header.Get("x-sentinel-key-id")
		gotRateLimit = r.Header.Get("x-sentinel-rate-limit")
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	NewAuthMiddleware(st, true).Wrap(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if gotKeyID != "key_1" || gotRateLimit != "2" {
		t.Fatalf("headers key/rate = %q/%q, want key_1/2", gotKeyID, gotRateLimit)
	}
}

func TestRateLimiterUsesHeaderOverride(t *testing.T) {
	limiter := NewRateLimiter(100, time.Minute)
	handler := limiter.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("x-sentinel-key-id", "key_1")
		req.Header.Set("x-sentinel-rate-limit", "1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if i == 0 && rec.Code != http.StatusNoContent {
			t.Fatalf("first status = %d, want 204", rec.Code)
		}
		if i == 1 && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second status = %d, want 429", rec.Code)
		}
	}
}
