package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthorizationPolicyAppliesModelRulesToA2A(t *testing.T) {
	policy, err := NewAuthorizationPolicy([]AuthorizationRule{{
		Name:    "deny-gpt-4o",
		When:    `model == "gpt-4o" && role != "admin"`,
		Effect:  "deny",
		Message: "gpt-4o requires admin",
	}})
	if err != nil {
		t.Fatal(err)
	}
	member := Identity{APIKeyID: "k", Role: "member"}
	a2aBody := func(model string) string {
		return `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"parts":[{"text":"hi"}]},"metadata":{"model":"` + model + `"}}}`
	}
	cases := []struct {
		name    string
		path    string
		body    string
		header  string
		allowed bool
	}{
		{"v1 denied", "/v1/chat/completions", `{"model":"gpt-4o"}`, "", false},
		{"a2a metadata denied", "/a2a", a2aBody("gpt-4o"), "", false},
		{"a2a header denied", "/a2a", `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{}}`, "gpt-4o", false},
		{"a2a metadata wins over header", "/a2a", a2aBody("gpt-4o-mini"), "gpt-4o", true},
		{"a2a other model allowed", "/a2a", a2aBody("gpt-4o-mini"), "", true},
		{"a2a invalid json denied", "/a2a", `{`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			if tc.header != "" {
				req.Header.Set("x-omniswitch-model", tc.header)
			}
			allowed, _ := policy.Evaluate(req, member)
			if allowed != tc.allowed {
				t.Fatalf("allowed = %v, want %v", allowed, tc.allowed)
			}
			rest, _ := io.ReadAll(req.Body)
			if string(rest) != tc.body {
				t.Fatalf("body not restored: %q", rest)
			}
		})
	}
	admin := Identity{APIKeyID: "k", Role: "admin"}
	req := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(a2aBody("gpt-4o")))
	if allowed, _ := policy.Evaluate(req, admin); !allowed {
		t.Fatal("admin denied")
	}
}
