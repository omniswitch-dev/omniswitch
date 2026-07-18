package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/cel-go/cel"
)

// AuthorizationRule is a CEL-based allow or deny decision evaluated after a
// caller is authenticated. Expressions can reference method, path, model,
// api_key_id, subject, workspace_id, organization_id, role, and claims.
type AuthorizationRule struct {
	Name    string
	When    string
	Effect  string
	Message string
}

type compiledAuthorizationRule struct {
	AuthorizationRule
	program cel.Program
}

// AuthorizationPolicy evaluates configured authorization rules. Deny rules
// always win. When one or more allow rules exist, at least one allow rule must
// match; this makes it safe to declare a least-privilege model allow-list.
type AuthorizationPolicy struct {
	rules    []compiledAuthorizationRule
	hasAllow bool
}

func NewAuthorizationPolicy(rules []AuthorizationRule) (*AuthorizationPolicy, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	env, err := cel.NewEnv(
		cel.Variable("method", cel.StringType),
		cel.Variable("path", cel.StringType),
		cel.Variable("model", cel.StringType),
		cel.Variable("api_key_id", cel.StringType),
		cel.Variable("subject", cel.StringType),
		cel.Variable("workspace_id", cel.StringType),
		cel.Variable("organization_id", cel.StringType),
		cel.Variable("role", cel.StringType),
		cel.Variable("claims", cel.MapType(cel.StringType, cel.DynType)),
	)
	if err != nil {
		return nil, fmt.Errorf("create authorization CEL environment: %w", err)
	}

	policy := &AuthorizationPolicy{rules: make([]compiledAuthorizationRule, 0, len(rules))}
	for index, rule := range rules {
		rule.Name = strings.TrimSpace(rule.Name)
		if rule.Name == "" {
			rule.Name = fmt.Sprintf("rule-%d", index+1)
		}
		rule.When = strings.TrimSpace(rule.When)
		if rule.When == "" {
			return nil, fmt.Errorf("authorization rule %q requires when", rule.Name)
		}
		rule.Effect = strings.ToLower(strings.TrimSpace(rule.Effect))
		if rule.Effect == "" {
			rule.Effect = "deny"
		}
		if rule.Effect != "allow" && rule.Effect != "deny" {
			return nil, fmt.Errorf("authorization rule %q effect must be allow or deny", rule.Name)
		}
		if rule.Message == "" {
			rule.Message = "request is not authorized by policy " + rule.Name
		}
		ast, issues := env.Compile(rule.When)
		if issues != nil && issues.Err() != nil {
			return nil, fmt.Errorf("compile authorization rule %q: %w", rule.Name, issues.Err())
		}
		if ast.OutputType() != cel.BoolType {
			return nil, fmt.Errorf("authorization rule %q must return a boolean", rule.Name)
		}
		program, err := env.Program(ast)
		if err != nil {
			return nil, fmt.Errorf("create authorization rule %q: %w", rule.Name, err)
		}
		policy.rules = append(policy.rules, compiledAuthorizationRule{AuthorizationRule: rule, program: program})
		policy.hasAllow = policy.hasAllow || rule.Effect == "allow"
	}
	return policy, nil
}

func (p *AuthorizationPolicy) Evaluate(r *http.Request, identity Identity) (bool, string) {
	if p == nil {
		return true, ""
	}
	model, err := modelFromRequest(r)
	if err != nil {
		return false, "invalid request body for authorization policy"
	}
	input := map[string]any{
		"method":          r.Method,
		"path":            r.URL.Path,
		"model":           model,
		"api_key_id":      identity.APIKeyID,
		"subject":         identity.Subject,
		"workspace_id":    identity.WorkspaceID,
		"organization_id": identity.OrganizationID,
		"role":            identity.Role,
		"claims":          identity.Claims,
	}
	matchedAllow := false
	for _, rule := range p.rules {
		result, _, err := rule.program.Eval(input)
		if err != nil {
			return false, "authorization policy evaluation failed"
		}
		matched, ok := result.Value().(bool)
		if !ok {
			return false, "authorization policy returned an invalid result"
		}
		if !matched {
			continue
		}
		if rule.Effect == "deny" {
			return false, rule.Message
		}
		matchedAllow = true
	}
	if p.hasAllow && !matchedAllow {
		return false, "request does not match an authorization allow policy"
	}
	return true, ""
}

// modelFromRequest peeks at standard inference request bodies and restores the
// reader so downstream handlers receive the original payload unchanged.
func modelFromRequest(r *http.Request) (string, error) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/") || r.Body == nil {
		return "", nil
	}
	const maxAuthorizationBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAuthorizationBody+1))
	if err != nil {
		return "", err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > maxAuthorizationBody {
		return "", fmt.Errorf("request body exceeds authorization inspection limit")
	}
	var request struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return "", err
	}
	return request.Model, nil
}
