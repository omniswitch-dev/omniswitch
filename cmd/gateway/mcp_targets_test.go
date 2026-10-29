package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/omniswitch-dev/omniswitch/internal/admin"
	"github.com/omniswitch-dev/omniswitch/internal/dashboard"
	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	mcpproxy "github.com/omniswitch-dev/omniswitch/internal/proxy"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

func TestBuildMCPTargetsPassesExchangeTimeoutAndMetadata(t *testing.T) {
	t.Setenv("TEST_MCP_CLIENT_SECRET", "s3cret")
	t.Setenv("TEST_MCP_ACTOR", "actor-jwt")
	settings := runtimeSettings{mcpTargets: []gatewayconfig.MCPTarget{
		{
			Name: "crm", Upstream: "https://crm.example.test/mcp",
			Timeout:  &gatewayconfig.Duration{Duration: 45 * time.Second},
			Metadata: map[string]string{"environment": "production"},
			TokenExchange: &gatewayconfig.MCPTokenExchange{
				TokenURL: " https://idp.example.test/token ", ClientID: "omniswitch", ClientSecret: "${TEST_MCP_CLIENT_SECRET}",
				Audience: "crm-api", Scope: "crm.read", Resource: "https://crm.example.test/",
				ActorToken: "$TEST_MCP_ACTOR", ActorTokenType: "urn:ietf:params:oauth:token-type:jwt",
				TokenCacheTTL: &gatewayconfig.Duration{Duration: 5 * time.Minute},
			},
		},
		{Name: "plain", Upstream: "https://plain.example.test/mcp"},
	}}
	targets, err := buildMCPTargets(nil, settings)
	if err != nil {
		t.Fatalf("buildMCPTargets() error = %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(targets))
	}
	crm := targets[0]
	if crm.Timeout != 45*time.Second || crm.Metadata["environment"] != "production" {
		t.Fatalf("crm timeout/metadata = %s/%v, want 45s and the configured metadata", crm.Timeout, crm.Metadata)
	}
	want := mcpproxy.TokenExchangeConfig{
		TokenURL: "https://idp.example.test/token", ClientID: "omniswitch", ClientSecret: "s3cret",
		Audience: "crm-api", Scope: "crm.read", Resource: "https://crm.example.test/",
		ActorToken: "actor-jwt", ActorTokenType: "urn:ietf:params:oauth:token-type:jwt",
		TokenCacheTTL: 5 * time.Minute,
	}
	if crm.TokenExchange == nil || *crm.TokenExchange != want {
		t.Fatalf("crm token exchange = %+v, want %+v", crm.TokenExchange, want)
	}
	if plain := targets[1]; plain.TokenExchange != nil || plain.Timeout != 0 || plain.Metadata != nil {
		t.Fatalf("plain target = %+v, want no exchange, timeout or metadata", plain)
	}
}

func TestBuildMCPTargetsRejectsEmptyExpandedExchangeSecret(t *testing.T) {
	t.Setenv("TEST_MCP_EMPTY_SECRET", "")
	settings := runtimeSettings{mcpTargets: []gatewayconfig.MCPTarget{{
		Name: "crm", Upstream: "https://crm.example.test/mcp",
		TokenExchange: &gatewayconfig.MCPTokenExchange{
			TokenURL: "https://idp.example.test/token", ClientID: "omniswitch", ClientSecret: "${TEST_MCP_EMPTY_SECRET}",
		},
	}}}
	_, err := buildMCPTargets(nil, settings)
	if err == nil || !strings.Contains(err.Error(), `MCP target "crm" token_exchange.client_secret is empty after expanding environment variables`) {
		t.Fatalf("buildMCPTargets() error = %v, want an empty secret error", err)
	}
}

// TestMCPTargetTokenExchangeEndToEnd loads token_exchange, metadata and
// timeout from a gateway config file and sends OIDC-authenticated MCP calls
// through the full middleware chain.
func TestMCPTargetTokenExchangeEndToEnd(t *testing.T) {
	clearGatewayEnv(t)
	signingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwk, _ := json.Marshal(map[string]string{
		"kty": "RSA", "kid": "mcp-test", "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(signingKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(signingKey.E)).Bytes()),
	})
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"keys":[`+string(jwk)+`]}`)
	}))
	defer jwks.Close()

	var mu sync.Mutex
	var exchanges []url.Values
	var upstreamCalls []string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		exchanges = append(exchanges, r.PostForm)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"crm-token-for-`+r.PostForm.Get("audience")+`","expires_in":3600}`)
	}))
	defer tokenServer.Close()
	crm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&rpc)
		mu.Lock()
		upstreamCalls = append(upstreamCalls, rpc.Params.Name+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"record 42"}]}}`)
	}))
	defer crm.Close()
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer slow.Close()
	defer close(release)

	config := fmt.Sprintf(`apiVersion: omniswitch.dev/v1
kind: GatewayConfig
gateway:
  auth: true
identity:
  oidc:
    jwks_url: %s
    issuer: https://issuer.example.test
    audience: omniswitch
mcp:
  enabled: true
  targets:
    - name: crm
      upstream: %s
      metadata:
        environment: production
        resource_type: repository
      token_exchange:
        token_url: %s
        client_id: omniswitch
        client_secret: ${TEST_MCP_EXCHANGE_SECRET}
        audience: crm-api
    - name: slow
      upstream: %s
      timeout: 200ms
`, jwks.URL, crm.URL, tokenServer.URL, slow.URL)
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OMNISWITCH_CONFIG", path)
	t.Setenv("TEST_MCP_EXCHANGE_SECRET", "exchange-secret")
	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatalf("loadRuntimeSettings() error = %v", err)
	}

	st := newTestStore(t)
	mcpHandler, err := buildMCPHandler(settings, st)
	if err != nil {
		t.Fatalf("buildMCPHandler() error = %v", err)
	}
	t.Cleanup(mcpHandler.Close)
	registry := provider.NewRegistry()
	registry.Register(fakeHealthProvider{name: "fake"})
	mux := newGatewayMux(routeHandlers{
		settings: settings, registry: registry, admin: admin.New(st), mcp: mcpHandler, dash: dashboard.New(st),
		gateway: gateway.New(registry, router.New(registry), st, guardrail.NewEngine()),
	})
	handler, closeRateLimiter, err := wrapGatewayMiddleware(settings, st, mux)
	if err != nil {
		t.Fatalf("wrapGatewayMiddleware() error = %v", err)
	}
	t.Cleanup(closeRateLimiter)

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "alice", "iss": "https://issuer.example.test", "aud": "omniswitch", "role": "member",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	token.Header["kid"] = "mcp-test"
	callerToken, err := token.SignedString(signingKey)
	if err != nil {
		t.Fatal(err)
	}
	call := func(body string) *httptest.ResponseRecorder {
		return doRequest(handler, http.MethodPost, "/mcp", callerToken, body, map[string]string{"Content-Type": "application/json"})
	}

	// The upstream gets the exchanged token, never the caller's own token.
	rec := call(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"crm__get_record","arguments":{"id":"42"}}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "record 42") {
		t.Fatalf("crm__get_record = %d %s, want the upstream result", rec.Code, rec.Body.String())
	}
	mu.Lock()
	if len(upstreamCalls) != 1 || upstreamCalls[0] != "get_record Bearer crm-token-for-crm-api" {
		t.Fatalf("upstream calls = %q, want one call with the exchanged token", upstreamCalls)
	}
	if len(exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(exchanges))
	}
	form := exchanges[0]
	if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" || form.Get("subject_token") != callerToken ||
		form.Get("client_id") != "omniswitch" || form.Get("client_secret") != "exchange-secret" || form.Get("audience") != "crm-api" {
		t.Fatalf("exchange form = %v, want the RFC 8693 request with the expanded secret", form)
	}
	mu.Unlock()

	// The target metadata reaches the policy: the default policy protects
	// deletes on production repositories, and the call names neither.
	rec = call(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"crm__delete_record","arguments":{"id":"42"}}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "is protected") {
		t.Fatalf("crm__delete_record = %d %s, want a policy denial", rec.Code, rec.Body.String())
	}
	mu.Lock()
	if len(upstreamCalls) != 1 {
		t.Fatalf("upstream calls = %q, want the denied call kept from the upstream", upstreamCalls)
	}
	mu.Unlock()

	// The target timeout bounds a call to an upstream that never answers.
	start := time.Now()
	rec = call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"slow__wait","arguments":{}}}`)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("slow__wait took %s, want the 200ms target timeout to stop it", elapsed)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("slow__wait = %d %s, want a JSON-RPC error", rec.Code, rec.Body.String())
	}
}
