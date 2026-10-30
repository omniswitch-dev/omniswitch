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

func TestAnthropicSDKKeyUsesNormalIdentityAndPermissions(t *testing.T) {
	st := newGatewayTestStore(t)
	const key = "sk-anthropic-gateway-test"
	hash := sha256.Sum256([]byte(key))
	if err := st.InsertAPIKey(context.Background(), store.APIKey{ID: "anthropic-key", Name: "app", KeyHash: hex.EncodeToString(hash[:]), Enabled: true, Role: "member", RateLimit: 10, AllowedModels: []string{"permitted"}, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		identity, _ := IdentityFromContext(r.Context())
		if identity.APIKeyID != "anthropic-key" || identity.Role != "member" || identity.RateLimit != 10 || len(identity.AllowedModels) != 1 || identity.AllowedModels[0] != "permitted" {
			t.Errorf("wrong identity: %+v", identity)
		}
		w.WriteHeader(204)
	})
	for _, tc := range []struct {
		path, bearer, apiKey string
		want                 int
	}{
		{"/v1/messages", "", key, 204},
		{"/v1/messages", "Bearer " + key, "invalid", 204},
		{"/v1/messages", "Bearer invalid", key, 401},
		{"/v1/messages", "Basic invalid", key, 401},
		{"/v1/messages", "", "invalid", 401},
		{"/v1/chat/completions", "", key, 401},
		{"/api/keys", "", key, 401},
	} {
		called = false
		req := httptest.NewRequest(http.MethodPost, tc.path, nil)
		req.Header.Set("Authorization", tc.bearer)
		req.Header.Set("x-api-key", tc.apiKey)
		req.Header.Set("x-omniswitch-role", "owner")
		rec := httptest.NewRecorder()
		NewAuthMiddleware(st, true).Wrap(next).ServeHTTP(rec, req)
		if rec.Code != tc.want || called != (tc.want == 204) {
			t.Fatalf("%+v = %d/%v", tc, rec.Code, called)
		}
	}
}

func TestAnthropicSDKKeyRejectsDisabledAndExpiredKeys(t *testing.T) {
	for _, expired := range []bool{false, true} {
		st := newGatewayTestStore(t)
		key := "sk-unusable"
		hash := sha256.Sum256([]byte(key))
		record := store.APIKey{ID: "key", Name: "app", KeyHash: hex.EncodeToString(hash[:]), CreatedAt: time.Now(), Enabled: expired}
		if expired {
			past := time.Now().Add(-time.Hour)
			record.ExpiresAt = &past
		}
		if err := st.InsertAPIKey(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("x-api-key", key)
		rec := httptest.NewRecorder()
		NewAuthMiddleware(st, true).Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unusable key accepted") })).ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Fatalf("expired=%v: status=%d", expired, rec.Code)
		}
	}
}
