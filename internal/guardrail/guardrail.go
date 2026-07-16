package guardrail

import (
	"regexp"
	"strings"
	"time"

	"sentinel/internal/provider"
)

type Result struct {
	Triggered bool   `json:"triggered"`
	Type      string `json:"type"`
	Action    string `json:"action"`
	Message   string `json:"message"`
	Details   string `json:"details,omitempty"`
}

type Engine struct {
	checks  []Check
	actions map[string]string
	rules   []Rule
}

type Check func(input GuardrailInput) Result

type GuardrailInput struct {
	Messages []provider.Message
	Response string
	IsInput  bool
}

// Rule adds a declarative, deterministic check to the built-in guardrail
// chain. Stage is input, output, or both and Action can be deny, warn, log, or
// redact. Regex validation happens when the engine is constructed.
type Rule struct {
	Name    string `json:"name" yaml:"name"`
	Stage   string `json:"stage,omitempty" yaml:"stage,omitempty"`
	Pattern string `json:"pattern" yaml:"pattern"`
	Action  string `json:"action,omitempty" yaml:"action,omitempty"`
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
}

type Config struct {
	Actions map[string]string `json:"actions,omitempty" yaml:"actions,omitempty"`
	Rules   []Rule            `json:"rules,omitempty" yaml:"rules,omitempty"`
}

func NewEngine() *Engine {
	return NewEngineWithConfig(Config{})
}

func NewEngineWithConfig(config Config) *Engine {
	actions := map[string]string{}
	for kind, action := range config.Actions {
		if validAction(action) {
			actions[kind] = action
		}
	}
	rules := make([]Rule, 0, len(config.Rules))
	for _, rule := range config.Rules {
		if strings.TrimSpace(rule.Name) == "" || strings.TrimSpace(rule.Pattern) == "" {
			continue
		}
		if _, err := regexp.Compile(rule.Pattern); err != nil {
			continue
		}
		if rule.Stage == "" {
			rule.Stage = "both"
		}
		if !validAction(rule.Action) {
			rule.Action = "deny"
		}
		if rule.Message == "" {
			rule.Message = "Custom guardrail triggered: " + rule.Name
		}
		rules = append(rules, rule)
	}
	return &Engine{checks: []Check{checkPII, checkPromptInjection, checkSQLInjection, checkToxicContent, checkCodeLeakage}, actions: actions, rules: rules}
}

func (e *Engine) EvaluateInput(messages []provider.Message) []Result {
	return e.evaluate(GuardrailInput{Messages: messages, IsInput: true})
}

func (e *Engine) EvaluateOutput(response string) []Result {
	return e.evaluate(GuardrailInput{Response: response, IsInput: false})
}

func (e *Engine) evaluate(input GuardrailInput) []Result {
	var triggered []Result
	for _, check := range e.checks {
		if r := check(input); r.Triggered {
			if action := e.actions[r.Type]; action != "" {
				r.Action = action
			}
			triggered = append(triggered, r)
		}
	}
	text := extractText(input)
	stage := "output"
	if input.IsInput {
		stage = "input"
	}
	for _, rule := range e.rules {
		if rule.Stage != "both" && rule.Stage != stage {
			continue
		}
		pattern, err := regexp.Compile(rule.Pattern)
		if err != nil || !pattern.MatchString(text) {
			continue
		}
		triggered = append(triggered, Result{Triggered: true, Type: rule.Name, Action: rule.Action, Message: rule.Message, Details: rule.Pattern})
	}
	return triggered
}

func validAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "deny", "warn", "log", "redact":
		return true
	default:
		return false
	}
}

var piiPatterns = map[string]*regexp.Regexp{
	"email":       regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`),
	"phone":       regexp.MustCompile(`\b\d{3}[-.]?\d{3}[-.]?\d{4}\b`),
	"ssn":         regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
	"credit_card": regexp.MustCompile(`\b\d{4}[-\s]?\d{4}[-\s]?\d{4}[-\s]?\d{4}\b`),
}

func checkPII(input GuardrailInput) Result {
	text := extractText(input)
	for piiType, pattern := range piiPatterns {
		if pattern.MatchString(text) {
			return Result{Triggered: true, Type: "pii", Action: "warn", Message: "PII detected: " + piiType}
		}
	}
	return Result{Type: "pii"}
}

var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?previous\s+instructions`),
	regexp.MustCompile(`(?i)disregard\s+(all\s+)?(previous|prior|above)`),
	regexp.MustCompile(`(?i)you\s+are\s+now\s+(a|an|the)`),
	regexp.MustCompile(`(?i)forget\s+(everything|all|your)`),
	regexp.MustCompile(`(?i)system\s*prompt\s*:`),
	regexp.MustCompile(`(?i)jailbreak`),
}

func checkPromptInjection(input GuardrailInput) Result {
	if !input.IsInput {
		return Result{Type: "injection"}
	}
	text := extractText(input)
	for _, p := range injectionPatterns {
		if p.MatchString(text) {
			return Result{Triggered: true, Type: "injection", Action: "deny", Message: "Prompt injection detected"}
		}
	}
	return Result{Type: "injection"}
}

var sqlPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(\b(DROP|DELETE|TRUNCATE)\s+(TABLE|DATABASE))`),
	regexp.MustCompile(`(?i)(\bUNION\s+SELECT\b)`),
}

func checkSQLInjection(input GuardrailInput) Result {
	if !input.IsInput {
		return Result{Type: "sql_injection"}
	}
	for _, p := range sqlPatterns {
		if p.MatchString(extractText(input)) {
			return Result{Triggered: true, Type: "sql_injection", Action: "deny", Message: "SQL injection detected"}
		}
	}
	return Result{Type: "sql_injection"}
}

func checkToxicContent(input GuardrailInput) Result {
	text := strings.ToLower(extractText(input))
	for _, p := range []string{"kill yourself", "go die", "kys"} {
		if strings.Contains(text, p) {
			return Result{Triggered: true, Type: "toxic", Action: "deny", Message: "Toxic content detected"}
		}
	}
	return Result{Type: "toxic"}
}

func checkCodeLeakage(input GuardrailInput) Result {
	if input.IsInput {
		return Result{Type: "code_leakage"}
	}
	secrets := []*regexp.Regexp{
		regexp.MustCompile(`sk-[a-zA-Z0-9]{32,}`),
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`ghp_[a-zA-Z0-9]{36}`),
	}
	for _, p := range secrets {
		if p.MatchString(input.Response) {
			return Result{Triggered: true, Type: "code_leakage", Action: "warn", Message: "Secret detected in output"}
		}
	}
	return Result{Type: "code_leakage"}
}

func extractText(input GuardrailInput) string {
	if input.Response != "" {
		return input.Response
	}
	var parts []string
	for _, msg := range input.Messages {
		parts = append(parts, msg.Text())
	}
	return strings.Join(parts, "\n")
}

func Now() time.Time { return time.Now().UTC() }
