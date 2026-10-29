package gateway

import (
	"context"
	"log"
	"time"
)

// JWKS refresh backoff: after a failed fetch no new fetch starts for
// jwksInitialBackoff, doubling per consecutive failure up to jwksMaxBackoff.
// The cap keeps a failing IdP at about one request per minute per gateway
// instance while still noticing recovery within a minute, well inside the
// default 5m cache TTL.
const (
	jwksInitialBackoff = time.Second
	jwksMaxBackoff     = time.Minute
)

// jwksFetchTimeout bounds a fetch when the configured HTTP client has no
// timeout of its own. Fetches do not use the request context's cancellation,
// so something else must bound them.
const jwksFetchTimeout = 30 * time.Second

// jwksFetch is one in-flight JWKS request shared by every caller that needs
// fresh keys at the same time.
type jwksFetch struct {
	done chan struct{}
	keys map[string]any
	err  error
}

// jwksBackoff returns the delay after the given number of consecutive
// failures (failures >= 1).
func jwksBackoff(failures int) time.Duration {
	delay := jwksInitialBackoff
	for i := 1; i < failures && delay < jwksMaxBackoff; i++ {
		delay *= 2
	}
	return min(delay, jwksMaxBackoff)
}

// currentKeys returns the key set to verify a token with.
//
//   - Fresh keys (younger than CacheTTL) are returned as is.
//   - Expired keys within the StaleIfError grace are returned at once and
//     one background refresh starts, unless a refresh is in flight or a
//     failed fetch is backing off. The request never waits for the IdP.
//   - Without usable keys (first fetch, or grace over) the caller waits for
//     a shared fetch, or fails at once with the last error during backoff.
//   - forceRefresh (a token with an unknown kid) waits for a shared fetch,
//     at most once per minForcedRefreshInterval and never during backoff.
func (authenticator *JWTAuthenticator) currentKeys(ctx context.Context, forceRefresh bool) (map[string]any, error) {
	authenticator.mu.Lock()
	now := authenticator.now()
	authenticator.dropExpiredKeysLocked(now)
	keys := authenticator.keys
	hasKeys := len(keys) > 0
	fresh := hasKeys && now.Sub(authenticator.fetchedAt) < authenticator.config.CacheTTL
	backingOff := now.Before(authenticator.retryAt)

	if hasKeys {
		forcedAllowed := forceRefresh && !backingOff && now.Sub(authenticator.lastForcedRefresh) >= minForcedRefreshInterval
		if !forcedAllowed {
			if !fresh && !backingOff {
				authenticator.startFetchLocked(ctx)
			}
			authenticator.mu.Unlock()
			return keys, nil
		}
		authenticator.lastForcedRefresh = now
		call := authenticator.startFetchLocked(ctx)
		authenticator.mu.Unlock()
		select {
		case <-call.done:
			if call.err != nil {
				// The keys we had are still usable; the unknown kid fails.
				return keys, nil
			}
			return call.keys, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if backingOff {
		err := authenticator.lastErr
		authenticator.mu.Unlock()
		return nil, err
	}
	if forceRefresh {
		authenticator.lastForcedRefresh = now
	}
	call := authenticator.startFetchLocked(ctx)
	authenticator.mu.Unlock()
	select {
	case <-call.done:
		return call.keys, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// dropExpiredKeysLocked forgets cached keys once they are older than
// CacheTTL plus the StaleIfError grace.
func (authenticator *JWTAuthenticator) dropExpiredKeysLocked(now time.Time) {
	if len(authenticator.keys) == 0 {
		return
	}
	limit := authenticator.config.CacheTTL + authenticator.config.StaleIfError
	if now.Sub(authenticator.fetchedAt) < limit {
		return
	}
	if authenticator.config.StaleIfError > 0 && authenticator.failures > 0 {
		log.Printf("ERROR: OIDC JWKS signing keys fetched at %s are past cache_ttl plus stale_if_error (%s) after %d failed refresh attempts; dropping them, OIDC tokens are rejected until a fetch succeeds: %v",
			authenticator.fetchedAt.UTC().Format(time.RFC3339), limit, authenticator.failures, authenticator.lastErr)
	}
	authenticator.keys = nil
}

// startFetchLocked returns the in-flight fetch, starting one if there is
// none. At most one fetch goroutine exists per authenticator. The fetch keeps
// the request context's values but not its cancellation, because it is
// shared and may outlive the request; the HTTP client timeout (or
// jwksFetchTimeout) bounds it.
func (authenticator *JWTAuthenticator) startFetchLocked(ctx context.Context) *jwksFetch {
	if authenticator.inflight != nil {
		return authenticator.inflight
	}
	call := &jwksFetch{done: make(chan struct{})}
	authenticator.inflight = call
	go authenticator.runFetch(context.WithoutCancel(ctx), call)
	return call
}

func (authenticator *JWTAuthenticator) runFetch(ctx context.Context, call *jwksFetch) {
	if authenticator.client.Timeout <= 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, jwksFetchTimeout)
		defer cancel()
	}
	keys, err := authenticator.fetchKeys(ctx)

	authenticator.mu.Lock()
	now := authenticator.now()
	if err == nil {
		if authenticator.failures > 0 {
			log.Printf("OIDC JWKS refresh succeeded after %d failed attempts; using %d signing keys", authenticator.failures, len(keys))
		}
		// A successful fetch replaces the key set, so keys the IdP removed
		// stop working now.
		authenticator.keys = keys
		authenticator.fetchedAt = now
		authenticator.failures = 0
		authenticator.retryAt = time.Time{}
		authenticator.lastErr = nil
	} else {
		authenticator.failures++
		backoff := jwksBackoff(authenticator.failures)
		authenticator.retryAt = now.Add(backoff)
		authenticator.lastErr = err
		// Fetches happen at most once per backoff period, so logging each
		// failure logs at most once per period (once a minute at the cap).
		if len(authenticator.keys) > 0 {
			usableUntil := authenticator.fetchedAt.Add(authenticator.config.CacheTTL + authenticator.config.StaleIfError)
			log.Printf("WARNING: OIDC JWKS refresh failed (attempt %d, next attempt in %s); using cached signing keys until %s: %v",
				authenticator.failures, backoff, usableUntil.UTC().Format(time.RFC3339), err)
		} else {
			log.Printf("ERROR: OIDC JWKS fetch failed (attempt %d, next attempt in %s); OIDC tokens are rejected until a fetch succeeds: %v",
				authenticator.failures, backoff, err)
		}
	}
	call.keys, call.err = keys, err
	authenticator.inflight = nil
	authenticator.mu.Unlock()
	close(call.done)
}
