package router

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/cel-go/cel"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"sentinel/internal/provider"
)

// Strategy defines how the router selects a provider.
type Strategy string

const (
	StrategyDirect     Strategy = "direct"      // Use the specified provider.
	StrategyFallback   Strategy = "fallback"    // Try primary, then fallback providers.
	StrategyRoundRobin Strategy = "round-robin" // Distribute across providers.
)

// Route defines how to reach a provider for a given request.
type Route struct {
	Provider       string         `json:"provider" yaml:"provider"`
	Fallbacks      []string       `json:"fallbacks,omitempty" yaml:"fallbacks,omitempty"`
	MaxRetries     int            `json:"max_retries,omitempty" yaml:"max_retries,omitempty"`
	RetryBackoff   string         `json:"retry_backoff,omitempty" yaml:"retry_backoff,omitempty"`
	RetryCodes     []int          `json:"retry_codes,omitempty" yaml:"retry_codes,omitempty"`
	Timeout        string         `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	ShadowProvider string         `json:"shadow_provider,omitempty" yaml:"shadow_provider,omitempty"`
	DefaultParams  map[string]any `json:"default_params,omitempty" yaml:"default_params,omitempty"`
	OverrideParams map[string]any `json:"override_params,omitempty" yaml:"override_params,omitempty"`
	DropParams     []string       `json:"drop_params,omitempty" yaml:"drop_params,omitempty"`
	Variants       []Variant      `json:"variants,omitempty" yaml:"variants,omitempty"`
}

type Variant struct {
	Name     string `json:"name" yaml:"name"`
	Provider string `json:"provider" yaml:"provider"`
	Model    string `json:"model" yaml:"model"`
	Weight   int    `json:"weight" yaml:"weight"`
	// Condition is an optional CEL expression with model and prompt variables.
	// Conditional variants are considered before unconditional variants.
	Condition string `json:"condition,omitempty" yaml:"condition,omitempty"`
}

// Router selects and invokes providers based on configured routing strategy.
type Router struct {
	registry   *provider.Registry
	routes     map[string]Route // model -> route overrides
	mu         sync.Mutex
	rrCounters map[string]int // round-robin counters per model
	breakers   *CircuitBreaker
}

// New creates a new Router.
func New(registry *provider.Registry) *Router {
	return &Router{
		registry:   registry,
		routes:     make(map[string]Route),
		rrCounters: make(map[string]int),
		breakers:   NewCircuitBreaker(5, 60*time.Second),
	}
}

func (r *Router) SetCircuitBreaker(breaker *CircuitBreaker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.breakers = breaker
}

// SetRoute configures a custom route for a specific model.
func (r *Router) SetRoute(model string, route Route) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[model] = route
}

// TransformRequest applies Portkey-style request shaping to the logical route.
// Defaults only fill omitted fields; overrides always win; drops run last.
func (r *Router) TransformRequest(req provider.ChatRequest) (provider.ChatRequest, error) {
	route, ok := r.routeFor(req.Model)
	if !ok {
		return req, nil
	}
	for field, value := range route.DefaultParams {
		if requestFieldSet(req, field) {
			continue
		}
		if err := setRequestField(&req, field, value); err != nil {
			return req, fmt.Errorf("route %q default_params.%s: %w", req.Model, field, err)
		}
	}
	for field, value := range route.OverrideParams {
		if err := setRequestField(&req, field, value); err != nil {
			return req, fmt.Errorf("route %q override_params.%s: %w", req.Model, field, err)
		}
	}
	for _, field := range route.DropParams {
		dropRequestField(&req, field)
	}
	return req, nil
}

func (r *Router) ShadowProviderForModel(model string) string {
	route, ok := r.routeFor(model)
	if !ok {
		return ""
	}
	return route.ShadowProvider
}

// Embeddings routes OpenAI-compatible embedding requests through the same
// provider/fallback configuration as chat traffic. Providers that do not
// implement embeddings are skipped so mixed fallback chains remain useful.
func (r *Router) Embeddings(ctx context.Context, req provider.EmbeddingRequest, providerHint string) (provider.EmbeddingResponse, provider.ProviderMeta, error) {
	logicalModel := req.Model
	route, hasRoute := r.routeFor(logicalModel)
	selectedRoute := false
	if providerHint == "" {
		if variant, ok := r.selectVariant(logicalModel, provider.ChatRequest{Model: logicalModel}); ok {
			providerHint = variant.Provider
			selectedRoute = true
			if variant.Model != "" {
				req.Model = variant.Model
			}
		}
	}
	providers := r.resolveProviders(logicalModel, req.Model, providerHint, route, hasRoute && (selectedRoute || providerHint == ""))
	if len(providers) == 0 {
		return provider.EmbeddingResponse{}, provider.ProviderMeta{}, fmt.Errorf("no provider found for model %q", req.Model)
	}
	var lastErr error
	for index, current := range providers {
		embeddingProvider, ok := current.(provider.EmbeddingProvider)
		if !ok {
			lastErr = fmt.Errorf("provider %q does not support embeddings", current.Name())
			continue
		}
		if !r.allowProvider(current.Name()) {
			lastErr = fmt.Errorf("provider %q circuit is open", current.Name())
			continue
		}
		callCtx, cancel := withRouteTimeout(ctx, route.Timeout)
		response, meta, err := embeddingProvider.Embeddings(callCtx, req)
		cancel()
		if err == nil {
			r.recordSuccess(current.Name())
			meta.Fallback = index > 0
			if meta.Model == "" {
				meta.Model = req.Model
			}
			return response, meta, nil
		}
		r.recordFailure(current.Name())
		lastErr = err
	}
	return provider.EmbeddingResponse{}, provider.ProviderMeta{Error: errorString(lastErr)}, fmt.Errorf("all embedding providers exhausted: %w", lastErr)
}

// Execute routes a chat request to the appropriate provider,
// handling retries and fallbacks automatically.
func (r *Router) Execute(ctx context.Context, req provider.ChatRequest, providerHint string) (provider.ChatResponse, provider.ProviderMeta, error) {
	logicalModel := req.Model
	route, hasRoute := r.routeFor(logicalModel)
	selectedRoute := false
	if providerHint == "" {
		if variant, ok := r.selectVariant(logicalModel, req); ok {
			providerHint = variant.Provider
			selectedRoute = true
			if variant.Model != "" {
				req.Model = variant.Model
			}
		}
	}
	providers := r.resolveProviders(logicalModel, req.Model, providerHint, route, hasRoute && (selectedRoute || providerHint == ""))
	if len(providers) == 0 {
		return provider.ChatResponse{}, provider.ProviderMeta{}, fmt.Errorf("no provider found for model %q", req.Model)
	}

	var lastErr error
	for i, prov := range providers {
		if !r.allowProvider(prov.Name()) {
			lastErr = fmt.Errorf("provider %q circuit is open", prov.Name())
			traceSkippedProvider(ctx, prov.Name(), req.Model, lastErr)
			continue
		}
		maxRetries := route.MaxRetries + 1
		if !hasRoute {
			maxRetries = 1
		}

		for attempt := 0; attempt < maxRetries; attempt++ {
			callCtx, cancel := withRouteTimeout(ctx, route.Timeout)
			callCtx, span := startProviderSpan(callCtx, prov.Name(), req.Model, attempt, i > 0, false)
			resp, meta, err := prov.ChatCompletion(callCtx, req)
			cancel()
			if err == nil {
				r.recordSuccess(prov.Name())
				meta.Retries = attempt
				meta.Fallback = i > 0
				if meta.Model == "" {
					meta.Model = req.Model
				}
				finishProviderSpan(span, meta, nil)
				return resp, meta, nil
			}
			finishProviderSpan(span, meta, err)
			r.recordFailure(prov.Name())
			lastErr = err
			meta.Retries = attempt

			// Don't retry on context cancellation.
			if ctx.Err() != nil || !isRetryable(err, route.RetryCodes) {
				if ctx.Err() != nil {
					return provider.ChatResponse{}, meta, ctx.Err()
				}
				return provider.ChatResponse{}, meta, err
			}

			// Brief backoff before retry.
			if attempt < maxRetries-1 {
				if err := waitForRetry(ctx, retryBackoff(route.RetryBackoff, attempt)); err != nil {
					return provider.ChatResponse{}, meta, err
				}
			}
		}
	}

	return provider.ChatResponse{}, provider.ProviderMeta{
		Error: errorString(lastErr),
	}, fmt.Errorf("all providers exhausted: %w", lastErr)
}

func (r *Router) ExecuteStream(ctx context.Context, req provider.ChatRequest, providerHint string) (<-chan provider.ChatResponseChunk, provider.ProviderMeta, error) {
	logicalModel := req.Model
	route, hasRoute := r.routeFor(logicalModel)
	selectedRoute := false
	if providerHint == "" {
		if variant, ok := r.selectVariant(logicalModel, req); ok {
			providerHint = variant.Provider
			selectedRoute = true
			if variant.Model != "" {
				req.Model = variant.Model
			}
		}
	}

	providers := r.resolveProviders(logicalModel, req.Model, providerHint, route, hasRoute && (selectedRoute || providerHint == ""))
	if len(providers) == 0 {
		return nil, provider.ProviderMeta{}, fmt.Errorf("no provider found for model %q", req.Model)
	}

	var lastErr error
	for i, prov := range providers {
		if !r.allowProvider(prov.Name()) {
			lastErr = fmt.Errorf("provider %q circuit is open", prov.Name())
			continue
		}

		if streamer, ok := prov.(provider.StreamProvider); ok {
			attempts := 1
			if hasRoute {
				attempts += route.MaxRetries
			}
			for attempt := 0; attempt < attempts; attempt++ {
				callCtx, cancel := withRouteTimeout(ctx, route.Timeout)
				callCtx, span := startProviderSpan(callCtx, prov.Name(), req.Model, attempt, i > 0, true)
				chunks, meta, err := streamer.ChatCompletionStream(callCtx, req)
				if err == nil {
					r.recordSuccess(prov.Name())
					meta.Retries = attempt
					meta.Fallback = i > 0
					if meta.Model == "" {
						meta.Model = req.Model
					}
					finishProviderSpan(span, meta, nil)
					return chunks, meta, nil
				}
				cancel()
				finishProviderSpan(span, meta, err)
				r.recordFailure(prov.Name())
				lastErr = err
				if ctx.Err() != nil || !isRetryable(err, route.RetryCodes) {
					break
				}
				if attempt < attempts-1 {
					if err := waitForRetry(ctx, retryBackoff(route.RetryBackoff, attempt)); err != nil {
						return nil, meta, err
					}
				}
			}
			continue
		}

		callCtx, cancel := withRouteTimeout(ctx, route.Timeout)
		callCtx, span := startProviderSpan(callCtx, prov.Name(), req.Model, 0, i > 0, false)
		resp, meta, err := prov.ChatCompletion(callCtx, req)
		cancel()
		if err == nil {
			r.recordSuccess(prov.Name())
			meta.Fallback = i > 0
			if meta.Model == "" {
				meta.Model = req.Model
			}
			finishProviderSpan(span, meta, nil)
			return provider.StreamFromResponse(ctx, resp), meta, nil
		}
		finishProviderSpan(span, meta, err)
		r.recordFailure(prov.Name())
		lastErr = err
		if ctx.Err() != nil {
			return nil, meta, ctx.Err()
		}
	}

	return nil, provider.ProviderMeta{Error: errorString(lastErr)}, fmt.Errorf("all providers exhausted: %w", lastErr)
}

func startProviderSpan(ctx context.Context, providerName, model string, attempt int, fallback bool, stream bool) (context.Context, oteltrace.Span) {
	return otel.Tracer("sentinel/router").Start(
		ctx,
		"llm.provider.call",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("llm.provider", providerName),
			attribute.String("llm.request.model", model),
			attribute.Int("sentinel.retry_attempt", attempt),
			attribute.Bool("sentinel.fallback", fallback),
			attribute.Bool("llm.request.stream", stream),
		),
	)
}

func finishProviderSpan(span oteltrace.Span, meta provider.ProviderMeta, err error) {
	defer span.End()
	span.SetAttributes(
		attribute.String("llm.response.provider", meta.Provider),
		attribute.String("llm.response.model", meta.Model),
		attribute.Float64("llm.usage.cost_usd", meta.Cost),
	)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return
	}
	span.SetStatus(codes.Ok, "")
}

func traceSkippedProvider(ctx context.Context, providerName, model string, err error) {
	_, span := startProviderSpan(ctx, providerName, model, 0, false, false)
	defer span.End()
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

func (r *Router) selectVariant(model string, req provider.ChatRequest) (Variant, bool) {
	route, ok := r.routeFor(model)
	if !ok || len(route.Variants) == 0 {
		return Variant{}, false
	}

	variants := make([]Variant, 0, len(route.Variants))
	for _, variant := range route.Variants {
		if strings.TrimSpace(variant.Condition) != "" && !matchesCondition(variant.Condition, req) {
			continue
		}
		variants = append(variants, variant)
	}
	if len(variants) == 0 {
		return Variant{}, false
	}
	total := 0
	for _, variant := range variants {
		if variant.Weight > 0 {
			total += variant.Weight
		}
	}
	if total <= 0 {
		return Variant{}, false
	}

	roll := randomInt(total)
	accumulated := 0
	for _, variant := range variants {
		if variant.Weight <= 0 {
			continue
		}
		accumulated += variant.Weight
		if roll < accumulated {
			return variant, true
		}
	}
	return variants[len(variants)-1], true
}

func randomInt(max int) int {
	if max <= 1 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return int(time.Now().UnixNano() % int64(max))
	}
	return int(binary.BigEndian.Uint64(b[:]) % uint64(max))
}

func (r *Router) allowProvider(providerName string) bool {
	r.mu.Lock()
	breaker := r.breakers
	r.mu.Unlock()
	if breaker == nil {
		return true
	}
	return breaker.Allow(providerName)
}

func (r *Router) recordSuccess(providerName string) {
	r.mu.Lock()
	breaker := r.breakers
	r.mu.Unlock()
	if breaker != nil {
		breaker.RecordSuccess(providerName)
	}
}

func (r *Router) recordFailure(providerName string) {
	r.mu.Lock()
	breaker := r.breakers
	r.mu.Unlock()
	if breaker != nil {
		breaker.RecordFailure(providerName)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// resolveProviders returns the ordered list of providers to try for the given model.
func (r *Router) resolveProviders(logicalModel, model, hint string, route Route, useRouteFallbacks bool) []provider.Provider {
	var provs []provider.Provider

	// If a provider is explicitly requested via header, use it.
	if hint != "" {
		if p, err := r.registry.Get(hint); err == nil {
			provs = append(provs, p)
		}
	}

	if useRouteFallbacks && len(provs) == 0 && route.Provider != "" {
		if p, err := r.registry.Get(route.Provider); err == nil {
			provs = append(provs, p)
		}
	}
	if useRouteFallbacks {
		for _, fb := range route.Fallbacks {
			if p, err := r.registry.Get(fb); err == nil {
				provs = appendUniqueProvider(provs, p)
			}
		}
	}

	// Auto-resolve by model name.
	if len(provs) == 0 {
		if p, err := r.registry.ResolveModel(model); err == nil {
			provs = append(provs, p)
		}
	}

	// Guess provider from model name prefix.
	if len(provs) == 0 {
		guessed := guessProvider(model)
		if p, err := r.registry.Get(guessed); err == nil {
			provs = append(provs, p)
		}
	}

	return provs
}

func (r *Router) routeFor(model string) (Route, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	route, ok := r.routes[model]
	return route, ok
}

func appendUniqueProvider(providers []provider.Provider, candidate provider.Provider) []provider.Provider {
	for _, existing := range providers {
		if existing.Name() == candidate.Name() {
			return providers
		}
	}
	return append(providers, candidate)
}

func withRouteTimeout(ctx context.Context, timeout string) (context.Context, context.CancelFunc) {
	if strings.TrimSpace(timeout) == "" {
		return context.WithCancel(ctx)
	}
	duration, err := time.ParseDuration(timeout)
	if err != nil || duration <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, duration)
}

func retryBackoff(value string, attempt int) time.Duration {
	if duration, err := time.ParseDuration(strings.TrimSpace(value)); err == nil && duration > 0 {
		return duration * time.Duration(attempt+1)
	}
	return time.Duration(attempt+1) * 200 * time.Millisecond
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var statusCodePattern = regexp.MustCompile(`(?i)status\s*(?:code\s*)?\(?([0-9]{3})\)?`)

func isRetryable(err error, codes []int) bool {
	if err == nil {
		return false
	}
	if len(codes) == 0 {
		return true
	}
	match := statusCodePattern.FindStringSubmatch(err.Error())
	if len(match) != 2 {
		return false
	}
	for _, code := range codes {
		if fmt.Sprint(code) == match[1] {
			return true
		}
	}
	return false
}

func matchesCondition(expression string, req provider.ChatRequest) bool {
	env, err := cel.NewEnv(
		cel.Variable("model", cel.StringType),
		cel.Variable("prompt", cel.StringType),
	)
	if err != nil {
		return false
	}
	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return false
	}
	program, err := env.Program(ast)
	if err != nil {
		return false
	}
	result, _, err := program.Eval(map[string]any{"model": req.Model, "prompt": promptText(req)})
	if err != nil {
		return false
	}
	matched, ok := result.Value().(bool)
	return ok && matched
}

func promptText(req provider.ChatRequest) string {
	parts := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		parts = append(parts, message.Text())
	}
	return strings.Join(parts, "\n")
}

func requestFieldSet(req provider.ChatRequest, field string) bool {
	switch field {
	case "model":
		return req.Model != ""
	case "temperature":
		return req.Temperature != nil
	case "max_tokens":
		return req.MaxTokens != nil
	case "top_p":
		return req.TopP != nil
	case "stop":
		return len(req.Stop) > 0
	case "stream":
		return req.Stream
	default:
		return false
	}
}

func setRequestField(req *provider.ChatRequest, field string, value any) error {
	switch field {
	case "model":
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return fmt.Errorf("must be a non-empty string")
		}
		req.Model = text
	case "temperature":
		number, ok := floatValue(value)
		if !ok {
			return fmt.Errorf("must be a number")
		}
		req.Temperature = &number
	case "max_tokens":
		number, ok := intValue(value)
		if !ok || number < 1 {
			return fmt.Errorf("must be a positive integer")
		}
		req.MaxTokens = &number
	case "top_p":
		number, ok := floatValue(value)
		if !ok || number < 0 || number > 1 {
			return fmt.Errorf("must be a number between 0 and 1")
		}
		req.TopP = &number
	case "stream":
		flag, ok := value.(bool)
		if !ok {
			return fmt.Errorf("must be a boolean")
		}
		req.Stream = flag
	case "stop":
		stops, ok := stringSlice(value)
		if !ok {
			return fmt.Errorf("must be an array of strings")
		}
		req.Stop = stops
	default:
		return fmt.Errorf("is not a supported chat-completions parameter")
	}
	return nil
}

func dropRequestField(req *provider.ChatRequest, field string) {
	switch field {
	case "temperature":
		req.Temperature = nil
	case "max_tokens":
		req.MaxTokens = nil
	case "top_p":
		req.TopP = nil
	case "stop":
		req.Stop = nil
	case "stream":
		req.Stream = false
	}
}

func floatValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case int32:
		return float64(number), true
	default:
		return 0, false
	}
}

func intValue(value any) (int, bool) {
	number, ok := floatValue(value)
	if !ok || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}

func stringSlice(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	default:
		return nil, false
	}
}

func guessProvider(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.HasPrefix(lower, "gpt") || strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") || strings.HasPrefix(lower, "chatgpt"):
		return "openai"
	case strings.HasPrefix(lower, "claude"):
		return "anthropic"
	case strings.HasPrefix(lower, "gemini"):
		return "google"
	case strings.HasPrefix(lower, "llama") || strings.HasPrefix(lower, "mixtral") || strings.HasPrefix(lower, "gemma"):
		return "groq"
	case strings.HasPrefix(lower, "deepseek"):
		return "deepseek"
	case strings.HasPrefix(lower, "mistral") || strings.HasPrefix(lower, "codestral") || strings.HasPrefix(lower, "pixtral"):
		return "mistral"
	case strings.HasPrefix(lower, "qwen"):
		return "qwen"
	case strings.HasPrefix(lower, "phi"):
		return "phi"
	case strings.HasPrefix(lower, "command"):
		return "cohere"
	default:
		return "openai"
	}
}
