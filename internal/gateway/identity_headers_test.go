package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestControlPlaneBodyLimit(t *testing.T) {
	var readErr error
	handler := ControlPlaneBodyLimit(16, 64, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	cases := []struct {
		path      string
		size      int
		chunked   bool
		wantCode  int
		wantError bool
	}{
		{"/api/feedback", 16, false, http.StatusNoContent, false},
		{"/api/feedback", 17, false, http.StatusRequestEntityTooLarge, false},
		{"/api/feedback", 17, true, http.StatusNoContent, true},
		{"/api/configs", 64, false, http.StatusNoContent, false},
		{"/api/config/raw", 65, false, http.StatusRequestEntityTooLarge, false},
		{"/v1/chat/completions", 1000, false, http.StatusNoContent, false},
	}
	for _, tc := range cases {
		readErr = nil
		var body io.Reader = strings.NewReader(strings.Repeat("x", tc.size))
		if tc.chunked {
			body = io.MultiReader(body) // hides the length
		}
		req := httptest.NewRequest(http.MethodPost, tc.path, body)
		if tc.chunked {
			req.ContentLength = -1
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.wantCode || (readErr != nil) != tc.wantError {
			t.Errorf("%s size %d chunked %v: code %d err %v", tc.path, tc.size, tc.chunked, rec.Code, readErr)
		}
	}
}

func TestControlPlaneBodyLimitStreamedBodyGets413(t *testing.T) {
	// The handler maps a decode error to 400, like the real control-plane
	// handlers do.
	handler := ControlPlaneBodyLimit(16, 64, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("decode: " + err.Error()))
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	for _, tc := range []struct {
		name     string
		body     string
		wantCode int
		wantBody string
	}{
		{"over the limit", `{"name":"` + strings.Repeat("x", 32) + `"}`, http.StatusRequestEntityTooLarge, `"request body too large"`},
		{"invalid within the limit", `{"name":`, http.StatusBadRequest, "decode: unexpected EOF"},
		{"valid within the limit", `{"name":"a"}`, http.StatusCreated, ""},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/keys", io.MultiReader(strings.NewReader(tc.body)))
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Errorf("%s: code %d body %q, want %d with %q", tc.name, rec.Code, rec.Body.String(), tc.wantCode, tc.wantBody)
		}
		if tc.wantCode == http.StatusRequestEntityTooLarge {
			if strings.Contains(rec.Body.String(), "decode:") || rec.Header().Get("Content-Type") != "application/json" {
				t.Errorf("%s: body %q content type %q, want only the JSON 413 error", tc.name, rec.Body.String(), rec.Header().Get("Content-Type"))
			}
		}
	}
}
