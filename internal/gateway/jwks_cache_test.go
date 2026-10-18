package gateway

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// switchableJWKS is a JWKS endpoint that can fail, block, or change keys.
type switchableJWKS struct {
	server *httptest.Server
	hits   atomic.Int32

	mu      sync.Mutex
	failing bool
	block   chan struct{} // when non-nil, requests wait for it to close
	body    []byte
}

func rsaJWK(t *testing.T, kid string, key *rsa.PrivateKey) map[string]string {
	t.Helper()
	return map[string]string{
		"kty": "RSA", "kid": kid,
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}
}

func newSwitchableJWKS(t *testing.T, kid string, key *rsa.PrivateKey) *switchableJWKS {
	t.Helper()
	s := &switchableJWKS{}
	s.setKey(t, kid, key)
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		failing, block, body := s.failing, s.block, s.body
		s.mu.Unlock()
		if block != nil {
			select {
			case <-block:
			case <-r.Context().Done():
				return
			}
		}
		if failing {
			http.Error(w, "idp down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *switchableJWKS) setKey(t *testing.T, kid string, key *rsa.PrivateKey) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"keys": []any{rsaJWK(t, kid, key)}})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.body = body
	s.mu.Unlock()
}

func (s *switchableJWKS) setFailing(failing bool) {
	s.mu.Lock()
	s.failing = failing
	s.mu.Unlock()
}

func (s *switchableJWKS) setBlock(block chan struct{}) {
	s.mu.Lock()
	s.block = block
	s.mu.Unlock()
}

func newTestAuthenticator(t *testing.T, url string, ttl, grace time.Duration) (*JWTAuthenticator, *fakeClock) {
	t.Helper()
	authenticator, err := NewJWTAuthenticator(JWTAuthenticatorConfig{JWKSURL: url, CacheTTL: ttl, StaleIfError: grace})
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	authenticator.now = clock.Now
	return authenticator, clock
}

// waitForFetch waits until no JWKS fetch is in flight.
func waitForFetch(authenticator *JWTAuthenticator) {
	authenticator.mu.Lock()
	call := authenticator.inflight
	authenticator.mu.Unlock()
	if call != nil {
		<-call.done
	}
}

func tokenWithKid(t *testing.T, key *rsa.PrivateKey, kid string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"sub": "workload", "exp": time.Now().Add(time.Hour).Unix()})
	token.Header["kid"] = kid
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustGenerateRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestJWKSBackoffDoublesAndCaps(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute}
	for i, expected := range want {
		if got := jwksBackoff(i + 1); got != expected {
			t.Errorf("jwksBackoff(%d) = %s, want %s", i+1, got, expected)
		}
	}
	if got := jwksBackoff(1000); got != time.Minute {
		t.Errorf("jwksBackoff(1000) = %s, want 1m", got)
	}
}

func TestJWKSFreshKeysNeedNoFetch(t *testing.T) {
	key := mustGenerateRSA(t)
	idp := newSwitchableJWKS(t, "a", key)
	authenticator, clock := newTestAuthenticator(t, idp.server.URL, 5*time.Minute, time.Hour)
	raw := tokenWithKid(t, key, "a")
	for range 20 {
		if _, err := authenticator.Authenticate(context.Background(), raw); err != nil {
			t.Fatalf("Authenticate() error = %v", err)
		}
		clock.Advance(10 * time.Second)
	}
	waitForFetch(authenticator)
	if got := idp.hits.Load(); got != 1 {
		t.Fatalf("JWKS fetches = %d, want 1", got)
	}
}

func TestJWKSStaleKeysServeDuringOutageWithBackoff(t *testing.T) {
	key := mustGenerateRSA(t)
	idp := newSwitchableJWKS(t, "a", key)
	authenticator, clock := newTestAuthenticator(t, idp.server.URL, 5*time.Minute, time.Hour)
	raw := tokenWithKid(t, key, "a")
	if _, err := authenticator.Authenticate(context.Background(), raw); err != nil {
		t.Fatalf("initial Authenticate() error = %v", err)
	}

	idp.setFailing(true)
	clock.Advance(5*time.Minute + time.Second)
	// Step one second at a time for 200s with a request per step. Fetches
	// must follow the backoff: after failures at t=0,1,3,7,15,31,63 the
	// delay caps at 1m, so the next ones are at t=123 and t=183.
	wantFetchAt := map[int]bool{0: true, 1: true, 3: true, 7: true, 15: true, 31: true, 63: true, 123: true, 183: true}
	for step := 0; step <= 200; step++ {
		before := idp.hits.Load()
		if _, err := authenticator.Authenticate(context.Background(), raw); err != nil {
			t.Fatalf("step %d: Authenticate() with stale keys error = %v", step, err)
		}
		waitForFetch(authenticator)
		fetched := idp.hits.Load() - before
		if wantFetchAt[step] && fetched != 1 {
			t.Fatalf("step %d: fetches = %d, want 1", step, fetched)
		}
		if !wantFetchAt[step] && fetched != 0 {
			t.Fatalf("step %d: fetches = %d, want 0 during backoff", step, fetched)
		}
		clock.Advance(time.Second)
	}
	if got, want := idp.hits.Load(), int32(1+len(wantFetchAt)); got != want {
		t.Fatalf("total JWKS fetches = %d, want %d", got, want)
	}
}

func TestJWKSGraceEndsThenRecoveryReplacesKeysAndResetsBackoff(t *testing.T) {
	oldKey, newKey := mustGenerateRSA(t), mustGenerateRSA(t)
	idp := newSwitchableJWKS(t, "old", oldKey)
	authenticator, clock := newTestAuthenticator(t, idp.server.URL, 5*time.Minute, 10*time.Minute)
	oldToken, newToken := tokenWithKid(t, oldKey, "old"), tokenWithKid(t, newKey, "new")
	if _, err := authenticator.Authenticate(context.Background(), oldToken); err != nil {
		t.Fatalf("initial Authenticate() error = %v", err)
	}

	idp.setFailing(true)
	clock.Advance(6 * time.Minute)
	if _, err := authenticator.Authenticate(context.Background(), oldToken); err != nil {
		t.Fatalf("Authenticate() within grace error = %v", err)
	}
	waitForFetch(authenticator)

	// Past cache_ttl + stale_if_error the keys are dropped.
	clock.Advance(10 * time.Minute)
	if _, err := authenticator.Authenticate(context.Background(), oldToken); err == nil {
		t.Fatal("Authenticate() after grace succeeded, want error")
	}
	// During the backoff the next request fails at once without a fetch.
	hits := idp.hits.Load()
	if _, err := authenticator.Authenticate(context.Background(), oldToken); err == nil {
		t.Fatal("Authenticate() during backoff succeeded, want last fetch error")
	}
	if got := idp.hits.Load(); got != hits {
		t.Fatalf("JWKS fetches during backoff = %d, want %d", got, hits)
	}

	// The IdP recovers with a rotated key set.
	idp.setFailing(false)
	idp.setKey(t, "new", newKey)
	clock.Advance(time.Minute)
	if _, err := authenticator.Authenticate(context.Background(), newToken); err != nil {
		t.Fatalf("Authenticate() after recovery error = %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), oldToken); err == nil {
		t.Fatal("token signed with a key the IdP removed is still accepted")
	}
	authenticator.mu.Lock()
	failures, retryAt := authenticator.failures, authenticator.retryAt
	authenticator.mu.Unlock()
	if failures != 0 || !retryAt.IsZero() {
		t.Fatalf("after recovery failures=%d retryAt=%s, want reset", failures, retryAt)
	}

	// A new failure streak starts again at the initial backoff.
	idp.setFailing(true)
	clock.Advance(6 * time.Minute)
	if _, err := authenticator.Authenticate(context.Background(), newToken); err != nil {
		t.Fatalf("Authenticate() with stale keys error = %v", err)
	}
	waitForFetch(authenticator)
	authenticator.mu.Lock()
	failures, retryAt = authenticator.failures, authenticator.retryAt
	authenticator.mu.Unlock()
	if want := clock.Now().Add(jwksInitialBackoff); failures != 1 || !retryAt.Equal(want) {
		t.Fatalf("new streak failures=%d retryAt=%s, want 1 and %s", failures, retryAt, want)
	}
}

func TestJWKSSuccessfulBackgroundRefreshReplacesKeys(t *testing.T) {
	oldKey, newKey := mustGenerateRSA(t), mustGenerateRSA(t)
	idp := newSwitchableJWKS(t, "old", oldKey)
	authenticator, clock := newTestAuthenticator(t, idp.server.URL, 5*time.Minute, time.Hour)
	oldToken := tokenWithKid(t, oldKey, "old")
	if _, err := authenticator.Authenticate(context.Background(), oldToken); err != nil {
		t.Fatal(err)
	}
	idp.setKey(t, "new", newKey)
	clock.Advance(6 * time.Minute)
	// This request still uses the cached keys and starts the refresh.
	if _, err := authenticator.Authenticate(context.Background(), oldToken); err != nil {
		t.Fatalf("Authenticate() with expired keys error = %v", err)
	}
	waitForFetch(authenticator)
	clock.Advance(time.Minute) // past minForcedRefreshInterval
	if _, err := authenticator.Authenticate(context.Background(), oldToken); err == nil {
		t.Fatal("removed key still accepted after a successful refresh")
	}
	if _, err := authenticator.Authenticate(context.Background(), tokenWithKid(t, newKey, "new")); err != nil {
		t.Fatalf("new key rejected: %v", err)
	}
}

func TestJWKSSlowEndpointDoesNotDelayStaleRequests(t *testing.T) {
	key := mustGenerateRSA(t)
	idp := newSwitchableJWKS(t, "a", key)
	authenticator, clock := newTestAuthenticator(t, idp.server.URL, 5*time.Minute, time.Hour)
	raw := tokenWithKid(t, key, "a")
	if _, err := authenticator.Authenticate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	idp.setBlock(release)
	clock.Advance(6 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	for range 10 {
		if _, err := authenticator.Authenticate(ctx, raw); err != nil {
			t.Fatalf("Authenticate() with stale keys error = %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("requests with stale keys took %s, want no wait for the IdP", elapsed)
	}
	// Cancelling the request that started the refresh must not cancel it.
	cancel()
	time.Sleep(20 * time.Millisecond)
	close(release)
	waitForFetch(authenticator)
	if got := idp.hits.Load(); got != 2 {
		t.Fatalf("JWKS fetches = %d, want 2 (one background refresh)", got)
	}
	authenticator.mu.Lock()
	fetchedAt, failures := authenticator.fetchedAt, authenticator.failures
	authenticator.mu.Unlock()
	if failures != 0 || !fetchedAt.Equal(clock.Now()) {
		t.Fatalf("background refresh failures=%d fetchedAt=%s, want success at %s", failures, fetchedAt, clock.Now())
	}
}

func TestJWKSUnknownKidDuringBackoffDoesNotFetch(t *testing.T) {
	key := mustGenerateRSA(t)
	idp := newSwitchableJWKS(t, "a", key)
	authenticator, clock := newTestAuthenticator(t, idp.server.URL, 5*time.Minute, time.Hour)
	if _, err := authenticator.Authenticate(context.Background(), tokenWithKid(t, key, "a")); err != nil {
		t.Fatal(err)
	}
	idp.setFailing(true)
	clock.Advance(6 * time.Minute)
	if _, err := authenticator.Authenticate(context.Background(), tokenWithKid(t, key, "a")); err != nil {
		t.Fatal(err)
	}
	waitForFetch(authenticator)
	hits := idp.hits.Load()
	for range 10 {
		if _, err := authenticator.Authenticate(context.Background(), tokenWithKid(t, key, "unknown")); err == nil {
			t.Fatal("unknown kid accepted")
		}
	}
	waitForFetch(authenticator)
	if got := idp.hits.Load(); got != hits {
		t.Fatalf("JWKS fetches for unknown kids during backoff = %d, want %d", got, hits)
	}
}

func TestJWKSZeroGraceFailsClosed(t *testing.T) {
	key := mustGenerateRSA(t)
	idp := newSwitchableJWKS(t, "a", key)
	authenticator, clock := newTestAuthenticator(t, idp.server.URL, 5*time.Minute, 0)
	raw := tokenWithKid(t, key, "a")
	if _, err := authenticator.Authenticate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	idp.setFailing(true)
	clock.Advance(5 * time.Minute)
	if _, err := authenticator.Authenticate(context.Background(), raw); err == nil {
		t.Fatal("Authenticate() with expired keys and grace 0 succeeded, want error")
	}
	if got := idp.hits.Load(); got != 2 {
		t.Fatalf("JWKS fetches = %d, want 2 (the request waited for the refresh)", got)
	}
	// Recovery after the backoff works again.
	idp.setFailing(false)
	clock.Advance(jwksInitialBackoff)
	if _, err := authenticator.Authenticate(context.Background(), raw); err != nil {
		t.Fatalf("Authenticate() after recovery error = %v", err)
	}
}

func TestJWKSFirstFetchFailureBacksOff(t *testing.T) {
	key := mustGenerateRSA(t)
	idp := newSwitchableJWKS(t, "a", key)
	idp.setFailing(true)
	authenticator, clock := newTestAuthenticator(t, idp.server.URL, 5*time.Minute, time.Hour)
	raw := tokenWithKid(t, key, "a")
	for range 5 {
		if _, err := authenticator.Authenticate(context.Background(), raw); err == nil {
			t.Fatal("Authenticate() without keys succeeded")
		}
	}
	if got := idp.hits.Load(); got != 1 {
		t.Fatalf("JWKS fetches = %d, want 1 during backoff", got)
	}
	idp.setFailing(false)
	clock.Advance(jwksInitialBackoff)
	if _, err := authenticator.Authenticate(context.Background(), raw); err != nil {
		t.Fatalf("Authenticate() after backoff error = %v", err)
	}
}

func TestJWKSConcurrentRequestsDuringRefresh(t *testing.T) {
	key := mustGenerateRSA(t)
	idp := newSwitchableJWKS(t, "a", key)
	authenticator, clock := newTestAuthenticator(t, idp.server.URL, 5*time.Minute, time.Hour)
	raw := tokenWithKid(t, key, "a")
	if _, err := authenticator.Authenticate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var failures atomic.Int32
	for worker := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				if worker == 0 && i%10 == 0 {
					idp.setFailing(i%20 == 0)
					clock.Advance(3 * time.Minute)
				}
				if _, err := authenticator.Authenticate(context.Background(), raw); err != nil {
					failures.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	waitForFetch(authenticator)
	if got := failures.Load(); got != 0 {
		t.Fatalf("%d requests failed, want 0 (stale keys within the grace)", got)
	}
	// At most one fetch per clock step: 5 steps plus the initial fetch.
	if got := idp.hits.Load(); got > 6 {
		t.Fatalf("JWKS fetches = %d, want at most 6", got)
	}
}
