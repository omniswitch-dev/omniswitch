package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func spoofedRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	for _, name := range internalIdentityHeaders {
		req.Header.Set(name, "spoofed")
	}
	return req
}

func TestAuthDisabledStripsInternalIdentityHeaders(t *testing.T) {
	var seen http.Header
	handler := NewAuthMiddleware(nil, false).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
	}))
	handler.ServeHTTP(httptest.NewRecorder(), spoofedRequest())
	for _, name := range internalIdentityHeaders {
		if value := seen.Get(name); value != "" {
			t.Errorf("header %s = %q, want stripped", name, value)
		}
	}
}

func TestStripInternalIdentityHeadersKeepsOtherHeaders(t *testing.T) {
	var seen http.Header
	handler := StripInternalIdentityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
	}))
	req := spoofedRequest()
	req.Header.Set("x-omniswitch-trace-id", "trace-1")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if seen.Get("x-omniswitch-role") != "" || seen.Get("x-omniswitch-trace-id") != "trace-1" {
		t.Fatalf("headers = %v", seen)
	}
}

func TestAuthEnabledReplacesSpoofedUserID(t *testing.T) {
	auth := NewAuthMiddleware(newGatewayTestStore(t), true)
	auth.SetTokenAuthenticator(tokenAuthenticatorFunc(func(context.Context, string) (Identity, error) {
		return Identity{APIKeyID: "jwt:alice", Subject: "alice", Role: "member", AuthMethod: "oidc"}, nil
	}))
	var userID, workspace string
	handler := auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID = r.Header.Get("x-omniswitch-user-id")
		workspace = r.Header.Get("x-omniswitch-workspace-id")
	}))
	req := spoofedRequest()
	req.Header.Set("Authorization", "Bearer token")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if userID != "alice" || workspace != "" {
		t.Fatalf("user/workspace = %q/%q, want alice/empty", userID, workspace)
	}
}

func TestLocalRateLimitBackendEvictsIdleKeys(t *testing.T) {
	backend := newLocalRateLimitBackend()
	now := time.Unix(1_700_000_000, 0)
	backend.now = func() time.Time { return now }
	for i := range 1000 {
		if allowed, _, _ := backend.Allow(context.Background(), fmt.Sprintf("key-%d", i), 5, time.Minute); !allowed {
			t.Fatalf("request %d unexpectedly limited", i)
		}
	}
	if got := backend.size(); got != 1000 {
		t.Fatalf("size = %d, want 1000", got)
	}
	now = now.Add(2 * time.Minute)
	if allowed, _, _ := backend.Allow(context.Background(), "fresh", 5, time.Minute); !allowed {
		t.Fatal("fresh key limited")
	}
	if got := backend.size(); got != 1 {
		t.Fatalf("size after idle sweep = %d, want 1", got)
	}
	// Limits still apply to active keys.
	for range 4 {
		backend.Allow(context.Background(), "fresh", 5, time.Minute)
	}
	if allowed, retry, _ := backend.Allow(context.Background(), "fresh", 5, time.Minute); allowed || retry != time.Minute {
		t.Fatalf("allowed/retry = %v/%v, want false/1m", allowed, retry)
	}
}
