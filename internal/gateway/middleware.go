package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net"
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

// Identity is the authenticated caller attached to a request. Gateway and
// management handlers use this instead of trusting caller-provided role or
// workspace headers.
type Identity struct {
	APIKeyID       string
	WorkspaceID    string
	OrganizationID string
	Role           string
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

		organizationID := ""
		if apiKey.WorkspaceID != "" {
			workspace, err := a.store.GetWorkspaceByID(r.Context(), apiKey.WorkspaceID)
			if err != nil {
				log.Printf("failed to resolve workspace %q for API key %q: %v", apiKey.WorkspaceID, apiKey.ID, err)
			} else {
				organizationID = workspace.OrganizationID
			}
		}
		// These headers are internal identity attributes. Always overwrite them
		// so a caller cannot choose another tenant's cache scope or role.
		r.Header.Set("x-sentinel-key-id", apiKey.ID)
		r.Header.Set("x-sentinel-rate-limit", strconv.Itoa(apiKey.RateLimit))
		r.Header.Set("x-sentinel-workspace-id", apiKey.WorkspaceID)
		r.Header.Set("x-sentinel-organization-id", organizationID)
		r.Header.Set("x-sentinel-role", normalizeRole(apiKey.Role))
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), Identity{
			APIKeyID:       apiKey.ID,
			WorkspaceID:    apiKey.WorkspaceID,
			OrganizationID: organizationID,
			Role:           normalizeRole(apiKey.Role),
		})))
	})
}

// AuthorizationMiddleware protects Sentinel's control plane. Inference and
// MCP requests remain available to any valid workload key, while destructive
// management actions require an administrator key.
type AuthorizationMiddleware struct {
	enabled bool
}

func NewAuthorizationMiddleware(enabled bool) *AuthorizationMiddleware {
	return &AuthorizationMiddleware{enabled: enabled}
}

func (a *AuthorizationMiddleware) Wrap(next http.Handler) http.Handler {
	if !a.enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		required := requiredRole(r.Method, r.URL.Path)
		if required == "" {
			next.ServeHTTP(w, r)
			return
		}
		identity, ok := IdentityFromContext(r.Context())
		if !ok || roleRank(identity.Role) < roleRank(required) {
			writeError(w, http.StatusForbidden, "insufficient role for this endpoint")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requiredRole(method, path string) string {
	// Health is intentionally public once the outer authentication middleware
	// permits it; all other dashboard and control-plane routes need a role.
	if path == "/api/health" || strings.HasPrefix(path, "/v1/") || path == "/mcp" {
		return ""
	}
	if !strings.HasPrefix(path, "/api/") {
		return "viewer"
	}
	switch path {
	case "/api/keys", "/api/virtual-keys", "/api/virtual-keys/rotate", "/api/orgs", "/api/workspaces", "/api/users", "/api/workspace-members":
		if method != http.MethodGet {
			return "admin"
		}
		return "viewer"
	case "/api/prompts", "/api/prompts/render", "/api/evals/policy":
		if method != http.MethodGet {
			return "member"
		}
		return "viewer"
	default:
		return "viewer"
	}
}

func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "owner":
		return "owner"
	case "admin":
		return "admin"
	case "member", "editor":
		return "member"
	default:
		return "viewer"
	}
}

func roleRank(role string) int {
	switch normalizeRole(role) {
	case "owner":
		return 4
	case "admin":
		return 3
	case "member":
		return 2
	default:
		return 1
	}
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
			key = remoteHost(r.RemoteAddr)
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

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil && host != "" {
		return host
	}
	return remoteAddr
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

// CORSMiddleware keeps the historical permissive behavior for embedded users.
// Production callers should use CORSMiddlewareWithOrigins with explicit origins.
func CORSMiddleware(next http.Handler) http.Handler {
	return CORSMiddlewareWithOrigins([]string{"*"}, next)
}

// CORSMiddlewareWithOrigins adds CORS headers only for configured origins. An
// empty allow-list disables browser cross-origin access rather than silently
// exposing bearer-token APIs to every website.
func CORSMiddlewareWithOrigins(origins []string, next http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(origins))
	allowAny := false
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "*" {
			allowAny = true
		}
		if origin != "" {
			allowed[origin] = struct{}{}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowAny {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if _, ok := allowed[origin]; ok {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		if allowAny || origin != "" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-sentinel-provider, x-sentinel-trace-id, x-sentinel-session-id")
		}
		if r.Method == http.MethodOptions {
			if !allowAny {
				if _, ok := allowed[origin]; !ok {
					writeError(w, http.StatusForbidden, "origin is not allowed")
					return
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// contextKey is an unexported type for context keys.
type contextKey string

const identityContextKey contextKey = "identity"

func withIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityContextKey, identity)
}

func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityContextKey).(Identity)
	return identity, ok
}

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
