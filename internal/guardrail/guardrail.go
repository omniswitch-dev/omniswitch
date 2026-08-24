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

	"github.com/omniswitch-dev/omniswitch/internal/accel"
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
	checks    []Check
	actions   map[string]string
	rules     []Rule
	compiled  []compiledRule
	toolRules []ToolRule
	webhooks  []webhookCheck
	llmJudges []llmJudgeCheck

	// accelScanners hold the Rust-accelerated single-pass scanners for the
	// input and output stages. nil when acceleration is unavailable; any
	// pattern the accelerator rejects stays in compiled for the Go path.
	accelInput  *accel.Scanner
	accelOutput *accel.Scanner
}

type compiledRule struct {
	rule Rule
	re   *regexp.Regexp
}

type Check func(input GuardrailInput) Result

type GuardrailInput struct {
	Messages  []provider.Message
	Response  string
	ToolCalls []provider.ToolCall
	IsInput   bool
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
	Actions   map[string]string `json:"actions,omitempty" yaml:"actions,omitempty"`
	Rules     []Rule            `json:"rules,omitempty" yaml:"rules,omitempty"`
	ToolRules []ToolRule        `json:"tool_rules,omitempty" yaml:"tool_rules,omitempty"`
	Webhooks  []Webhook         `json:"webhooks,omitempty" yaml:"webhooks,omitempty"`
	LLMJudges []LLMJudge        `json:"llm_judges,omitempty" yaml:"llm_judges,omitempty"`
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

type ToolRule struct {
	Name             string `json:"name" yaml:"name"`
	Stage            string `json:"stage,omitempty" yaml:"stage,omitempty"`
	ToolName         string `json:"tool_name,omitempty" yaml:"tool_name,omitempty"`
	ArgumentsPattern string `json:"arguments_pattern,omitempty" yaml:"arguments_pattern,omitempty"`
	Action           string `json:"action,omitempty" yaml:"action,omitempty"`
	Message          string `json:"message,omitempty" yaml:"message,omitempty"`
}

type LLMJudge struct {
	Name         string            `json:"name" yaml:"name"`
	URL          string            `json:"url" yaml:"url"`
	Model        string            `json:"model" yaml:"model"`
	Stage        string            `json:"stage,omitempty" yaml:"stage,omitempty"`
	Action       string            `json:"action,omitempty" yaml:"action,omitempty"`
	SystemPrompt string            `json:"system_prompt,omitempty" yaml:"system_prompt,omitempty"`
	APIKey       string            `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	Headers      map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Timeout      time.Duration     `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	FailOpen     bool              `json:"fail_open,omitempty" yaml:"fail_open,omitempty"`
}

type llmJudgeCheck struct {
	config LLMJudge
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
	compiled := make([]compiledRule, 0, len(config.Rules))
	for _, rule := range config.Rules {
		if strings.TrimSpace(rule.Name) == "" || strings.TrimSpace(rule.Pattern) == "" {
			continue
		}
		pattern, err := regexp.Compile(rule.Pattern)
		if err != nil {
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
		compiled = append(compiled, compiledRule{rule: rule, re: pattern})
	}
	toolRules := make([]ToolRule, 0, len(config.ToolRules))
	for _, rule := range config.ToolRules {
		if normalized, ok := normalizeToolRule(rule); ok {
			toolRules = append(toolRules, normalized)
		}
	}
	webhooks := make([]webhookCheck, 0, len(config.Webhooks))
	for _, webhook := range config.Webhooks {
		if normalized, ok := normalizeWebhook(webhook); ok {
			webhooks = append(webhooks, webhookCheck{config: normalized, client: &http.Client{Timeout: normalized.Timeout}})
		}
	}
	llmJudges := make([]llmJudgeCheck, 0, len(config.LLMJudges))
	for _, judge := range config.LLMJudges {
		if normalized, ok := normalizeLLMJudge(judge); ok {
			llmJudges = append(llmJudges, llmJudgeCheck{config: normalized, client: &http.Client{Timeout: normalized.Timeout}})
		}
	}
	engine := &Engine{
		checks:  []Check{checkPII, checkPromptInjection, checkSQLInjection, checkToxicContent, checkCodeLeakage},
		actions: actions, rules: rules, compiled: compiled, toolRules: toolRules, webhooks: webhooks, llmJudges: llmJudges,
	}
	engine.initAccelerator()
	return engine
}

// initAccelerator builds single-pass WASM scanners for both stages. Patterns
// rejected by the accelerator's engine are left on the Go path so behavior
// never changes when the module is absent or a pattern is unsupported.
func (e *Engine) initAccelerator() {
	if !accel.Available() || len(e.compiled) == 0 {
		return
	}
	inputPatterns := make([]string, 0, len(e.compiled))
	outputPatterns := make([]string, 0, len(e.compiled))
	for _, cr := range e.compiled {
		stage := strings.ToLower(cr.rule.Stage)
		if stage == "input" || stage == "both" {
			inputPatterns = append(inputPatterns, cr.rule.Pattern)
		} else {
			inputPatterns = append(inputPatterns, "")
		}
		if stage == "output" || stage == "both" {
			outputPatterns = append(outputPatterns, cr.rule.Pattern)
		} else {
			outputPatterns = append(outputPatterns, "")
		}
	}
	rejected, err := accel.ValidatePatterns(append(append([]string(nil), inputPatterns...), outputPatterns...))
	if err != nil {
		return // pure-Go path remains authoritative.
	}
	rejectedSet := map[int]bool{}
	for _, idx := range rejected {
		rejectedSet[idx] = true
	}
	goInput := make([]string, len(inputPatterns))
	goOutput := make([]string, len(outputPatterns))
	accelInput := make([]string, len(inputPatterns))
	accelOutput := make([]string, len(outputPatterns))
	fallback := false
	for i := range e.compiled {
		if rejectedSet[i] || rejectedSet[i+len(e.compiled)] {
			fallback = true
			goInput[i], goOutput[i] = inputPatterns[i], outputPatterns[i]
			continue
		}
		accelInput[i], accelOutput[i] = inputPatterns[i], outputPatterns[i]
	}
	scannerIn, errIn := accel.NewScanner(accelInput)
	scannerOut, errOut := accel.NewScanner(accelOutput)
	if errIn != nil || errOut != nil {
		return
	}
	e.accelInput, e.accelOutput = scannerIn, scannerOut
	if fallback {
		e.compiled = retainGoRules(e.compiled, goInput, goOutput)
	} else {
		e.compiled = nil
	}
}

func retainGoRules(compiled []compiledRule, goInput, goOutput []string) []compiledRule {
	var kept []compiledRule
	for i, cr := range compiled {
		if goInput[i] != "" || goOutput[i] != "" {
			kept = append(kept, cr)
		}
	}
	return kept
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

func (e *Engine) EvaluateOutputMessageContext(ctx context.Context, message provider.Message) []Result {
	return e.evaluate(ctx, GuardrailInput{Response: message.Content, ToolCalls: message.ToolCalls, IsInput: false})
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
	scanner := e.accelOutput
	if input.IsInput {
		scanner = e.accelInput
	}
	if scanner != nil && text != "" {
		if matches, err := scanner.ScanFirst([]byte(text)); err == nil {
			emitted := make(map[int]bool, len(matches))
			for _, m := range matches {
				if m.RuleIndex < 0 || m.RuleIndex >= len(e.rules) || emitted[m.RuleIndex] {
					continue
				}
				rule := e.rules[m.RuleIndex]
				if rule.Stage != "both" && rule.Stage != stage {
					continue
				}
				emitted[m.RuleIndex] = true
				triggered = append(triggered, Result{Triggered: true, Type: rule.Name, Action: rule.Action, Message: rule.Message, Details: rule.Pattern})
			}
		}
	}
	for _, cr := range e.compiled {
		if cr.rule.Stage != "both" && cr.rule.Stage != stage {
			continue
		}
		if !cr.re.MatchString(text) {
			continue
		}
		triggered = append(triggered, Result{Triggered: true, Type: cr.rule.Name, Action: cr.rule.Action, Message: cr.rule.Message, Details: cr.rule.Pattern})
	}
	for _, rule := range e.toolRules {
		if rule.Stage != "both" && rule.Stage != stage {
			continue
		}
		for _, call := range input.ToolCalls {
			if !toolRuleMatches(rule, call) {
				continue
			}
			message := rule.Message
			if message == "" {
				message = "Tool guardrail triggered: " + rule.Name
			}
			triggered = append(triggered, Result{
				Triggered: true,
				Type:      "tool:" + rule.Name,
				Action:    rule.Action,
				Message:   message,
				Details:   call.Function.Name,
			})
		}
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
	for _, judge := range e.llmJudges {
		if !judge.appliesTo(stage) {
			continue
		}
		result, err := judge.Evaluate(ctx, input)
		if err != nil {
			if judge.config.FailOpen {
				continue
			}
			triggered = append(triggered, Result{Triggered: true, Type: "llm:" + judge.config.Name, Action: judge.config.Action, Message: "LLM guardrail unavailable: " + judge.config.Name, Details: err.Error()})
			continue
		}
		if result.Triggered {
			triggered = append(triggered, result)
		}
	}
	return triggered
}

func normalizeToolRule(rule ToolRule) (ToolRule, bool) {
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Name == "" {
		return ToolRule{}, false
	}
	rule.Stage = strings.ToLower(strings.TrimSpace(rule.Stage))
	if rule.Stage == "" {
		rule.Stage = "output"
	}
	if rule.Stage != "input" && rule.Stage != "output" && rule.Stage != "both" {
		return ToolRule{}, false
	}
	if strings.TrimSpace(rule.ToolName) != "" {
		if _, err := regexp.Compile(rule.ToolName); err != nil {
			return ToolRule{}, false
		}
	}
	if strings.TrimSpace(rule.ArgumentsPattern) != "" {
		if _, err := regexp.Compile(rule.ArgumentsPattern); err != nil {
			return ToolRule{}, false
		}
	}
	rule.Action = strings.ToLower(strings.TrimSpace(rule.Action))
	if rule.Action == "" {
		rule.Action = "deny"
	}
	if !validAction(rule.Action) {
		return ToolRule{}, false
	}
	return rule, true
}

func toolRuleMatches(rule ToolRule, call provider.ToolCall) bool {
	if strings.TrimSpace(rule.ToolName) != "" {
		pattern := regexp.MustCompile(rule.ToolName)
		if !pattern.MatchString(call.Function.Name) {
			return false
		}
	}
	if strings.TrimSpace(rule.ArgumentsPattern) != "" {
		pattern := regexp.MustCompile(rule.ArgumentsPattern)
		if !pattern.MatchString(call.Function.Arguments) {
			return false
		}
	}
	return strings.TrimSpace(rule.ToolName) != "" || strings.TrimSpace(rule.ArgumentsPattern) != ""
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

func normalizeLLMJudge(judge LLMJudge) (LLMJudge, bool) {
	judge.Name = strings.TrimSpace(judge.Name)
	judge.URL = strings.TrimSpace(judge.URL)
	judge.Model = strings.TrimSpace(judge.Model)
	if judge.Name == "" || judge.URL == "" || judge.Model == "" {
		return LLMJudge{}, false
	}
	parsed, err := url.Parse(judge.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return LLMJudge{}, false
	}
	judge.Stage = strings.ToLower(strings.TrimSpace(judge.Stage))
	if judge.Stage == "" {
		judge.Stage = "both"
	}
	if judge.Stage != "input" && judge.Stage != "output" && judge.Stage != "both" {
		return LLMJudge{}, false
	}
	judge.Action = strings.ToLower(strings.TrimSpace(judge.Action))
	if judge.Action == "" {
		judge.Action = "deny"
	}
	if !validAction(judge.Action) {
		return LLMJudge{}, false
	}
	if judge.Timeout <= 0 {
		judge.Timeout = 10 * time.Second
	}
	if strings.TrimSpace(judge.SystemPrompt) == "" {
		judge.SystemPrompt = "You are a strict safety evaluator. Return compact JSON with fields triggered (boolean), message (string), and details (string)."
	}
	return judge, true
}

func (webhook webhookCheck) appliesTo(stage string) bool {
	return webhook.config.Stage == "both" || webhook.config.Stage == stage
}

func (judge llmJudgeCheck) appliesTo(stage string) bool {
	return judge.config.Stage == "both" || judge.config.Stage == stage
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

func (judge llmJudgeCheck) Evaluate(ctx context.Context, input GuardrailInput) (Result, error) {
	stage := "output"
	if input.IsInput {
		stage = "input"
	}
	body, err := json.Marshal(map[string]any{
		"model": judge.config.Model,
		"messages": []map[string]string{
			{"role": "system", "content": judge.config.SystemPrompt},
			{"role": "user", "content": fmt.Sprintf("Stage: %s\n\nContent:\n%s\n\nTool calls:\n%s", stage, extractText(input), toolCallsJSON(input.ToolCalls))},
		},
		"temperature":     0,
		"response_format": map[string]string{"type": "json_object"},
	})
	if err != nil {
		return Result{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, judge.config.URL, strings.NewReader(string(body)))
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if judge.config.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+judge.config.APIKey)
	}
	for key, value := range judge.config.Headers {
		request.Header.Set(key, value)
	}
	response, err := judge.client.Do(request)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Result{}, fmt.Errorf("unexpected status %s", response.Status)
	}
	var chat struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Triggered bool   `json:"triggered"`
		Message   string `json:"message"`
		Details   string `json:"details"`
	}
	if err := json.NewDecoder(response.Body).Decode(&chat); err != nil {
		return Result{}, fmt.Errorf("decode response: %w", err)
	}
	decision := struct {
		Triggered bool   `json:"triggered"`
		Message   string `json:"message"`
		Details   string `json:"details"`
	}{Triggered: chat.Triggered, Message: chat.Message, Details: chat.Details}
	if len(chat.Choices) > 0 && strings.TrimSpace(chat.Choices[0].Message.Content) != "" {
		_ = json.Unmarshal([]byte(extractJSONObject(chat.Choices[0].Message.Content)), &decision)
	}
	if !decision.Triggered {
		return Result{}, nil
	}
	message := strings.TrimSpace(decision.Message)
	if message == "" {
		message = "LLM guardrail triggered: " + judge.config.Name
	}
	return Result{Triggered: true, Type: "llm:" + judge.config.Name, Action: judge.config.Action, Message: message, Details: decision.Details}, nil
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

func toolCallsJSON(toolCalls []provider.ToolCall) string {
	if len(toolCalls) == 0 {
		return "[]"
	}
	data, err := json.Marshal(toolCalls)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func extractJSONObject(text string) string {
	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end >= start {
		return text[start : end+1]
	}
	return text
}

func Now() time.Time { return time.Now().UTC() }
