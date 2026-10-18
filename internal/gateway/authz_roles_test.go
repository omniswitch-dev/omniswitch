package gateway

import (
	"net/http"
	"testing"
)

func TestRequiredRoleTable(t *testing.T) {
	cases := []struct {
		method, path, want string
	}{
		{http.MethodPost, "/v1/chat/completions", ""},
		{http.MethodGet, "/api/health", ""},
		{http.MethodGet, "/api/logs", "viewer"},
		{http.MethodGet, "/api/metrics", "viewer"},
		{http.MethodGet, "/api/traces/abc", "viewer"},
		{http.MethodGet, "/api/analytics/cost", "viewer"},
		{http.MethodGet, "/metrics", "viewer"},
		// SEC-2: secret-bearing reads need admin.
		{http.MethodGet, "/api/keys", "admin"},
		{http.MethodGet, "/api/users", "admin"},
		{http.MethodGet, "/api/workspace-members", "admin"},
		{http.MethodGet, "/api/virtual-keys", "admin"},
		{http.MethodGet, "/api/config/raw", "admin"},
		{http.MethodGet, "/api/configs", "admin"},
		{http.MethodHead, "/api/keys", "admin"},
		{http.MethodGet, "/api/orgs", "viewer"},
		{http.MethodPost, "/api/orgs", "admin"},
		// SEC-4: only GET/HEAD/OPTIONS are reads; everything else fails closed.
		{http.MethodGet, "/api/prompts/render", "viewer"},
		{http.MethodPost, "/api/prompts/render", "member"},
		{"PROPFIND", "/api/prompts/render", "member"},
		{http.MethodPut, "/api/prompts", "member"},
		{http.MethodPost, "/api/evals/policy", "member"},
		{http.MethodPost, "/api/feedback", "viewer"},
		{http.MethodPost, "/api/unknown", "admin"},
		{"BREW", "/api/logs", "admin"},
		{http.MethodOptions, "/api/logs", "viewer"},
	}
	for _, tc := range cases {
		if got := requiredRole(tc.method, tc.path); got != tc.want {
			t.Errorf("requiredRole(%s, %s) = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}
