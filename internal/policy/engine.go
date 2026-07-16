package policy

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/cel-go/cel"

	"github.com/omniswitch-dev/omniswitch/internal/model"
	"github.com/omniswitch-dev/omniswitch/internal/spec"
)

type Engine interface {
	Evaluate(ctx context.Context, req model.ToolRequest) (model.Decision, error)
}

type Rule struct {
	Name       string
	Expression string
	Reason     string
	Effect     string
	Source     string
	Version    string
	Hash       string
}

type CELEngine struct {
	env   *cel.Env
	rules []compiledRule
}

type compiledRule struct {
	rule    Rule
	program cel.Program
}

func NewEngine(rules ...Rule) (*CELEngine, error) {
	env, err := newEnvironment()
	if err != nil {
		return nil, err
	}

	engine := &CELEngine{env: env}
	for _, rule := range rules {
		compiled, err := engine.compileRule(rule)
		if err != nil {
			return nil, err
		}
		engine.rules = append(engine.rules, compiled)
	}

	return engine, nil
}

func NewEngineFromFiles(paths ...string) (*CELEngine, error) {
	rules := make([]Rule, 0, len(paths))
	for _, path := range paths {
		rule, err := RuleFromFile(path)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}

	return NewEngine(rules...)
}

func ValidateRule(expression string) error {
	engine, err := NewEngine(Rule{Name: "validate", Expression: expression})
	if err != nil {
		return err
	}
	if len(engine.rules) != 1 {
		return errors.New("policy did not compile")
	}
	return nil
}

func RuleFromFile(path string) (Rule, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return ruleFromSpecFile(path)
	}

	file, err := os.Open(path)
	if err != nil {
		return Rule{}, fmt.Errorf("open policy file: %w", err)
	}
	defer file.Close()

	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	reason := ""
	version := ""
	expressionLines := []string{}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimPrefix(scanner.Text(), "\uFEFF")
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "// name:"):
			name = strings.TrimSpace(strings.TrimPrefix(trimmed, "// name:"))
		case strings.HasPrefix(trimmed, "// reason:"):
			reason = strings.TrimSpace(strings.TrimPrefix(trimmed, "// reason:"))
		case strings.HasPrefix(trimmed, "// version:"):
			version = strings.TrimSpace(strings.TrimPrefix(trimmed, "// version:"))
		case strings.HasPrefix(trimmed, "//"):
			continue
		default:
			expressionLines = append(expressionLines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return Rule{}, fmt.Errorf("read policy file: %w", err)
	}

	return Rule{
		Name:       name,
		Expression: strings.TrimSpace(strings.Join(expressionLines, "\n")),
		Reason:     reason,
		Effect:     spec.EffectDeny,
		Source:     path,
		Version:    version,
	}, nil
}

func (e *CELEngine) Evaluate(ctx context.Context, req model.ToolRequest) (model.Decision, error) {
	start := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}

	finish := func(decision model.Decision) model.Decision {
		if decision.ID == "" {
			decision.ID = newDecisionID()
		}
		decision.EvaluationTime = time.Since(start)
		if decision.EvaluationTime <= 0 {
			decision.EvaluationTime = time.Nanosecond
		}
		return decision
	}

	if e == nil || e.env == nil {
		decision := finish(model.Decision{
			Allowed: false,
			Rule:    "engine",
			Reason:  "Policy engine is not initialized",
		})
		return decision, errors.New(decision.Reason)
	}

	traces := []model.RuleTrace{}
	for _, compiled := range e.rules {
		select {
		case <-ctx.Done():
			decision := finish(model.Decision{
				Allowed: false,
				Rule:    compiled.rule.Name,
				Reason:  "Policy evaluation was canceled",
				Trace:   traces,
			})
			return decision, ctx.Err()
		default:
		}

		output, _, err := compiled.program.Eval(activation(req))
		if err != nil {
			traces = append(traces, compiled.rule.trace(false))
			decision := finish(model.Decision{
				Allowed: false,
				Rule:    compiled.rule.Name,
				Reason:  fmt.Sprintf("Policy evaluation failed: %s", err),
				Trace:   traces,
			})
			return decision, fmt.Errorf("evaluate policy %q: %w", compiled.rule.Name, err)
		}

		violation, ok := output.Value().(bool)
		if !ok {
			traces = append(traces, compiled.rule.trace(false))
			decision := finish(model.Decision{
				Allowed: false,
				Rule:    compiled.rule.Name,
				Reason:  "Policy must evaluate to a boolean",
				Trace:   traces,
			})
			return decision, errors.New(decision.Reason)
		}

		traces = append(traces, compiled.rule.trace(violation))
		if violation {
			return finish(model.Decision{
				Allowed: false,
				Rule:    compiled.rule.Name,
				Reason:  renderReason(compiled.rule, req),
				Trace:   traces,
			}), nil
		}
	}

	return finish(model.Decision{
		Allowed: true,
		Rule:    "none",
		Reason:  "No policy violation detected.",
		Trace:   traces,
	}), nil
}

func newEnvironment() (*cel.Env, error) {
	stringMap := cel.MapType(cel.StringType, cel.StringType)
	env, err := cel.NewEnv(
		cel.Variable("agent", stringMap),
		cel.Variable("tool", stringMap),
		cel.Variable("action", stringMap),
		cel.Variable("resource", stringMap),
	)
	if err != nil {
		return nil, fmt.Errorf("create CEL environment: %w", err)
	}
	return env, nil
}

func (e *CELEngine) compileRule(rule Rule) (compiledRule, error) {
	if strings.TrimSpace(rule.Name) == "" {
		rule.Name = "inline"
	}
	if strings.TrimSpace(rule.Expression) == "" {
		return compiledRule{}, fmt.Errorf("policy %q is empty", rule.Name)
	}
	if strings.TrimSpace(rule.Reason) == "" {
		rule.Reason = fmt.Sprintf("Policy %s matched.", rule.Name)
	}
	if strings.TrimSpace(rule.Effect) == "" {
		rule.Effect = spec.EffectDeny
	}
	if strings.ToLower(rule.Effect) != spec.EffectDeny {
		return compiledRule{}, fmt.Errorf("policy %q has unsupported effect %q", rule.Name, rule.Effect)
	}
	rule.Effect = spec.EffectDeny
	if rule.Hash == "" {
		rule.Hash = ruleHash(rule)
	}

	ast, issues := e.env.Compile(rule.Expression)
	if issues != nil && issues.Err() != nil {
		return compiledRule{}, fmt.Errorf("compile policy %q: %w", rule.Name, issues.Err())
	}

	program, err := e.env.Program(ast)
	if err != nil {
		return compiledRule{}, fmt.Errorf("create policy program %q: %w", rule.Name, err)
	}

	return compiledRule{
		rule:    rule,
		program: program,
	}, nil
}

func ruleFromSpecFile(path string) (Rule, error) {
	document, err := spec.LoadPolicy(path)
	if err != nil {
		return Rule{}, err
	}
	expression, err := document.Expression()
	if err != nil {
		return Rule{}, err
	}
	return Rule{
		Name:       document.Metadata.Name,
		Expression: expression,
		Reason:     document.Spec.Reason,
		Effect:     spec.EffectDeny,
		Source:     path,
		Version:    document.Metadata.Version,
	}, nil
}

func (r Rule) trace(matched bool) model.RuleTrace {
	return model.RuleTrace{
		Rule:       r.Name,
		Expression: r.Expression,
		Effect:     r.Effect,
		Matched:    matched,
		Reason:     r.Reason,
		Source:     r.Source,
		Version:    r.Version,
		Hash:       r.Hash,
	}
}

func ruleHash(rule Rule) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{
		rule.Name,
		rule.Expression,
		rule.Effect,
		rule.Reason,
		rule.Version,
	}, "\x00")))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func newDecisionID() string {
	var entropy [6]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return fmt.Sprintf("dec_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("dec_%d_%s", time.Now().UnixNano(), hex.EncodeToString(entropy[:]))
}

func activation(req model.ToolRequest) map[string]any {
	return map[string]any{
		"agent": map[string]string{
			"id":         req.Agent.ID,
			"department": req.Agent.Department,
			"role":       req.Agent.Role,
		},
		"tool": map[string]string{
			"name": req.Tool.Name,
		},
		"action": map[string]string{
			"name": req.Action.Name,
		},
		"resource": map[string]string{
			"type":        req.Resource.Type,
			"name":        req.Resource.Name,
			"environment": req.Resource.Environment,
		},
	}
}

func renderReason(rule Rule, req model.ToolRequest) string {
	reason := rule.Reason
	replacements := map[string]string{
		"{{agent.id}}":             req.Agent.ID,
		"{{agent.department}}":     req.Agent.Department,
		"{{agent.role}}":           req.Agent.Role,
		"{{tool.name}}":            req.Tool.Name,
		"{{action.name}}":          req.Action.Name,
		"{{resource.type}}":        req.Resource.Type,
		"{{resource.name}}":        req.Resource.Name,
		"{{resource.environment}}": req.Resource.Environment,
		"{{session.id}}":           req.Session.ID,
	}
	for old, replacement := range replacements {
		reason = strings.ReplaceAll(reason, old, replacement)
	}
	return reason
}
