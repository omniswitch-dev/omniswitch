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
	checks []Check
}

type Check func(input GuardrailInput) Result

type GuardrailInput struct {
	Messages []provider.Message
	Response string
	IsInput  bool
}

func NewEngine() *Engine {
	return &Engine{checks: []Check{checkPII, checkPromptInjection, checkSQLInjection, checkToxicContent, checkCodeLeakage}}
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
			triggered = append(triggered, r)
		}
	}
	return triggered
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
