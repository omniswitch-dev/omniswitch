package guardrail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

type Result struct {
	Triggered bool   `json:"triggered"`
	Type      string `json:"type"`
	Action    string `json:"action"`
	Message   string `json:"message"`
	Details   string `json:"details,omitempty"`
}

type Engine struct {
	checks   []Check
	actions  map[string]string
	rules    []Rule
	webhooks []webhookCheck
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
	Actions  map[string]string `json:"actions,omitempty" yaml:"actions,omitempty"`
	Rules    []Rule            `json:"rules,omitempty" yaml:"rules,omitempty"`
	Webhooks []Webhook         `json:"webhooks,omitempty" yaml:"webhooks,omitempty"`
}

// Webhook delegates a guardrail decision to a managed or in-house service.
// The service receives stage/text/messages and responds with JSON such as
// {"triggered":true,"message":"...","details":"..."}. A false
// `allowed` field is also treated as a trigger.
type Webhook struct {
	Name     string            `json:"name" yaml:"name"`
	URL      string            `json:"url" yaml:"url"`
	Stage    string            `json:"stage,omitempty" yaml:"stage,omitempty"`
	Action   string            `json:"action,omitempty" yaml:"action,omitempty"`
	Headers  map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Timeout  time.Duration     `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	FailOpen bool              `json:"fail_open,omitempty" yaml:"fail_open,omitempty"`
}

type webhookCheck struct {
	config Webhook
	client *http.Client
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
	webhooks := make([]webhookCheck, 0, len(config.Webhooks))
	for _, webhook := range config.Webhooks {
		if normalized, ok := normalizeWebhook(webhook); ok {
			webhooks = append(webhooks, webhookCheck{config: normalized, client: &http.Client{Timeout: normalized.Timeout}})
		}
	}
	return &Engine{checks: []Check{checkPII, checkPromptInjection, checkSQLInjection, checkToxicContent, checkCodeLeakage}, actions: actions, rules: rules, webhooks: webhooks}
}

func (e *Engine) EvaluateInput(messages []provider.Message) []Result {
	return e.EvaluateInputContext(context.Background(), messages)
}

func (e *Engine) EvaluateOutput(response string) []Result {
	return e.EvaluateOutputContext(context.Background(), response)
}

func (e *Engine) EvaluateInputContext(ctx context.Context, messages []provider.Message) []Result {
	return e.evaluate(ctx, GuardrailInput{Messages: messages, IsInput: true})
}

func (e *Engine) EvaluateOutputContext(ctx context.Context, response string) []Result {
	return e.evaluate(ctx, GuardrailInput{Response: response, IsInput: false})
}

func (e *Engine) evaluate(ctx context.Context, input GuardrailInput) []Result {
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
	for _, webhook := range e.webhooks {
		if !webhook.appliesTo(stage) {
			continue
		}
		result, err := webhook.Evaluate(ctx, input)
		if err != nil {
			if webhook.config.FailOpen {
				continue
			}
			triggered = append(triggered, Result{Triggered: true, Type: "webhook:" + webhook.config.Name, Action: webhook.config.Action, Message: "External guardrail unavailable: " + webhook.config.Name, Details: err.Error()})
			continue
		}
		if result.Triggered {
			triggered = append(triggered, result)
		}
	}
	return triggered
}

func normalizeWebhook(webhook Webhook) (Webhook, bool) {
	webhook.Name = strings.TrimSpace(webhook.Name)
	webhook.URL = strings.TrimSpace(webhook.URL)
	if webhook.Name == "" || webhook.URL == "" {
		return Webhook{}, false
	}
	parsed, err := url.Parse(webhook.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return Webhook{}, false
	}
	webhook.Stage = strings.ToLower(strings.TrimSpace(webhook.Stage))
	if webhook.Stage == "" {
		webhook.Stage = "both"
	}
	if webhook.Stage != "input" && webhook.Stage != "output" && webhook.Stage != "both" {
		return Webhook{}, false
	}
	webhook.Action = strings.ToLower(strings.TrimSpace(webhook.Action))
	if webhook.Action == "" {
		webhook.Action = "deny"
	}
	if !validAction(webhook.Action) {
		return Webhook{}, false
	}
	if webhook.Timeout <= 0 {
		webhook.Timeout = 3 * time.Second
	}
	return webhook, true
}

func (webhook webhookCheck) appliesTo(stage string) bool {
	return webhook.config.Stage == "both" || webhook.config.Stage == stage
}

func (webhook webhookCheck) Evaluate(ctx context.Context, input GuardrailInput) (Result, error) {
	stage := "output"
	if input.IsInput {
		stage = "input"
	}
	payload, err := json.Marshal(struct {
		Stage    string             `json:"stage"`
		Text     string             `json:"text"`
		Messages []provider.Message `json:"messages,omitempty"`
	}{Stage: stage, Text: extractText(input), Messages: input.Messages})
	if err != nil {
		return Result{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook.config.URL, strings.NewReader(string(payload)))
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range webhook.config.Headers {
		request.Header.Set(key, value)
	}
	response, err := webhook.client.Do(request)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Result{}, fmt.Errorf("unexpected status %s", response.Status)
	}
	var decision struct {
		Triggered bool   `json:"triggered"`
		Allowed   *bool  `json:"allowed"`
		Message   string `json:"message"`
		Details   string `json:"details"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decision); err != nil {
		return Result{}, fmt.Errorf("decode response: %w", err)
	}
	triggered := decision.Triggered || (decision.Allowed != nil && !*decision.Allowed)
	if !triggered {
		return Result{}, nil
	}
	message := strings.TrimSpace(decision.Message)
	if message == "" {
		message = "External guardrail triggered: " + webhook.config.Name
	}
	return Result{Triggered: true, Type: "webhook:" + webhook.config.Name, Action: webhook.config.Action, Message: message, Details: decision.Details}, nil
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
