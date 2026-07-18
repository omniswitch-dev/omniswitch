package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

type rateLimitBackendFunc func(context.Context, string, int, time.Duration) (bool, time.Duration, error)

func (fn rateLimitBackendFunc) Allow(ctx context.Context, key string, limit int, interval time.Duration) (bool, time.Duration, error) {
	return fn(ctx, key, limit, interval)
}

type tokenAuthenticatorFunc func(context.Context, string) (Identity, error)

func (fn tokenAuthenticatorFunc) Authenticate(ctx context.Context, token string) (Identity, error) {
	return fn(ctx, token)
}

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
		gotKeyID = r.Header.Get("x-omniswitch-key-id")
		gotRateLimit = r.Header.Get("x-omniswitch-rate-limit")
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

func TestAuthMiddlewareDerivesOrganizationAndRejectsSpoofedHeader(t *testing.T) {
	st := newGatewayTestStore(t)
	if err := st.InsertWorkspace(context.Background(), store.Workspace{ID: "ws_1", OrganizationID: "org_verified", Name: "Production"}); err != nil {
		t.Fatalf("InsertWorkspace() error = %v", err)
	}
	rawKey := "sk-sentinel-org-test"
	hash := sha256.Sum256([]byte(rawKey))
	if err := st.InsertAPIKey(context.Background(), store.APIKey{
		ID: "key_org", Name: "workspace key", KeyHash: hex.EncodeToString(hash[:]), KeyPrefix: "sk-sentinel-...",
		WorkspaceID: "ws_1", CreatedAt: time.Now().UTC(), RateLimit: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("InsertAPIKey() error = %v", err)
	}

	var organizationID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		organizationID = r.Header.Get("x-omniswitch-organization-id")
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("x-omniswitch-organization-id", "org_spoofed")
	rec := httptest.NewRecorder()
	NewAuthMiddleware(st, true).Wrap(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || organizationID != "org_verified" {
		t.Fatalf("status/organization = %d/%q, want 204/org_verified", rec.Code, organizationID)
	}
}

func TestRateLimiterUsesHeaderOverride(t *testing.T) {
	limiter := NewRateLimiter(100, time.Minute)
	handler := limiter.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("x-omniswitch-key-id", "key_1")
		req.Header.Set("x-omniswitch-rate-limit", "1")
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

func TestRateLimiterUsesHostWithoutEphemeralPort(t *testing.T) {
	limiter := NewRateLimiter(1, time.Minute)
	handler := limiter.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for i, addr := range []string{"192.0.2.1:10000", "192.0.2.1:10001"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if want := []int{http.StatusNoContent, http.StatusTooManyRequests}[i]; rec.Code != want {
			t.Fatalf("request %d status = %d, want %d", i, rec.Code, want)
		}
	}
}

func TestRateLimiterBackendFailureIsFailClosedByDefault(t *testing.T) {
	backend := rateLimitBackendFunc(func(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
		return false, 0, errors.New("backend unavailable")
	})
	handler := NewRateLimiterWithBackend(10, time.Minute, backend, false).Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestRateLimiterBackendFailureCanFailOpen(t *testing.T) {
	backend := rateLimitBackendFunc(func(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
		return false, 0, errors.New("backend unavailable")
	})
	handler := NewRateLimiterWithBackend(10, time.Minute, backend, true).Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestAuthMiddlewareClearsSpoofedQuotaForExternalIdentity(t *testing.T) {
	st := newGatewayTestStore(t)
	auth := NewAuthMiddleware(st, true)
	auth.SetTokenAuthenticator(tokenAuthenticatorFunc(func(context.Context, string) (Identity, error) {
		return Identity{APIKeyID: "jwt:workload-1", Subject: "workload-1", Role: "member"}, nil
	}))
	var gotRateLimit, gotKeyID string
	handler := auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRateLimit = r.Header.Get("x-omniswitch-rate-limit")
		gotKeyID = r.Header.Get("x-omniswitch-key-id")
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer external-token")
	req.Header.Set("x-omniswitch-rate-limit", "999999")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent || gotRateLimit != "" || gotKeyID != "jwt:workload-1" {
		t.Fatalf("status/rate/key = %d/%q/%q, want 204/empty/jwt identity", recorder.Code, gotRateLimit, gotKeyID)
	}
}

func TestAuthMiddlewareAllowsPublicA2ADiscoveryOnly(t *testing.T) {
	st := newGatewayTestStore(t)
	handler := NewAuthMiddleware(st, true).Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("discovery status = %d, want %d", recorder.Code, http.StatusNoContent)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/a2a", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("A2A status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestAuthorizationMiddlewareRequiresAdminForKeyManagement(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := NewAuthorizationMiddleware(true).Wrap(next)

	for _, test := range []struct {
		name string
		role string
		want int
	}{
		{name: "viewer denied", role: "viewer", want: http.StatusForbidden},
		{name: "admin allowed", role: "admin", want: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
			req = req.WithContext(withIdentity(req.Context(), Identity{APIKeyID: "key_1", Role: test.role}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d", rec.Code, test.want)
			}
		})
	}
}

func TestAuthorizationPolicyRestrictsModelsAndRestoresBody(t *testing.T) {
	policy, err := NewAuthorizationPolicy([]AuthorizationRule{
		{Name: "member-models", Effect: "allow", When: `role == "member" && path == "/v1/chat/completions" && model == "allowed-model"`},
		{Name: "blocked-model", Effect: "deny", When: `model == "blocked-model"`, Message: "model is blocked"},
	})
	if err != nil {
		t.Fatalf("NewAuthorizationPolicy() error = %v", err)
	}

	var forwardedBody string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		forwardedBody = string(body)
		w.WriteHeader(http.StatusNoContent)
	})
	middleware := NewAuthorizationMiddleware(true)
	middleware.SetPolicy(policy)
	handler := middleware.Wrap(next)

	request := func(model string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[]}`))
		return req.WithContext(withIdentity(req.Context(), Identity{APIKeyID: "key_1", Role: "member"}))
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request("allowed-model"))
	if rec.Code != http.StatusNoContent || !strings.Contains(forwardedBody, `"allowed-model"`) {
		t.Fatalf("allowed request status/body = %d/%q, want forwarded allowed body", rec.Code, forwardedBody)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request("blocked-model"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "model is blocked") {
		t.Fatalf("blocked request status/body = %d/%q, want denied model", rec.Code, rec.Body.String())
	}
}

func TestAuthorizationPolicyUsesSubjectAndClaims(t *testing.T) {
	policy, err := NewAuthorizationPolicy([]AuthorizationRule{{
		Effect: "allow", When: `subject == "workload-1" && claims["department"] == "platform"`,
	}})
	if err != nil {
		t.Fatalf("NewAuthorizationPolicy() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	allowed, reason := policy.Evaluate(req, Identity{Subject: "workload-1", Claims: map[string]any{"department": "platform"}})
	if !allowed || reason != "" {
		t.Fatalf("Evaluate() = %v/%q, want allow", allowed, reason)
	}
}
