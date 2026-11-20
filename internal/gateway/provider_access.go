package gateway

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/router"
)

// providerPolicyCacheScope isolates entries produced under different egress
// policies, including when the configured cache scope is shared across keys.
// The separate namespace also excludes existing entries with no provenance.
func providerPolicyCacheScope(scope string, allowed []string) string {
	if len(allowed) == 0 {
		return scope
	}
	names := make([]string, len(allowed))
	for i, name := range allowed {
		names[i] = strings.ToLower(name)
	}
	slices.Sort(names)
	policy, _ := json.Marshal(slices.Compact(names))
	return fmt.Sprintf("provider-policy:%x|%s", sha256.Sum256(policy), scope)
}

func writeProviderError(w http.ResponseWriter, prefix string, err error) {
	if errors.Is(err, router.ErrProviderAccessDenied) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": map[string]string{
				"message": router.ErrProviderAccessDenied.Error(),
				"type":    "permission_error",
				"code":    "provider_access_denied",
			},
		})
		return
	}
	writeError(w, http.StatusBadGateway, prefix+err.Error())
}

func providerErrorLogStatus(err error) string {
	if errors.Is(err, router.ErrProviderAccessDenied) {
		return statusDenied
	}
	return statusError
}
