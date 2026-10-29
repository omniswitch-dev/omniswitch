package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExternalAuthorizationPreservesBodyAndMinimizesPayload(t *testing.T) {
	const body = `{"model":"allowed","messages":[{"role":"user","content":"private prompt"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected authorization call: %s, %v", r.Method, r.Header)
		}
		for _, secret := range []string{"private prompt", "private-token", "private-query", "secret-claim"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("authorization payload contains %q", secret)
			}
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil || payload["model"] != "allowed" || payload["path"] != "/v1/chat/completions" {
			t.Errorf("unexpected payload: %s", data)
		}
		_, _ = io.WriteString(w, `{"allowed":true}`)
	}))
	defer server.Close()
	external, err := NewExternalAuthorizer(server.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?secret=private-query", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer private-token")
	allowed, _, err := external.Evaluate(req, Identity{APIKeyID: "key_1", Role: "member", Claims: map[string]any{"secret": "secret-claim"}})
	if !allowed || err != nil {
		t.Fatalf("allowed/error = %v/%v", allowed, err)
	}
	restored, _ := io.ReadAll(req.Body)
	if string(restored) != body {
		t.Fatalf("body changed: %s", restored)
	}
}

func TestExternalAuthorizationFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantStatus int
	}{
		{"allow", 200, `{"allowed":true}`, 204},
		{"deny", 200, `{"allowed":false,"message":"policy denied"}`, 403},
		{"missing", 200, `{}`, 503},
		{"null", 200, `{"allowed":null}`, 503},
		{"wrong-type", 200, `{"allowed":"true"}`, 503},
		{"malformed", 200, `broken`, 503},
		{"trailing-data", 200, `{"allowed":true}{}`, 503},
		{"failure", 500, `{"allowed":true}`, 503},
		{"redirect", 302, `{"allowed":true}`, 503},
		{"oversized", 200, `{"allowed":true,"message":"` + strings.Repeat("x", 65536) + `"}`, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			external, err := NewExternalAuthorizer(server.URL, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			auth := NewAuthorizationMiddleware(true)
			auth.SetExternalAuthorizer(external)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"allowed"}`))
			req = req.WithContext(withIdentity(req.Context(), Identity{Role: "member"}))
			rec := httptest.NewRecorder()
			auth.Wrap(next).ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status/body = %d/%s", rec.Code, rec.Body)
			}
		})
	}
}

func TestExternalAuthorizationHonorsCancellationAndTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-release
	}))
	defer func() {
		close(release)
		server.Close()
	}()
	for _, canceled := range []bool{false, true} {
		external, err := NewExternalAuthorizer(server.URL, 20*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		if canceled {
			cancel()
		}
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil).WithContext(ctx)
		allowed, _, err := external.Evaluate(req, Identity{})
		cancel()
		if allowed || err == nil {
			t.Fatalf("canceled=%v allowed/error = %v/%v", canceled, allowed, err)
		}
	}
}

func TestExternalAuthorizationCannotOverrideLocalPolicy(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, `{"allowed":true}`)
	}))
	defer server.Close()
	external, err := NewExternalAuthorizer(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/keys", "/v1/chat/completions"} {
		auth := NewAuthorizationMiddleware(true)
		auth.SetExternalAuthorizer(external)
		if path == "/v1/chat/completions" {
			policy, err := NewAuthorizationPolicy([]AuthorizationRule{{When: "true", Effect: "deny"}})
			if err != nil {
				t.Fatal(err)
			}
			auth.SetPolicy(policy)
		}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"allowed"}`))
		req = req.WithContext(withIdentity(req.Context(), Identity{Role: "viewer"}))
		rec := httptest.NewRecorder()
		auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("handler reached") })).ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || called {
			t.Fatalf("status/called = %d/%v", rec.Code, called)
		}
	}
}
