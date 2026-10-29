package gateway

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newCountingJWKSServer(t *testing.T, key *rsa.PrivateKey, delay time.Duration) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	jwk, err := json.Marshal(map[string]string{
		"kty": "RSA", "kid": "test-key",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	})
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		time.Sleep(delay)
		_, _ = w.Write([]byte(`{"keys":[` + string(jwk) + `]}`))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestJWTAuthenticatorBoundsUnknownKidRefreshes(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server, hits := newCountingJWKSServer(t, key, 0)
	authenticator, err := NewJWTAuthenticator(JWTAuthenticatorConfig{JWKSURL: server.URL, CacheTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"sub": "x", "exp": time.Now().Add(time.Minute).Unix()})
		token.Header["kid"] = fmt.Sprintf("random-%d", i)
		raw, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := authenticator.Authenticate(context.Background(), raw); err == nil {
			t.Fatal("unknown kid accepted")
		}
	}
	// One initial fetch plus at most one forced refresh in the interval.
	if got := hits.Load(); got > 2 {
		t.Fatalf("JWKS fetches = %d, want at most 2", got)
	}
	// A valid token still verifies from the cache.
	if _, err := authenticator.Authenticate(context.Background(), signedJWT(t, key, jwt.MapClaims{"sub": "ok", "exp": time.Now().Add(time.Minute).Unix()})); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
}

func TestJWTAuthenticatorSharesConcurrentFetch(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server, hits := newCountingJWKSServer(t, key, 100*time.Millisecond)
	authenticator, err := NewJWTAuthenticator(JWTAuthenticatorConfig{JWKSURL: server.URL, CacheTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	raw := signedJWT(t, key, jwt.MapClaims{"sub": "ok", "exp": time.Now().Add(time.Minute).Unix()})
	var wg sync.WaitGroup
	var failures atomic.Int32
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := authenticator.Authenticate(context.Background(), raw); err != nil {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d concurrent authentications failed", failures.Load())
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("JWKS fetches = %d, want 1", got)
	}
}

func TestJWTAuthenticatorRequiresExpiration(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := newCountingJWKSServer(t, key, 0)
	authenticator, err := NewJWTAuthenticator(JWTAuthenticatorConfig{JWKSURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authenticator.Authenticate(context.Background(), signedJWT(t, key, jwt.MapClaims{"sub": "svc", "role": "owner"})); err == nil {
		t.Fatal("token without exp accepted")
	}
	// Within leeway: expired 10s ago is still accepted.
	if _, err := authenticator.Authenticate(context.Background(), signedJWT(t, key, jwt.MapClaims{"sub": "svc", "exp": time.Now().Add(-10 * time.Second).Unix()})); err != nil {
		t.Fatalf("token inside leeway rejected: %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), signedJWT(t, key, jwt.MapClaims{"sub": "svc", "exp": time.Now().Add(-2 * time.Minute).Unix()})); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestJWTConfigMissingBindingWarnings(t *testing.T) {
	if got := (JWTAuthenticatorConfig{}).MissingBindingWarnings(); len(got) != 2 {
		t.Fatalf("warnings = %v, want 2", got)
	}
	if got := (JWTAuthenticatorConfig{Issuer: "i", Audience: "a"}).MissingBindingWarnings(); len(got) != 0 {
		t.Fatalf("warnings = %v, want none", got)
	}
}
