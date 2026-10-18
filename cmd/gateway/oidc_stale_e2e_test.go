package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/omniswitch-dev/omniswitch/internal/admin"
	"github.com/omniswitch-dev/omniswitch/internal/dashboard"
	"github.com/omniswitch-dev/omniswitch/internal/eval"
	"github.com/omniswitch-dev/omniswitch/internal/feedback"
	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/org"
	"github.com/omniswitch-dev/omniswitch/internal/prompt"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/vault"
)

// TestOIDCRequestsSurviveJWKSOutageAfterCacheTTL runs OIDC requests through
// the real middleware chain while the JWKS endpoint fails after its first
// response: once the cache TTL has passed, requests keep getting 200 from
// the cached keys instead of 401.
func TestOIDCRequestsSurviveJWKSOutageAfterCacheTTL(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwk, err := json.Marshal(map[string]any{"keys": []any{map[string]string{
		"kty": "RSA", "kid": "e2e",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) > 1 {
			http.Error(w, "idp down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(jwk)
	}))
	defer idp.Close()

	const cacheTTL = 50 * time.Millisecond
	st := newTestStore(t)
	settings := runtimeSettings{
		authEnabled:       true,
		rateLimitRequests: 1000,
		rateLimitWindow:   time.Minute,
		oidcJWKSURL:       idp.URL,
		oidcCacheTTL:      cacheTTL,
		oidcStaleIfError:  time.Hour,
	}
	registry := provider.NewRegistry()
	registry.Register(fakeHealthProvider{name: "fake"})
	gw := gateway.New(registry, router.New(registry), st, guardrail.NewEngine())
	mux := newGatewayMux(routeHandlers{
		settings: settings,
		registry: registry,
		gateway:  gw,
		admin:    admin.New(st),
		feedback: feedback.New(st),
		prompt:   prompt.New(st),
		org:      org.New(st),
		eval:     eval.NewHandler(st),
		vault:    vault.NewHandler(vault.New(st, "")),
		dash:     dashboard.New(st),
	})
	handler, closeRateLimiter, err := wrapGatewayMiddleware(settings, st, mux)
	if err != nil {
		t.Fatalf("wrapGatewayMiddleware() error = %v", err)
	}
	t.Cleanup(closeRateLimiter)
	server := newTopMux(newHealthState(st, registry, false), handler)

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"sub": "e2e-workload", "role": "member", "exp": time.Now().Add(time.Hour).Unix()})
	token.Header["kid"] = "e2e"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	get := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}

	if rec := get(); rec.Code != http.StatusOK {
		t.Fatalf("first OIDC request = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	time.Sleep(2 * cacheTTL)
	// The first request after the TTL starts a refresh that fails; this and
	// the following requests must keep succeeding from the cached keys.
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() < 2 {
		if rec := get(); rec.Code != http.StatusOK {
			t.Fatalf("OIDC request during JWKS outage = %d, want 200; body %s", rec.Code, rec.Body.String())
		}
		if time.Now().After(deadline) {
			t.Fatal("JWKS refresh was never attempted after the cache TTL")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// After the failed refresh, requests keep working for several TTLs, and
	// the 1s backoff keeps them from refetching.
	for range 20 {
		time.Sleep(10 * time.Millisecond)
		if rec := get(); rec.Code != http.StatusOK {
			t.Fatalf("OIDC request after failed refresh = %d, want 200; body %s", rec.Code, rec.Body.String())
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("JWKS fetches = %d, want 2 (backoff after the failure)", got)
	}
}
