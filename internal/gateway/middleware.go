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

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// AuthMiddleware validates API keys from the Authorization header.
type AuthMiddleware struct {
	store         *store.Store
	enabled       bool
	authenticator TokenAuthenticator
}

// Identity is the authenticated caller attached to a request. Gateway and
// management handlers use this instead of trusting caller-provided role or
// workspace headers.
type Identity struct {
	APIKeyID       string
	RateLimit      int
	AuthMethod     string
	Subject        string
	WorkspaceID    string
	OrganizationID string
	Role           string
	Claims         map[string]any
}

// TokenAuthenticator validates a bearer token from an external identity
// provider and maps it into OmniSwitch's tenant-aware request identity.
type TokenAuthenticator interface {
	Authenticate(ctx context.Context, token string) (Identity, error)
}

func NewAuthMiddleware(st *store.Store, enabled bool) *AuthMiddleware {
	return &AuthMiddleware{store: st, enabled: enabled}
}

// SetTokenAuthenticator adds an external bearer-token identity source. Local
// API keys remain accepted, which makes OIDC migration incremental.
func (a *AuthMiddleware) SetTokenAuthenticator(authenticator TokenAuthenticator) {
	a.authenticator = authenticator
}

// internalIdentityHeaders are request headers that downstream handlers
// trust as identity, quota, or tenant attributes. Only the authentication
// middleware may set them; any value a client sends is removed first.
var internalIdentityHeaders = []string{
	"x-omniswitch-key-id",
	"x-omniswitch-rate-limit",
	"x-omniswitch-workspace-id",
	"x-omniswitch-organization-id",
	"x-omniswitch-role",
	"x-omniswitch-auth-method",
	"x-omniswitch-user-id",
}

// stripInternalIdentityHeaders deletes every internal identity header from
// the request.
func stripInternalIdentityHeaders(header http.Header) {
	for _, name := range internalIdentityHeaders {
		header.Del(name)
	}
}

// StripInternalIdentityHeaders removes client-supplied internal identity
// headers (key id, rate limit, workspace, organization, role, auth method,
// user id) regardless of the authentication mode. Use it as the outermost
// gateway middleware so a caller can never choose its own quota, cache
// scope, dashboard scope, or MCP policy role.
func StripInternalIdentityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stripInternalIdentityHeaders(r.Header)
		next.ServeHTTP(w, r)
	})
}

func (a *AuthMiddleware) Wrap(next http.Handler) http.Handler {
	if !a.enabled {
		// Without authentication nothing may vouch for identity headers.
		return StripInternalIdentityHeaders(next)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stripInternalIdentityHeaders(r.Header)
		if isPublicDiscoveryRoute(r) {
			next.ServeHTTP(w, r)
			return
		}
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
		if err == nil {
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
				workspace, workspaceErr := a.store.GetWorkspaceByID(r.Context(), apiKey.WorkspaceID)
				if workspaceErr != nil {
					log.Printf("failed to resolve workspace %q for API key %q: %v", apiKey.WorkspaceID, apiKey.ID, workspaceErr)
				} else {
					organizationID = workspace.OrganizationID
				}
			}
			// Hand the key's budgets to the gateway so budget admission
			// needs no second key lookup.
			r = r.WithContext(withAPIKeyBudget(r.Context(), apiKey))
			a.serveIdentity(w, r, next, Identity{
				APIKeyID:       apiKey.ID,
				RateLimit:      apiKey.RateLimit,
				AuthMethod:     "api_key",
				WorkspaceID:    apiKey.WorkspaceID,
				OrganizationID: organizationID,
				Role:           normalizeRole(apiKey.Role),
			})
			return
		}

		if a.authenticator == nil {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		identity, authErr := a.authenticator.Authenticate(r.Context(), key)
		if authErr != nil {
			writeError(w, http.StatusUnauthorized, "invalid bearer token")
			return
		}
		a.serveIdentity(w, r, next, identity)
	})
}

func (a *AuthMiddleware) serveIdentity(w http.ResponseWriter, r *http.Request, next http.Handler, identity Identity) {
	// These headers are internal identity attributes. Always overwrite them so a
	// caller cannot choose another tenant's cache scope, quota, or role.
	if identity.APIKeyID == "" {
		identity.APIKeyID = identity.Subject
	}
	r.Header.Set("x-omniswitch-key-id", identity.APIKeyID)
	if identity.RateLimit > 0 {
		r.Header.Set("x-omniswitch-rate-limit", strconv.Itoa(identity.RateLimit))
	} else {
		r.Header.Del("x-omniswitch-rate-limit")
	}
	r.Header.Set("x-omniswitch-workspace-id", identity.WorkspaceID)
	r.Header.Set("x-omniswitch-organization-id", identity.OrganizationID)
	r.Header.Set("x-omniswitch-role", normalizeRole(identity.Role))
	r.Header.Set("x-omniswitch-auth-method", identity.AuthMethod)
	if identity.Subject != "" {
		r.Header.Set("x-omniswitch-user-id", identity.Subject)
	} else {
		r.Header.Del("x-omniswitch-user-id")
	}
	next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), identity)))
}

// AuthorizationMiddleware protects OmniSwitch's control plane. Inference and
// MCP requests remain available to any valid workload key, while destructive
// management actions require an administrator key.
type AuthorizationMiddleware struct {
	enabled bool
	policy  *AuthorizationPolicy
}

func NewAuthorizationMiddleware(enabled bool) *AuthorizationMiddleware {
	return &AuthorizationMiddleware{enabled: enabled}
}

// SetPolicy adds CEL authorization after API-key authentication. It is kept
// separate from authentication so deployments can adopt policy gradually.
func (a *AuthorizationMiddleware) SetPolicy(policy *AuthorizationPolicy) {
	a.policy = policy
}

func (a *AuthorizationMiddleware) Wrap(next http.Handler) http.Handler {
	if !a.enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicDiscoveryRoute(r) {
			next.ServeHTTP(w, r)
			return
		}
		identity, ok := IdentityFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "authenticated identity is required")
			return
		}
		if allowed, reason := a.policy.Evaluate(r, identity); !allowed {
			writeError(w, http.StatusForbidden, reason)
			return
		}
		required := requiredRole(r.Method, r.URL.Path)
		if required == "" {
			next.ServeHTTP(w, r)
			return
		}
		if roleRank(identity.Role) < roleRank(required) {
			writeError(w, http.StatusForbidden, "insufficient role for this endpoint")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isPublicDiscoveryRoute(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == "/.well-known/agent-card.json"
}

// isReadMethod reports whether method is a safe, read-only HTTP method.
// Only these count as reads for role checks; every other method (including
// unusual ones such as PROPFIND or a GET-like custom verb) is treated as a
// write so authorization fails closed.
func isReadMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
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
	read := isReadMethod(method)
	switch path {
	case "/api/keys", "/api/virtual-keys", "/api/virtual-keys/rotate", "/api/users", "/api/workspace-members",
		"/api/configs", "/api/config/raw":
		// These expose credentials metadata, user emails, or raw config
		// bodies that can contain secrets, so even reads need an admin.
		return "admin"
	case "/api/orgs", "/api/workspaces":
		if read {
			return "viewer"
		}
		return "admin"
	case "/api/prompts", "/api/prompts/versions", "/api/prompts/render", "/api/evals/policy":
		if read {
			return "viewer"
		}
		return "member"
	case "/api/feedback":
		// Workload keys report feedback on their own responses.
		return "viewer"
	default:
		if read {
			return "viewer"
		}
		return "admin"
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

// RateLimitBackend makes quota enforcement portable across local and shared
// deployments. Implementations return an optional retry-after duration.
type RateLimitBackend interface {
	Allow(ctx context.Context, key string, limit int, interval time.Duration) (allowed bool, retryAfter time.Duration, err error)
}

// RateLimiter applies per-key limits using a local or distributed backend.
type RateLimiter struct {
	backend  RateLimitBackend
	limit    int
	interval time.Duration
	failOpen bool
}

func NewRateLimiter(limit int, interval time.Duration) *RateLimiter {
	return NewRateLimiterWithBackend(limit, interval, newLocalRateLimitBackend(), false)
}

func NewRateLimiterWithBackend(limit int, interval time.Duration, backend RateLimitBackend, failOpen bool) *RateLimiter {
	if backend == nil {
		backend = newLocalRateLimitBackend()
	}
	return &RateLimiter{backend: backend, limit: limit, interval: interval, failOpen: failOpen}
}

func (rl *RateLimiter) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-omniswitch-key-id")
		if key == "" {
			key = remoteHost(r.RemoteAddr)
		}
		limit := rl.limit
		if header := r.Header.Get("x-omniswitch-rate-limit"); header != "" {
			if parsed, err := strconv.Atoi(header); err == nil && parsed > 0 {
				limit = parsed
			}
		}

		allowed, retryAfter, err := rl.backend.Allow(r.Context(), key, limit, rl.interval)
		if err != nil {
			if rl.failOpen {
				log.Printf("rate limit backend failure (allowing request): %v", err)
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusServiceUnavailable, "rate limit backend unavailable")
			return
		}
		if !allowed {
			if retryAfter > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Seconds()))))
			}
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

type localRateLimitBackend struct {
	mu        sync.Mutex
	windows   map[string][]time.Time
	lastSweep time.Time
	now       func() time.Time
}

func newLocalRateLimitBackend() *localRateLimitBackend {
	return &localRateLimitBackend{windows: make(map[string][]time.Time), now: time.Now}
}

func (backend *localRateLimitBackend) Allow(_ context.Context, key string, limit int, interval time.Duration) (bool, time.Duration, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()

	now := backend.now()
	cutoff := now.Add(-interval)
	backend.sweepLocked(now, cutoff, interval)

	timestamps := backend.windows[key]
	var valid []time.Time
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			valid = append(valid, ts)
		}
	}

	if len(valid) >= limit {
		if len(valid) == 0 {
			delete(backend.windows, key)
		} else {
			backend.windows[key] = valid
		}
		retryAfter := interval
		if len(valid) > 0 {
			retryAfter = valid[0].Add(interval).Sub(now)
		}
		return false, retryAfter, nil
	}

	backend.windows[key] = append(valid, now)
	return true, 0, nil
}

// sweepLocked drops keys whose newest request is older than the window, at
// most once per interval, so one-off keys (such as many client addresses)
// do not accumulate forever. The caller holds backend.mu.
func (backend *localRateLimitBackend) sweepLocked(now, cutoff time.Time, interval time.Duration) {
	if now.Sub(backend.lastSweep) < interval {
		return
	}
	backend.lastSweep = now
	for key, timestamps := range backend.windows {
		if len(timestamps) == 0 || !timestamps[len(timestamps)-1].After(cutoff) {
			delete(backend.windows, key)
		}
	}
}

func (backend *localRateLimitBackend) size() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return len(backend.windows)
}

// Default control-plane request body limits.
const (
	// DefaultControlPlaneBodyLimit caps /api/* request bodies.
	DefaultControlPlaneBodyLimit int64 = 1 << 20
	// DefaultConfigBodyLimit caps gateway config bodies (/api/configs and
	// /api/config/raw), which are legitimately larger.
	DefaultConfigBodyLimit int64 = 8 << 20
)

// ControlPlaneBodyLimit caps request bodies on /api/* routes. Requests whose
// declared Content-Length is over the limit get 413 immediately; streamed
// bodies are wrapped in http.MaxBytesReader so decoding fails once the limit
// is reached instead of buffering an unbounded body in memory, and the
// handler's error response is then replaced with the same 413. The body is
// read only by the handler, after authentication. Config endpoints use
// configLimit; every other /api/ path uses limit.
func ControlPlaneBodyLimit(limit, configLimit int64, next http.Handler) http.Handler {
	if limit <= 0 {
		limit = DefaultControlPlaneBodyLimit
	}
	if configLimit <= 0 {
		configLimit = DefaultConfigBodyLimit
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		max := limit
		if r.URL.Path == "/api/configs" || r.URL.Path == "/api/config/raw" {
			max = configLimit
		}
		if r.ContentLength > max {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		body := &limitedBody{ReadCloser: http.MaxBytesReader(w, r.Body, max)}
		r.Body = body
		next.ServeHTTP(&tooLargeWriter{ResponseWriter: w, body: body}, r)
	})
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
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-omniswitch-provider, x-omniswitch-trace-id, x-omniswitch-session-id")
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

// ContextWithIdentity returns ctx carrying identity, as AuthMiddleware does
// for authenticated requests. It exists for handlers' tests and embedders.
func ContextWithIdentity(ctx context.Context, identity Identity) context.Context {
	return withIdentity(ctx, identity)
}

// RoleRank orders roles: owner 4, admin 3, member (or editor) 2, anything
// else 1 (viewer). Handlers use it to stop callers granting roles above
// their own.
func RoleRank(role string) int {
	return roleRank(role)
}

// NormalizeRole maps a role name onto owner, admin, member or viewer.
func NormalizeRole(role string) string {
	return normalizeRole(role)
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
