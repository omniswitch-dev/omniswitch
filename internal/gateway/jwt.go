package gateway

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var errJWTKeyNotFound = errors.New("JWT signing key not found")

// JWTAuthenticatorConfig configures OIDC-compatible JWT validation against a
// JSON Web Key Set. Tokens must carry exp. Issuer and audience are optional,
// but without them any token signed by the JWKS keys is accepted, including
// tokens minted for other applications; see MissingBindingWarnings.
type JWTAuthenticatorConfig struct {
	JWKSURL           string
	Issuer            string
	Audience          string
	RoleClaim         string
	WorkspaceClaim    string
	OrganizationClaim string
	CacheTTL          time.Duration
	HTTPClient        *http.Client
}

// JWTAuthenticator validates signed JWTs fetched from a configured JWKS URL.
// It caches public keys and refreshes them when a token names a kid that is
// not cached (an IdP key rotation). Forced refreshes are shared between
// concurrent callers and happen at most once per minForcedRefreshInterval.
type JWTAuthenticator struct {
	config JWTAuthenticatorConfig
	client *http.Client

	mu                sync.Mutex
	keys              map[string]any
	fetchedAt         time.Time
	lastForcedRefresh time.Time
	inflight          *jwksFetch
}

func NewJWTAuthenticator(config JWTAuthenticatorConfig) (*JWTAuthenticator, error) {
	config.JWKSURL = strings.TrimSpace(config.JWKSURL)
	if config.JWKSURL == "" {
		return nil, fmt.Errorf("JWKS URL is required")
	}
	parsedURL, err := url.Parse(config.JWKSURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return nil, fmt.Errorf("JWKS URL must be an absolute HTTP(S) URL")
	}
	if config.CacheTTL <= 0 {
		config.CacheTTL = 5 * time.Minute
	}
	if config.RoleClaim == "" {
		config.RoleClaim = "role"
	}
	if config.WorkspaceClaim == "" {
		config.WorkspaceClaim = "workspace_id"
	}
	if config.OrganizationClaim == "" {
		config.OrganizationClaim = "organization_id"
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &JWTAuthenticator{config: config, client: client}, nil
}

// MissingBindingWarnings describes issuer or audience checks that are not
// configured, so startup can log them.
func (config JWTAuthenticatorConfig) MissingBindingWarnings() []string {
	var warnings []string
	if strings.TrimSpace(config.Issuer) == "" {
		warnings = append(warnings, "OIDC issuer is not configured; JWTs from any issuer using these signing keys are accepted")
	}
	if strings.TrimSpace(config.Audience) == "" {
		warnings = append(warnings, "OIDC audience is not configured; JWTs minted for other applications are accepted")
	}
	return warnings
}

// Authenticate implements TokenAuthenticator.
func (authenticator *JWTAuthenticator) Authenticate(ctx context.Context, rawToken string) (Identity, error) {
	claims, err := authenticator.parse(ctx, rawToken, false)
	if errors.Is(err, errJWTKeyNotFound) {
		claims, err = authenticator.parse(ctx, rawToken, true)
	}
	if err != nil {
		return Identity{}, err
	}

	subject := claimString(claims, "sub")
	if subject == "" {
		return Identity{}, fmt.Errorf("JWT subject is required")
	}
	return Identity{
		APIKeyID:       "jwt:" + subject,
		AuthMethod:     "oidc",
		Subject:        subject,
		WorkspaceID:    claimString(claims, authenticator.config.WorkspaceClaim),
		OrganizationID: claimString(claims, authenticator.config.OrganizationClaim),
		Role:           normalizeRole(claimString(claims, authenticator.config.RoleClaim)),
		Claims:         map[string]any(claims),
	}, nil
}

// jwtClockLeeway tolerates small clock differences with the IdP on exp, nbf
// and iat.
const jwtClockLeeway = 30 * time.Second

func (authenticator *JWTAuthenticator) parse(ctx context.Context, rawToken string, forceRefresh bool) (jwt.MapClaims, error) {
	options := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "EdDSA"}),
		// Tokens without exp would be valid forever.
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(jwtClockLeeway),
	}
	if issuer := strings.TrimSpace(authenticator.config.Issuer); issuer != "" {
		options = append(options, jwt.WithIssuer(issuer))
	}
	if audience := strings.TrimSpace(authenticator.config.Audience); audience != "" {
		options = append(options, jwt.WithAudience(audience))
	}
	parser := jwt.NewParser(options...)
	claims := jwt.MapClaims{}
	_, err := parser.ParseWithClaims(rawToken, claims, func(token *jwt.Token) (any, error) {
		return authenticator.keyForToken(ctx, token, forceRefresh)
	})
	if err != nil {
		return nil, fmt.Errorf("validate JWT: %w", err)
	}
	return claims, nil
}

func (authenticator *JWTAuthenticator) keyForToken(ctx context.Context, token *jwt.Token, forceRefresh bool) (any, error) {
	keys, err := authenticator.currentKeys(ctx, forceRefresh)
	if err != nil {
		return nil, err
	}
	kid, _ := token.Header["kid"].(string)
	if kid != "" {
		if key, ok := keys[kid]; ok {
			return key, nil
		}
		return nil, errJWTKeyNotFound
	}
	if len(keys) == 1 {
		for _, key := range keys {
			return key, nil
		}
	}
	return nil, errJWTKeyNotFound
}

// minForcedRefreshInterval bounds how often a token with an unknown kid can
// trigger a JWKS refetch. Without it any unauthenticated caller could force
// one outbound IdP request per inbound request.
const minForcedRefreshInterval = 30 * time.Second

// maxJWKSBytes caps the JWKS document size.
const maxJWKSBytes = 1 << 20

// jwksFetch is one in-flight JWKS request shared by every caller that needs
// fresh keys at the same time.
type jwksFetch struct {
	done chan struct{}
	keys map[string]any
	err  error
}

func (authenticator *JWTAuthenticator) currentKeys(ctx context.Context, forceRefresh bool) (map[string]any, error) {
	authenticator.mu.Lock()
	now := time.Now()
	if len(authenticator.keys) > 0 {
		fresh := now.Sub(authenticator.fetchedAt) < authenticator.config.CacheTTL
		if !forceRefresh && fresh {
			keys := authenticator.keys
			authenticator.mu.Unlock()
			return keys, nil
		}
		if forceRefresh && fresh && now.Sub(authenticator.lastForcedRefresh) < minForcedRefreshInterval {
			keys := authenticator.keys
			authenticator.mu.Unlock()
			return keys, nil
		}
	}
	if forceRefresh {
		authenticator.lastForcedRefresh = now
	}
	call := authenticator.inflight
	if call == nil {
		call = &jwksFetch{done: make(chan struct{})}
		authenticator.inflight = call
		authenticator.mu.Unlock()
		// The fetch is shared, so one caller's cancellation must not fail the
		// others; the HTTP client timeout still bounds it.
		keys, err := authenticator.fetchKeys(context.WithoutCancel(ctx))
		authenticator.mu.Lock()
		if err == nil {
			authenticator.keys = keys
			authenticator.fetchedAt = time.Now()
		}
		call.keys, call.err = keys, err
		authenticator.inflight = nil
		authenticator.mu.Unlock()
		close(call.done)
	} else {
		authenticator.mu.Unlock()
	}
	select {
	case <-call.done:
		return call.keys, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (authenticator *JWTAuthenticator) fetchKeys(ctx context.Context) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, authenticator.config.JWKSURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create JWKS request: %w", err)
	}
	response, err := authenticator.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch JWKS: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("fetch JWKS: unexpected status %s", response.Status)
	}
	var document struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxJWKSBytes)).Decode(&document); err != nil {
		return nil, fmt.Errorf("decode JWKS: %w", err)
	}
	keys := make(map[string]any, len(document.Keys))
	for _, rawKey := range document.Keys {
		kid, key, err := parseJWK(rawKey)
		if err != nil {
			return nil, err
		}
		if _, exists := keys[kid]; exists {
			return nil, fmt.Errorf("JWKS contains duplicate kid %q", kid)
		}
		keys[kid] = key
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("JWKS contains no supported public keys")
	}
	return keys, nil
}

func parseJWK(raw json.RawMessage) (string, any, error) {
	var key struct {
		KTY string `json:"kty"`
		KID string `json:"kid"`
		N   string `json:"n"`
		E   string `json:"e"`
		CRV string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}
	if err := json.Unmarshal(raw, &key); err != nil {
		return "", nil, fmt.Errorf("decode JWK: %w", err)
	}
	key.KID = strings.TrimSpace(key.KID)
	if key.KID == "" {
		return "", nil, fmt.Errorf("JWK kid is required")
	}
	switch key.KTY {
	case "RSA":
		modulus, err := decodeJWKInteger(key.N)
		if err != nil {
			return "", nil, fmt.Errorf("decode RSA JWK %q modulus: %w", key.KID, err)
		}
		exponent, err := decodeJWKInteger(key.E)
		if err != nil || !exponent.IsInt64() || exponent.Int64() <= 1 {
			return "", nil, fmt.Errorf("decode RSA JWK %q exponent", key.KID)
		}
		return key.KID, &rsa.PublicKey{N: modulus, E: int(exponent.Int64())}, nil
	case "EC":
		curve := ellipticCurve(key.CRV)
		if curve == nil {
			return "", nil, fmt.Errorf("unsupported EC JWK curve %q", key.CRV)
		}
		publicKey, err := parseECJWK(curve, key.X, key.Y)
		if err != nil {
			return "", nil, fmt.Errorf("decode EC JWK %q: %w", key.KID, err)
		}
		return key.KID, publicKey, nil
	case "OKP":
		if key.CRV != "Ed25519" {
			return "", nil, fmt.Errorf("unsupported OKP JWK curve %q", key.CRV)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return "", nil, fmt.Errorf("decode Ed25519 JWK %q", key.KID)
		}
		return key.KID, ed25519.PublicKey(decoded), nil
	default:
		return "", nil, fmt.Errorf("unsupported JWK key type %q", key.KTY)
	}
}

// parseECJWK decodes the base64url x/y coordinates of an EC JWK into an ECDSA
// public key. ecdsa.ParseUncompressedPublicKey rejects points that are not on
// the curve, so a malicious or corrupt JWKS cannot smuggle in an invalid key.
// RFC 7518 section 6.2.1 requires full-length coordinates, but some issuers
// strip leading zero bytes, so shorter values are left-padded.
func parseECJWK(curve elliptic.Curve, encodedX, encodedY string) (*ecdsa.PublicKey, error) {
	size := (curve.Params().BitSize + 7) / 8
	x, err := decodeJWKCoordinate(encodedX, size)
	if err != nil {
		return nil, fmt.Errorf("x: %w", err)
	}
	y, err := decodeJWKCoordinate(encodedY, size)
	if err != nil {
		return nil, fmt.Errorf("y: %w", err)
	}
	point := make([]byte, 0, 1+2*size)
	point = append(point, 0x04)
	point = append(point, x...)
	point = append(point, y...)
	publicKey, err := ecdsa.ParseUncompressedPublicKey(curve, point)
	if err != nil {
		return nil, fmt.Errorf("invalid curve point: %w", err)
	}
	return publicKey, nil
}

func decodeJWKCoordinate(encoded string, size int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 {
		return nil, fmt.Errorf("invalid base64url coordinate")
	}
	if len(decoded) > size {
		return nil, fmt.Errorf("coordinate is %d bytes, want at most %d", len(decoded), size)
	}
	if len(decoded) < size {
		padded := make([]byte, size)
		copy(padded[size-len(decoded):], decoded)
		decoded = padded
	}
	return decoded, nil
}

func decodeJWKInteger(encoded string) (*big.Int, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 {
		return nil, fmt.Errorf("invalid base64url integer")
	}
	return new(big.Int).SetBytes(decoded), nil
}

func ellipticCurve(name string) elliptic.Curve {
	switch name {
	case "P-256":
		return elliptic.P256()
	case "P-384":
		return elliptic.P384()
	case "P-521":
		return elliptic.P521()
	default:
		return nil
	}
}

func claimString(claims jwt.MapClaims, name string) string {
	value, ok := claims[name]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}
