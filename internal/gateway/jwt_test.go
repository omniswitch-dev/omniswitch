package gateway

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
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
)

func TestJWTAuthenticatorValidatesOIDCClaimsAndCachesJWKS(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	jwk, err := json.Marshal(map[string]string{
		"kty": "RSA",
		"kid": "test-key",
		"n":   base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.E)).Bytes()),
	})
	if err != nil {
		t.Fatalf("Marshal(jwk) error = %v", err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[` + string(jwk) + `]}`))
	}))
	defer server.Close()

	authenticator, err := NewJWTAuthenticator(JWTAuthenticatorConfig{
		JWKSURL:           server.URL,
		Issuer:            "https://issuer.example.test",
		Audience:          "omniswitch",
		RoleClaim:         "roles",
		WorkspaceClaim:    "workspace",
		OrganizationClaim: "organization",
		CacheTTL:          time.Hour,
	})
	if err != nil {
		t.Fatalf("NewJWTAuthenticator() error = %v", err)
	}
	rawToken := signedJWT(t, privateKey, jwt.MapClaims{
		"sub":          "workload-123",
		"iss":          "https://issuer.example.test",
		"aud":          "omniswitch",
		"roles":        []string{"admin"},
		"workspace":    "ws_1",
		"organization": "org_1",
		"department":   "platform",
		"exp":          time.Now().Add(time.Minute).Unix(),
	})

	for range 2 {
		identity, err := authenticator.Authenticate(context.Background(), rawToken)
		if err != nil {
			t.Fatalf("Authenticate() error = %v", err)
		}
		if identity.APIKeyID != "jwt:workload-123" || identity.Subject != "workload-123" || identity.Role != "admin" || identity.WorkspaceID != "ws_1" || identity.OrganizationID != "org_1" {
			t.Fatalf("identity = %+v, want mapped OIDC identity", identity)
		}
		if identity.Claims["department"] != "platform" {
			t.Fatalf("claims = %+v, want department claim", identity.Claims)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("JWKS requests = %d, want 1 cached request", requests.Load())
	}
}

func TestJWTAuthenticatorRejectsIssuerMismatch(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	jwk, err := json.Marshal(map[string]string{
		"kty": "RSA",
		"kid": "test-key",
		"n":   base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.E)).Bytes()),
	})
	if err != nil {
		t.Fatalf("Marshal(jwk) error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[` + string(jwk) + `]}`))
	}))
	defer server.Close()
	authenticator, err := NewJWTAuthenticator(JWTAuthenticatorConfig{JWKSURL: server.URL, Issuer: "https://expected.example.test"})
	if err != nil {
		t.Fatalf("NewJWTAuthenticator() error = %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), signedJWT(t, privateKey, jwt.MapClaims{
		"sub": "workload-123", "iss": "https://different.example.test", "exp": time.Now().Add(time.Minute).Unix(),
	})); err == nil {
		t.Fatal("Authenticate() error = nil, want issuer validation error")
	}
}

func signedJWT(t *testing.T, privateKey *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	raw, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	return raw
}

func TestJWTAuthenticatorAcceptsECKeys(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	point, err := privateKey.PublicKey.Bytes()
	if err != nil {
		t.Fatalf("PublicKey.Bytes() error = %v", err)
	}
	jwk, err := json.Marshal(map[string]string{
		"kty": "EC",
		"kid": "ec-key",
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(point[1:33]),
		"y":   base64.RawURLEncoding.EncodeToString(point[33:]),
	})
	if err != nil {
		t.Fatalf("Marshal(jwk) error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[` + string(jwk) + `]}`))
	}))
	defer server.Close()
	authenticator, err := NewJWTAuthenticator(JWTAuthenticatorConfig{JWKSURL: server.URL})
	if err != nil {
		t.Fatalf("NewJWTAuthenticator() error = %v", err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"sub": "ec-workload", "exp": time.Now().Add(time.Minute).Unix(),
	})
	token.Header["kid"] = "ec-key"
	raw, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	identity, err := authenticator.Authenticate(context.Background(), raw)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if identity.Subject != "ec-workload" {
		t.Fatalf("identity.Subject = %q, want ec-workload", identity.Subject)
	}
}

func TestParseECJWKValidatesPoint(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	point, err := privateKey.PublicKey.Bytes()
	if err != nil {
		t.Fatalf("PublicKey.Bytes() error = %v", err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	x, y := point[1:33], point[33:]

	if _, err := parseECJWK(elliptic.P256(), encode(x), encode(y)); err != nil {
		t.Fatalf("parseECJWK(valid) error = %v", err)
	}
	if _, err := parseECJWK(elliptic.P256(), encode([]byte{1}), encode([]byte{1})); err == nil {
		t.Fatal("parseECJWK(off-curve point) error = nil, want rejection")
	}
	if _, err := parseECJWK(elliptic.P256(), encode(append([]byte{0}, x...)), encode(y)); err == nil {
		t.Fatal("parseECJWK(oversized coordinate) error = nil, want rejection")
	}
	if _, err := parseECJWK(elliptic.P256(), "not base64!", encode(y)); err == nil {
		t.Fatal("parseECJWK(bad base64) error = nil, want rejection")
	}
	// Issuers that strip leading zero bytes must still be accepted.
	if x[0] == 0 {
		if _, err := parseECJWK(elliptic.P256(), encode(x[1:]), encode(y)); err != nil {
			t.Fatalf("parseECJWK(stripped leading zero) error = %v", err)
		}
	}
	padded, err := decodeJWKCoordinate(encode([]byte{0xAB}), 4)
	if err != nil || len(padded) != 4 || padded[3] != 0xAB || padded[0] != 0 {
		t.Fatalf("decodeJWKCoordinate(short) = %x, %v; want left-padded 000000ab", padded, err)
	}
}
