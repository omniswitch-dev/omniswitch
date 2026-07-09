package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel/internal/store"
)

// AuthMiddleware validates API keys from the Authorization header.
type AuthMiddleware struct {
	store   *store.Store
	enabled bool
}

func NewAuthMiddleware(st *store.Store, enabled bool) *AuthMiddleware {
	return &AuthMiddleware{store: st, enabled: enabled}
}

func (a *AuthMiddleware) Wrap(next http.Handler) http.Handler {
	if !a.enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			writeError(w, http.StatusUnauthorized, "missing Authorization header")
			return
		}
		key := strings.TrimPrefix(auth, "Bearer ")
		if key == auth {
			writeError(w, http.StatusUnauthorized, "Authorization must use Bearer scheme")
			return
		}

		hash := sha256.Sum256([]byte(key))
		hashStr := hex.EncodeToString(hash[:])

		apiKey, err := a.store.GetAPIKeyByHash(r.Context(), hashStr)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		if !apiKey.Enabled {
			writeError(w, http.StatusForbidden, "API key is disabled")
			return
		}
		if apiKey.ExpiresAt != nil && time.Now().After(*apiKey.ExpiresAt) {
			writeError(w, http.StatusForbidden, "API key has expired")
			return
		}

		r.Header.Set("x-sentinel-key-id", apiKey.ID)
		r.Header.Set("x-sentinel-rate-limit", strconv.Itoa(apiKey.RateLimit))
		next.ServeHTTP(w, r)
	})
}

// RateLimiter provides simple per-key sliding window rate limiting.
type RateLimiter struct {
	mu       sync.Mutex
	windows  map[string][]time.Time
	limit    int
	interval time.Duration
}

func NewRateLimiter(limit int, interval time.Duration) *RateLimiter {
	return &RateLimiter{
		windows:  make(map[string][]time.Time),
		limit:    limit,
		interval: interval,
	}
}

func (rl *RateLimiter) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-sentinel-key-id")
		if key == "" {
			key = r.RemoteAddr
		}
		limit := rl.limit
		if header := r.Header.Get("x-sentinel-rate-limit"); header != "" {
			if parsed, err := strconv.Atoi(header); err == nil && parsed > 0 {
				limit = parsed
			}
		}

		if !rl.allow(key, limit) {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) allow(key string, limit int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.interval)

	timestamps := rl.windows[key]
	var valid []time.Time
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			valid = append(valid, ts)
		}
	}

	if len(valid) >= limit {
		rl.windows[key] = valid
		return false
	}

	rl.windows[key] = append(valid, now)
	return true
}

// LoggingMiddleware logs every request.
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s %v", r.Method, r.URL.Path, r.RemoteAddr, time.Since(start))
	})
}

// CORSMiddleware adds CORS headers for the dashboard.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-sentinel-provider")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// contextKey is an unexported type for context keys.
type contextKey string

// WithRequestID adds a request ID to the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey("request_id"), id)
}

func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey("trace_id"), id)
}

func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey("session_id"), id)
}
