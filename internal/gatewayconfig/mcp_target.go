package gatewayconfig

import (
	"fmt"
	"net/url"
	"strings"
)

// validateMCPTargetOptions checks the timeout, metadata and token exchange
// of mcp.targets[index]. Validate has already checked the transport.
func validateMCPTargetOptions(index int, target MCPTarget) error {
	field := fmt.Sprintf("mcp.targets[%d]", index)
	if target.Timeout != nil && target.Timeout.Duration <= 0 {
		return fmt.Errorf("%s.timeout must be greater than zero", field)
	}
	for key := range target.Metadata {
		name := strings.TrimSpace(key)
		if name == "" {
			return fmt.Errorf("%s.metadata keys must not be empty", field)
		}
		if strings.HasPrefix(strings.ToLower(name), "mcp.") {
			return fmt.Errorf("%s.metadata key %q is reserved: the gateway sets keys that start with \"mcp.\"", field, key)
		}
	}
	te := target.TokenExchange
	if te == nil {
		return nil
	}
	// The proxy exchanges tokens only for calls it forwards to an HTTP MCP
	// upstream. Reject every setup where the exchange would silently do
	// nothing or where its token would be replaced.
	if strings.EqualFold(strings.TrimSpace(target.Transport), "stdio") {
		return fmt.Errorf("%s.token_exchange needs an HTTP transport: stdio targets get no bearer token", field)
	}
	if strings.TrimSpace(target.OpenAPISpec) != "" {
		return fmt.Errorf("%s.token_exchange cannot be used with openapi_spec: OpenAPI tool calls send only the target headers", field)
	}
	if target.ForwardBearerToken != nil && *target.ForwardBearerToken {
		return fmt.Errorf("%s: set forward_bearer_token or token_exchange, not both", field)
	}
	for name := range target.Headers {
		if strings.EqualFold(strings.TrimSpace(name), "authorization") {
			return fmt.Errorf("%s.headers must not set Authorization when token_exchange is set: the header would replace the exchanged token", field)
		}
	}
	parsed, err := url.Parse(strings.TrimSpace(te.TokenURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%s.token_exchange.token_url must be an absolute HTTP(S) URL", field)
	}
	if strings.TrimSpace(te.ClientID) == "" {
		return fmt.Errorf("%s.token_exchange.client_id is required", field)
	}
	if te.TokenCacheTTL != nil && te.TokenCacheTTL.Duration < 0 {
		return fmt.Errorf("%s.token_exchange.token_cache_ttl must not be negative", field)
	}
	if strings.TrimSpace(te.ActorTokenType) != "" && strings.TrimSpace(te.ActorToken) == "" {
		return fmt.Errorf("%s.token_exchange.actor_token_type needs actor_token", field)
	}
	return nil
}
