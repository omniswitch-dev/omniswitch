package main

import (
	"os"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
)

// oidcStaleIfError reads OMNISWITCH_OIDC_STALE_IF_ERROR, which must be
// between 0 and gatewayconfig.MaxOIDCStaleIfError like
// identity.oidc.stale_if_error.
func (e *envReader) oidcStaleIfError(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 0 || parsed > gatewayconfig.MaxOIDCStaleIfError {
		e.fail(key, value, "a Go duration between 0 and 168h such as 24h")
		return fallback
	}
	return parsed
}
