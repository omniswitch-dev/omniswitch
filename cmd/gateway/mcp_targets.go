package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	mcpproxy "github.com/omniswitch-dev/omniswitch/internal/proxy"
)

// mcpTokenExchange converts a configured token exchange into the proxy
// form. The client credentials and the actor token expand ${VAR}
// references so that secrets can stay out of the config file. A value that
// expands to nothing is an error, so a missing variable stops startup
// instead of failing every exchange later.
func mcpTokenExchange(targetName string, configured *gatewayconfig.MCPTokenExchange) (*mcpproxy.TokenExchangeConfig, error) {
	if configured == nil {
		return nil, nil
	}
	expanded := map[string]string{}
	for name, value := range map[string]string{
		"client_id":     configured.ClientID,
		"client_secret": configured.ClientSecret,
		"actor_token":   configured.ActorToken,
	} {
		expanded[name] = strings.TrimSpace(os.ExpandEnv(value))
		if strings.TrimSpace(value) != "" && expanded[name] == "" {
			return nil, fmt.Errorf("MCP target %q token_exchange.%s is empty after expanding environment variables", targetName, name)
		}
	}
	exchange := &mcpproxy.TokenExchangeConfig{
		TokenURL:           strings.TrimSpace(configured.TokenURL),
		ClientID:           expanded["client_id"],
		ClientSecret:       expanded["client_secret"],
		SubjectTokenType:   configured.SubjectTokenType,
		RequestedTokenType: configured.RequestedTokenType,
		Scope:              configured.Scope,
		Audience:           configured.Audience,
		Resource:           configured.Resource,
		ActorToken:         expanded["actor_token"],
		ActorTokenType:     configured.ActorTokenType,
	}
	if configured.TokenCacheTTL != nil {
		exchange.TokenCacheTTL = configured.TokenCacheTTL.Duration
	}
	return exchange, nil
}
