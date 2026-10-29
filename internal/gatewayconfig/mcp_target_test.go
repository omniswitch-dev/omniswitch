package gatewayconfig

import (
	"strings"
	"testing"
	"time"
)

func TestParseMCPTargetTokenExchangeTimeoutAndMetadata(t *testing.T) {
	cfg, err := Parse([]byte(`
mcp:
  targets:
    - name: crm
      upstream: https://crm.example.test/mcp
      timeout: 45s
      metadata:
        environment: production
        resource_type: database
      token_exchange:
        token_url: https://idp.example.test/oauth2/token
        client_id: omniswitch
        client_secret: ${CRM_EXCHANGE_SECRET}
        audience: crm-api
        scope: crm.read
        resource: https://crm.example.test/
        requested_token_type: urn:ietf:params:oauth:token-type:access_token
        token_cache_ttl: 5m
`), ".yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	target := cfg.MCP.Targets[0]
	if target.Timeout == nil || target.Timeout.Duration != 45*time.Second {
		t.Fatalf("timeout = %v, want 45s", target.Timeout)
	}
	if target.Metadata["environment"] != "production" || target.Metadata["resource_type"] != "database" {
		t.Fatalf("metadata = %v, want environment and resource_type", target.Metadata)
	}
	te := target.TokenExchange
	if te == nil {
		t.Fatal("token_exchange = nil, want a parsed exchange")
	}
	if te.TokenURL != "https://idp.example.test/oauth2/token" || te.ClientID != "omniswitch" || te.ClientSecret != "${CRM_EXCHANGE_SECRET}" ||
		te.Audience != "crm-api" || te.Scope != "crm.read" || te.Resource != "https://crm.example.test/" ||
		te.RequestedTokenType != "urn:ietf:params:oauth:token-type:access_token" {
		t.Fatalf("token_exchange = %+v, want the configured values (secrets expand later)", te)
	}
	if te.TokenCacheTTL == nil || te.TokenCacheTTL.Duration != 5*time.Minute {
		t.Fatalf("token_cache_ttl = %v, want 5m", te.TokenCacheTTL)
	}
}

func TestParseRejectsUnknownTokenExchangeField(t *testing.T) {
	_, err := Parse([]byte(`mcp: {targets: [{name: crm, upstream: https://crm.example.test/mcp, token_exchange: {token_url: https://idp.example.test/token, client_id: c, client_secrt: typo}}]}`), ".yaml")
	if err == nil || !strings.Contains(err.Error(), "client_secrt") {
		t.Fatalf("Parse() error = %v, want an unknown field error", err)
	}
}

func TestValidateMCPTargetOptions(t *testing.T) {
	const exchange = `token_exchange: {token_url: "https://idp.example.test/token", client_id: omniswitch, client_secret: s}`
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{"zero timeout", `{name: crm, upstream: "https://crm.example.test/mcp", timeout: 0s}`, "mcp.targets[0].timeout must be greater than zero"},
		{"negative timeout", `{name: crm, upstream: "https://crm.example.test/mcp", timeout: -1s}`, "mcp.targets[0].timeout must be greater than zero"},
		{"empty metadata key", `{name: crm, upstream: "https://crm.example.test/mcp", metadata: {" ": production}}`, "metadata keys must not be empty"},
		{"reserved metadata key", `{name: crm, upstream: "https://crm.example.test/mcp", metadata: {MCP.Target: other}}`, `metadata key "MCP.Target" is reserved`},
		{"stdio exchange", `{name: local, transport: stdio, command: npx, ` + exchange + `}`, "token_exchange needs an HTTP transport"},
		{"openapi exchange", `{name: pets, upstream: "https://pets.example.test", openapi_spec: ./pets.yaml, ` + exchange + `}`, "cannot be used with openapi_spec"},
		{"forward and exchange", `{name: crm, upstream: "https://crm.example.test/mcp", forward_bearer_token: true, ` + exchange + `}`, "set forward_bearer_token or token_exchange, not both"},
		{"authorization header", `{name: crm, upstream: "https://crm.example.test/mcp", headers: {authorization: "Bearer static"}, ` + exchange + `}`, "headers must not set Authorization"},
		{"missing token url", `{name: crm, upstream: "https://crm.example.test/mcp", token_exchange: {client_id: c}}`, "token_exchange.token_url must be an absolute HTTP(S) URL"},
		{"relative token url", `{name: crm, upstream: "https://crm.example.test/mcp", token_exchange: {token_url: /token, client_id: c}}`, "token_exchange.token_url must be an absolute HTTP(S) URL"},
		{"file token url", `{name: crm, upstream: "https://crm.example.test/mcp", token_exchange: {token_url: "file:///token", client_id: c}}`, "token_exchange.token_url must be an absolute HTTP(S) URL"},
		{"missing client id", `{name: crm, upstream: "https://crm.example.test/mcp", token_exchange: {token_url: "https://idp.example.test/token"}}`, "token_exchange.client_id is required"},
		{"negative cache ttl", `{name: crm, upstream: "https://crm.example.test/mcp", token_exchange: {token_url: "https://idp.example.test/token", client_id: c, token_cache_ttl: -1m}}`, "token_cache_ttl must not be negative"},
		{"actor type without token", `{name: crm, upstream: "https://crm.example.test/mcp", token_exchange: {token_url: "https://idp.example.test/token", client_id: c, actor_token_type: "urn:ietf:params:oauth:token-type:jwt"}}`, "actor_token_type needs actor_token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(`mcp: {targets: [`+tt.target+`]}`), ".yaml")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want %q", err, tt.want)
			}
		})
	}

	valid := []string{
		`{name: crm, upstream: "https://crm.example.test/mcp", ` + exchange + `}`,
		`{name: crm, transport: streamable_http, upstream: "http://127.0.0.1:9000/mcp", forward_bearer_token: false, headers: {x-api-key: k}, ` + exchange + `}`,
		`{name: local, transport: stdio, command: npx, timeout: 10s, metadata: {environment: staging}}`,
		`{name: crm, upstream: "https://crm.example.test/mcp", token_exchange: {token_url: "https://idp.example.test/token", client_id: c, token_cache_ttl: 0s, actor_token: "${ACTOR}", actor_token_type: "urn:ietf:params:oauth:token-type:jwt"}}`,
	}
	for _, target := range valid {
		if _, err := Parse([]byte(`mcp: {targets: [`+target+`]}`), ".yaml"); err != nil {
			t.Errorf("Parse(%s) error = %v, want valid", target, err)
		}
	}
}
