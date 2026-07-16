package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
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
