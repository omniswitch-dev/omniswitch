package trace

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/omniswitch-dev/omniswitch/internal/model"
	"github.com/omniswitch-dev/omniswitch/internal/spec"
)

const KindDecisionTrace = "DecisionTrace"

type Document struct {
	APIVersion string    `json:"apiVersion" yaml:"apiVersion"`
	Kind       string    `json:"kind" yaml:"kind"`
	Metadata   Metadata  `json:"metadata" yaml:"metadata"`
	Spec       TraceSpec `json:"spec" yaml:"spec"`
}

type Metadata struct {
	DecisionID string    `json:"decisionId" yaml:"decisionId"`
	Timestamp  time.Time `json:"timestamp" yaml:"timestamp"`
}

type TraceSpec struct {
	Request model.ToolRequest `json:"request" yaml:"request"`
	Policy  PolicyRef         `json:"policy" yaml:"policy"`
	Result  Result            `json:"result" yaml:"result"`
	Trace   []model.RuleTrace `json:"trace" yaml:"trace"`
}

type PolicyRef struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	Hash    string `json:"hash,omitempty" yaml:"hash,omitempty"`
	Source  string `json:"source,omitempty" yaml:"source,omitempty"`
}

type Result struct {
	Effect       string        `json:"effect" yaml:"effect"`
	Allowed      bool          `json:"allowed" yaml:"allowed"`
	Reason       string        `json:"reason" yaml:"reason"`
	EvaluationMs float64       `json:"evaluationMs" yaml:"evaluationMs"`
	Duration     time.Duration `json:"-" yaml:"-"`
}

func NewDocument(req model.ToolRequest, decision model.Decision) Document {
	policyRef := PolicyRef{Name: decision.Rule}
	if matched, ok := matchedRule(decision); ok {
		policyRef = PolicyRef{
			Name:    matched.Rule,
			Hash:    matched.Hash,
			Source:  matched.Source,
			Version: matched.Version,
		}
	}

	return Document{
		APIVersion: spec.APIVersion,
		Kind:       KindDecisionTrace,
		Metadata: Metadata{
			DecisionID: decision.ID,
			Timestamp:  time.Now().UTC(),
		},
		Spec: TraceSpec{
			Request: req,
			Policy:  policyRef,
			Result: Result{
				Effect:       strings.ToLower(decision.Status()),
				Allowed:      decision.Allowed,
				Reason:       decision.Reason,
				EvaluationMs: float64(decision.EvaluationTime.Microseconds()) / 1000,
				Duration:     decision.EvaluationTime,
			},
			Trace: decision.Trace,
		},
	}
}

func Load(path string) (Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, fmt.Errorf("read decision trace: %w", err)
	}

	var document Document
	if err := yaml.Unmarshal(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}), &document); err != nil {
		return Document{}, fmt.Errorf("parse decision trace: %w", err)
	}
	if err := document.Validate(); err != nil {
		return Document{}, err
	}
	return document, nil
}

func (d Document) MarshalYAMLBytes() ([]byte, error) {
	data, err := yaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshal decision trace: %w", err)
	}
	return data, nil
}

func (d Document) Validate() error {
	if d.APIVersion != spec.APIVersion {
		return fmt.Errorf("apiVersion must be %s", spec.APIVersion)
	}
	if d.Kind != KindDecisionTrace {
		return fmt.Errorf("kind must be %s", KindDecisionTrace)
	}
	if d.Metadata.DecisionID == "" {
		return fmt.Errorf("metadata.decisionId is required")
	}
	return nil
}

func Diff(left, right Document) []string {
	diffs := []string{}
	if left.Spec.Result.Allowed != right.Spec.Result.Allowed {
		diffs = append(diffs, fmt.Sprintf("result.allowed: %v -> %v", left.Spec.Result.Allowed, right.Spec.Result.Allowed))
	}
	if left.Spec.Result.Effect != right.Spec.Result.Effect {
		diffs = append(diffs, fmt.Sprintf("result.effect: %s -> %s", left.Spec.Result.Effect, right.Spec.Result.Effect))
	}
	if left.Spec.Result.Reason != right.Spec.Result.Reason {
		diffs = append(diffs, fmt.Sprintf("result.reason: %s -> %s", left.Spec.Result.Reason, right.Spec.Result.Reason))
	}
	if left.Spec.Policy.Hash != right.Spec.Policy.Hash {
		diffs = append(diffs, fmt.Sprintf("policy.hash: %s -> %s", left.Spec.Policy.Hash, right.Spec.Policy.Hash))
	}
	if left.Spec.Request.Tool.Name != right.Spec.Request.Tool.Name {
		diffs = append(diffs, fmt.Sprintf("request.tool: %s -> %s", left.Spec.Request.Tool.Name, right.Spec.Request.Tool.Name))
	}
	if left.Spec.Request.Action.Name != right.Spec.Request.Action.Name {
		diffs = append(diffs, fmt.Sprintf("request.action: %s -> %s", left.Spec.Request.Action.Name, right.Spec.Request.Action.Name))
	}
	if left.Spec.Request.Resource.Name != right.Spec.Request.Resource.Name {
		diffs = append(diffs, fmt.Sprintf("request.resource: %s -> %s", left.Spec.Request.Resource.Name, right.Spec.Request.Resource.Name))
	}
	if left.Spec.Request.Resource.Environment != right.Spec.Request.Resource.Environment {
		diffs = append(diffs, fmt.Sprintf("request.environment: %s -> %s", left.Spec.Request.Resource.Environment, right.Spec.Request.Resource.Environment))
	}
	return diffs
}

func matchedRule(decision model.Decision) (model.RuleTrace, bool) {
	for _, item := range decision.Trace {
		if item.Matched {
			return item, true
		}
	}
	if len(decision.Trace) > 0 {
		return decision.Trace[len(decision.Trace)-1], true
	}
	return model.RuleTrace{}, false
}
