package spec

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	APIVersion = "omniswitch.dev/v1"
	KindPolicy = "Policy"
	EffectDeny = "deny"
)

type Policy struct {
	APIVersion string     `json:"apiVersion" yaml:"apiVersion"`
	Kind       string     `json:"kind" yaml:"kind"`
	Metadata   Metadata   `json:"metadata" yaml:"metadata"`
	Spec       PolicySpec `json:"spec" yaml:"spec"`
}

type Metadata struct {
	Name    string            `json:"name" yaml:"name"`
	Version string            `json:"version,omitempty" yaml:"version,omitempty"`
	Labels  map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

type PolicySpec struct {
	Match  Match  `json:"match,omitempty" yaml:"match,omitempty"`
	CEL    string `json:"cel,omitempty" yaml:"cel,omitempty"`
	Effect string `json:"effect" yaml:"effect"`
	Reason string `json:"reason" yaml:"reason"`
}

type Match struct {
	Agent        string `json:"agent,omitempty" yaml:"agent,omitempty"`
	Department   string `json:"department,omitempty" yaml:"department,omitempty"`
	Role         string `json:"role,omitempty" yaml:"role,omitempty"`
	Tool         string `json:"tool,omitempty" yaml:"tool,omitempty"`
	Action       string `json:"action,omitempty" yaml:"action,omitempty"`
	Resource     string `json:"resource,omitempty" yaml:"resource,omitempty"`
	ResourceType string `json:"resourceType,omitempty" yaml:"resourceType,omitempty"`
	Environment  string `json:"environment,omitempty" yaml:"environment,omitempty"`
}

func LoadPolicy(path string) (Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, fmt.Errorf("read policy spec: %w", err)
	}

	var policy Policy
	if err := yaml.Unmarshal(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}), &policy); err != nil {
		return Policy{}, fmt.Errorf("parse policy spec: %w", err)
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func (p Policy) Validate() error {
	if p.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %s", APIVersion)
	}
	if p.Kind != KindPolicy {
		return fmt.Errorf("kind must be %s", KindPolicy)
	}
	if strings.TrimSpace(p.Metadata.Name) == "" {
		return fmt.Errorf("metadata.name is required")
	}
	effect := strings.ToLower(strings.TrimSpace(p.Spec.Effect))
	if effect == "" {
		effect = EffectDeny
	}
	if effect != EffectDeny {
		return fmt.Errorf("unsupported policy effect %q", p.Spec.Effect)
	}
	if strings.TrimSpace(p.Spec.Reason) == "" {
		return fmt.Errorf("spec.reason is required")
	}
	if strings.TrimSpace(p.Spec.CEL) == "" && p.Spec.Match.Empty() {
		return fmt.Errorf("spec.match or spec.cel is required")
	}
	return nil
}

func (p Policy) Expression() (string, error) {
	if strings.TrimSpace(p.Spec.CEL) != "" {
		return strings.TrimSpace(p.Spec.CEL), nil
	}

	parts := []string{}
	add := func(field, value string) {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, fmt.Sprintf("%s == %s", field, strconv.Quote(value)))
		}
	}

	add("agent.id", p.Spec.Match.Agent)
	add("agent.department", p.Spec.Match.Department)
	add("agent.role", p.Spec.Match.Role)
	add("tool.name", p.Spec.Match.Tool)
	add("action.name", p.Spec.Match.Action)
	add("resource.name", p.Spec.Match.Resource)
	add("resource.type", p.Spec.Match.ResourceType)
	add("resource.environment", p.Spec.Match.Environment)

	if len(parts) == 0 {
		return "", fmt.Errorf("spec.match does not contain any supported fields")
	}
	return strings.Join(parts, " &&\n"), nil
}

func (m Match) Empty() bool {
	return m.Agent == "" &&
		m.Department == "" &&
		m.Role == "" &&
		m.Tool == "" &&
		m.Action == "" &&
		m.Resource == "" &&
		m.ResourceType == "" &&
		m.Environment == ""
}
