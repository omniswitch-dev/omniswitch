package router

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/cel-go/cel"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

// Strategy defines how the router selects a provider.
type Strategy string

const (
	StrategyDirect       Strategy = "direct"        // Use the specified provider.
	StrategyFallback     Strategy = "fallback"      // Try primary, then fallback providers.
	StrategyRoundRobin   Strategy = "round-robin"   // Distribute across providers.
	StrategyLeastLatency Strategy = "least-latency" // Prefer the fastest recent provider.
	StrategyCostOptimal  Strategy = "cost-optimal"  // Prefer the lowest input-token price.
)

// Route defines how to reach a provider for a given request.
type Route struct {
	Strategy       Strategy       `json:"strategy,omitempty" yaml:"strategy,omitempty"`
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
	latency    *latencyTracker
	// conditions caches compiled CEL programs for conditional variants,
	// keyed by trimmed expression. It is rebuilt whenever the route table
	// is replaced so it only holds expressions of the current routes.
	conditions map[string]compiledCondition
}

type latencyTracker struct {
	mu      sync.Mutex
	samples map[string][]time.Duration
}

func newLatencyTracker() *latencyTracker {
	return &latencyTracker{samples: map[string][]time.Duration{}}
}

func (t *latencyTracker) record(provider string, d time.Duration) {
	if t == nil || provider == "" || d <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	samples := append(t.samples[provider], d)
	const maxSamples = 32
	if len(samples) > maxSamples {
		samples = samples[len(samples)-maxSamples:]
	}
	t.samples[provider] = samples
}

func (t *latencyTracker) average(provider string) time.Duration {
	if t == nil || provider == "" {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	samples := t.samples[provider]
	if len(samples) == 0 {
		return 0
	}
	var total time.Duration
	for _, sample := range samples {
		total += sample
	}
	return total / time.Duration(len(samples))
}

// New creates a new Router.
func New(registry *provider.Registry) *Router {
	return &Router{
		registry:   registry,
		routes:     make(map[string]Route),
		rrCounters: make(map[string]int),
		breakers:   NewCircuitBreaker(5, 60*time.Second),
		latency:    newLatencyTracker(),
		conditions: make(map[string]compiledCondition),
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
	compileRouteConditions(r.conditions, map[string]Route{model: route})
}

// ReplaceRoutes atomically swaps the full route table, removing routes that
// are no longer present. Used by config hot-reload.
func (r *Router) ReplaceRoutes(routes map[string]Route) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = make(map[string]Route, len(routes))
	for model, route := range routes {
		r.routes[model] = route
	}
	r.conditions = make(map[string]compiledCondition)
	compileRouteConditions(r.conditions, r.routes)
}

// RouteFor reports whether a logical model has a configured route.
func (r *Router) RouteFor(model string) (Route, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	route, ok := r.routes[model]
	return route, ok
}

// CloneWithRoutes returns a request-scoped router that inherits the registry,
// base routes, and circuit breaker, then overlays the supplied routes.
func (r *Router) CloneWithRoutes(overrides map[string]Route) *Router {
	r.mu.Lock()
	defer r.mu.Unlock()
	cloned := New(r.registry)
	cloned.breakers = r.breakers
	cloned.latency = r.latency
	for model, route := range r.routes {
		cloned.routes[model] = route
	}
	for model, route := range overrides {
		cloned.routes[model] = route
	}
	for expression, compiled := range r.conditions {
		cloned.conditions[expression] = compiled
	}
	compileRouteConditions(cloned.conditions, overrides)
	return cloned
}

// CloneForShadow returns a copy of r for mirrored (shadow) traffic. The copy
// keeps r's routes but uses breaker and its own latency tracker, so shadow
// calls can neither open circuits nor skew least-latency routing for
// production traffic.
func (r *Router) CloneForShadow(breaker *CircuitBreaker) *Router {
	// The clone is not shared with any other goroutine yet, so its fields
	// can be set without taking its lock.
	cloned := r.CloneWithRoutes(nil)
	cloned.breakers = breaker
	cloned.latency = newLatencyTracker()
	return cloned
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
			r.recordLatency(current.Name(), meta.Latency)
			meta.Fallback = index > 0
			if meta.Model == "" {
				meta.Model = req.Model
			}
			return response, meta, nil
		}
		r.recordAttemptFailure(ctx, current.Name(), err)
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return provider.EmbeddingResponse{}, provider.ProviderMeta{Error: errorString(lastErr)}, fmt.Errorf("all embedding providers exhausted: %w", lastErr)
}

func (r *Router) ImageGeneration(ctx context.Context, req provider.ImageRequest, providerHint string) (provider.ImageResponse, provider.ProviderMeta, error) {
	logicalModel := req.Model
	route, hasRoute := r.routeFor(logicalModel)
	providers := r.resolveProviders(logicalModel, req.Model, providerHint, route, hasRoute && providerHint == "")
	if len(providers) == 0 {
		return provider.ImageResponse{}, provider.ProviderMeta{}, fmt.Errorf("no provider found for model %q", req.Model)
	}
	var lastErr error
	for index, current := range providers {
		imageProvider, ok := current.(provider.ImageProvider)
		if !ok {
			lastErr = fmt.Errorf("provider %q does not support image generation", current.Name())
			continue
		}
		if !r.allowProvider(current.Name()) {
			lastErr = fmt.Errorf("provider %q circuit is open", current.Name())
			continue
		}
		callCtx, cancel := withRouteTimeout(ctx, route.Timeout)
		response, meta, err := imageProvider.ImageGeneration(callCtx, req)
		cancel()
		if err == nil {
			r.recordSuccess(current.Name())
			r.recordLatency(current.Name(), meta.Latency)
			meta.Fallback = index > 0
			if meta.Model == "" {
				meta.Model = req.Model
			}
			return response, meta, nil
		}
		r.recordAttemptFailure(ctx, current.Name(), err)
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return provider.ImageResponse{}, provider.ProviderMeta{Error: errorString(lastErr)}, fmt.Errorf("all image providers exhausted: %w", lastErr)
}

func (r *Router) Transcription(ctx context.Context, req provider.TranscriptionRequest, providerHint string) (provider.TranscriptionResponse, provider.ProviderMeta, error) {
	logicalModel := req.Model
	route, hasRoute := r.routeFor(logicalModel)
	providers := r.resolveProviders(logicalModel, req.Model, providerHint, route, hasRoute && providerHint == "")
	if len(providers) == 0 {
		return provider.TranscriptionResponse{}, provider.ProviderMeta{}, fmt.Errorf("no provider found for model %q", req.Model)
	}
	var lastErr error
	for index, current := range providers {
		audioProvider, ok := current.(provider.AudioProvider)
		if !ok {
			lastErr = fmt.Errorf("provider %q does not support audio transcription", current.Name())
			continue
		}
		if !r.allowProvider(current.Name()) {
			lastErr = fmt.Errorf("provider %q circuit is open", current.Name())
			continue
		}
		callCtx, cancel := withRouteTimeout(ctx, route.Timeout)
		response, meta, err := audioProvider.Transcription(callCtx, req)
		cancel()
		if err == nil {
			r.recordSuccess(current.Name())
			r.recordLatency(current.Name(), meta.Latency)
			meta.Fallback = index > 0
			if meta.Model == "" {
				meta.Model = req.Model
			}
			return response, meta, nil
		}
		r.recordAttemptFailure(ctx, current.Name(), err)
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return provider.TranscriptionResponse{}, provider.ProviderMeta{Error: errorString(lastErr)}, fmt.Errorf("all transcription providers exhausted: %w", lastErr)
}

func (r *Router) Speech(ctx context.Context, req provider.SpeechRequest, providerHint string) (io.ReadCloser, string, provider.ProviderMeta, error) {
	logicalModel := req.Model
	route, hasRoute := r.routeFor(logicalModel)
	providers := r.resolveProviders(logicalModel, req.Model, providerHint, route, hasRoute && providerHint == "")
	if len(providers) == 0 {
		return nil, "", provider.ProviderMeta{}, fmt.Errorf("no provider found for model %q", req.Model)
	}
	var lastErr error
	for index, current := range providers {
		audioProvider, ok := current.(provider.AudioProvider)
		if !ok {
			lastErr = fmt.Errorf("provider %q does not support audio speech", current.Name())
			continue
		}
		if !r.allowProvider(current.Name()) {
			lastErr = fmt.Errorf("provider %q circuit is open", current.Name())
			continue
		}
		callCtx, cancel := withRouteTimeout(ctx, route.Timeout)
		body, contentType, meta, err := audioProvider.Speech(callCtx, req)
		if err == nil {
			r.recordSuccess(current.Name())
			r.recordLatency(current.Name(), meta.Latency)
			meta.Fallback = index > 0
			if meta.Model == "" {
				meta.Model = req.Model
			}
			return &cancelReadCloser{ReadCloser: body, cancel: cancel}, contentType, meta, nil
		}
		cancel()
		r.recordAttemptFailure(ctx, current.Name(), err)
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, "", provider.ProviderMeta{Error: errorString(lastErr)}, fmt.Errorf("all speech providers exhausted: %w", lastErr)
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// Rerank routes common RAG reranking requests through providers that implement
// the optional rerank interface. Providers that do not support rerank are
// skipped in a fallback chain.
func (r *Router) Rerank(ctx context.Context, req provider.RerankRequest, providerHint string) (provider.RerankResponse, provider.ProviderMeta, error) {
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
		return provider.RerankResponse{}, provider.ProviderMeta{}, fmt.Errorf("no provider found for model %q", req.Model)
	}
	var lastErr error
	for index, current := range providers {
		rerankProvider, ok := current.(provider.RerankProvider)
		if !ok {
			lastErr = fmt.Errorf("provider %q does not support rerank", current.Name())
			continue
		}
		if !r.allowProvider(current.Name()) {
			lastErr = fmt.Errorf("provider %q circuit is open", current.Name())
			continue
		}
		callCtx, cancel := withRouteTimeout(ctx, route.Timeout)
		response, meta, err := rerankProvider.Rerank(callCtx, req)
		cancel()
		if err == nil {
			r.recordSuccess(current.Name())
			r.recordLatency(current.Name(), meta.Latency)
			meta.Fallback = index > 0
			if meta.Model == "" {
				meta.Model = req.Model
			}
			return response, meta, nil
		}
		r.recordAttemptFailure(ctx, current.Name(), err)
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return provider.RerankResponse{}, provider.ProviderMeta{Error: errorString(lastErr)}, fmt.Errorf("all rerank providers exhausted: %w", lastErr)
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
				r.recordLatency(prov.Name(), meta.Latency)
				meta.Retries = attempt
				meta.Fallback = i > 0
				if meta.Model == "" {
					meta.Model = req.Model
				}
				finishProviderSpan(span, meta, nil)
				return resp, meta, nil
			}
			finishProviderSpan(span, meta, err)
			r.recordAttemptFailure(ctx, prov.Name(), err)
			lastErr = err
			meta.Retries = attempt

			// The caller is gone: neither retries nor fallbacks can help.
			if ctx.Err() != nil {
				return provider.ChatResponse{}, meta, ctx.Err()
			}
			// A non-retryable error ends the retries against this provider,
			// but the next provider in the fallback chain is still tried
			// (the same behaviour as ExecuteStream).
			if !isRetryable(err, route.RetryCodes) {
				break
			}

			// Brief backoff before retry. A Retry-After hint that is too
			// long moves on to the next provider instead of waiting.
			if attempt < maxRetries-1 {
				delay, ok := retryWait(route.RetryBackoff, attempt, err)
				if !ok {
					break
				}
				if err := waitForRetry(ctx, delay); err != nil {
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

	timeout := routeTimeout(route.Timeout)
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
				chunks, meta, err := r.startStream(ctx, prov.Name(), streamer, req, timeout, attempt, i > 0)
				if err == nil {
					r.recordSuccess(prov.Name())
					r.recordLatency(prov.Name(), meta.Latency)
					meta.Retries = attempt
					meta.Fallback = i > 0
					if meta.Model == "" {
						meta.Model = req.Model
					}
					return chunks, meta, nil
				}
				r.recordAttemptFailure(ctx, prov.Name(), err)
				lastErr = err
				if ctx.Err() != nil {
					return nil, meta, ctx.Err()
				}
				if !isRetryable(err, route.RetryCodes) {
					break
				}
				if attempt < attempts-1 {
					delay, ok := retryWait(route.RetryBackoff, attempt, err)
					if !ok {
						break
					}
					if err := waitForRetry(ctx, delay); err != nil {
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
			r.recordLatency(prov.Name(), meta.Latency)
			meta.Fallback = i > 0
			if meta.Model == "" {
				meta.Model = req.Model
			}
			finishProviderSpan(span, meta, nil)
			return provider.StreamFromResponse(ctx, resp), meta, nil
		}
		finishProviderSpan(span, meta, err)
		r.recordAttemptFailure(ctx, prov.Name(), err)
		lastErr = err
		if ctx.Err() != nil {
			return nil, meta, ctx.Err()
		}
	}

	return nil, provider.ProviderMeta{Error: errorString(lastErr)}, fmt.Errorf("all providers exhausted: %w", lastErr)
}

// errStreamIdleTimeout is reported on the terminal chunk when an upstream
// stream stays silent for longer than the route timeout after it started.
var errStreamIdleTimeout = errors.New("stream idle timeout exceeded")

// finalChunkGrace bounds how long the relay waits to hand the terminal error
// chunk to a consumer whose request context has already ended.
const finalChunkGrace = time.Second

// startStream runs one streaming attempt against a provider.
//
// The route timeout is a time-to-first-response limit: it covers opening the
// upstream stream and receiving its first chunk. Once the first chunk has
// arrived the stream is no longer bound by the route timeout, so long
// generations are not cut off part way; instead the relay applies an idle
// timeout of the same length between chunks. Because nothing has been sent
// to the client before the first chunk, a failure in that phase is returned
// as an error and the caller may retry or fall back to another provider.
//
// The returned channel always ends either with the source closing normally
// or with a terminal chunk whose Err field is set, and the attempt's context
// is cancelled when the relay finishes so no timer or upstream body is left
// running.
func (r *Router) startStream(ctx context.Context, providerName string, streamer provider.StreamProvider, req provider.ChatRequest, timeout time.Duration, attempt int, fallback bool) (<-chan provider.ChatResponseChunk, provider.ProviderMeta, error) {
	attemptCtx, cancel := context.WithCancel(ctx)
	var timedOut atomic.Bool
	var firstByte *time.Timer
	if timeout > 0 {
		firstByte = time.AfterFunc(timeout, func() {
			timedOut.Store(true)
			cancel()
		})
	}
	stopFirstByte := func() bool {
		if firstByte == nil {
			return true
		}
		return firstByte.Stop()
	}
	timeoutErr := func(name string) error {
		return fmt.Errorf("provider %q did not start streaming within route timeout %s: %w", name, timeout, context.DeadlineExceeded)
	}
	fail := func(span oteltrace.Span, meta provider.ProviderMeta, err error) (<-chan provider.ChatResponseChunk, provider.ProviderMeta, error) {
		stopFirstByte()
		cancel()
		if timedOut.Load() && ctx.Err() == nil {
			err = timeoutErr(providerName)
		}
		if meta.Error == "" {
			meta.Error = err.Error()
		}
		finishProviderSpan(span, meta, err)
		return nil, meta, err
	}

	spanCtx, span := startProviderSpan(attemptCtx, providerName, req.Model, attempt, fallback, true)
	source, meta, err := streamer.ChatCompletionStream(spanCtx, req)
	if err != nil {
		return fail(span, meta, err)
	}

	var first provider.ChatResponseChunk
	var gotFirst bool
	select {
	case first, gotFirst = <-source:
	case <-attemptCtx.Done():
	}
	if !gotFirst {
		if attemptCtx.Err() != nil {
			// Timed out or cancelled before anything reached the client.
			go drainChunks(source)
			cause := ctx.Err()
			if cause == nil {
				cause = attemptCtx.Err()
			}
			return fail(span, meta, cause)
		}
		// The upstream closed without sending anything: no content, no
		// finish reason and no error. Native adapters report a missing
		// terminal event as ErrStreamTruncated; do the same here so the
		// attempt counts as a failure (and may be retried or fall back)
		// instead of being logged as an empty successful response.
		return fail(span, meta, fmt.Errorf("provider %q: %w", providerName, provider.ErrStreamTruncated))
	}
	if !stopFirstByte() {
		// The route timeout fired just as the first chunk arrived; the
		// attempt context is already cancelled, so the stream is unusable.
		go drainChunks(source)
		return fail(span, meta, timeoutErr(providerName))
	}
	if first.Err != nil && len(first.Choices) == 0 {
		// The upstream failed before producing output; nothing was sent to
		// the client, so let the caller retry or fall back.
		go drainChunks(source)
		return fail(span, meta, first.Err)
	}
	finishProviderSpan(span, meta, nil)

	out := make(chan provider.ChatResponseChunk)
	go relayStream(ctx, cancel, source, first, out, timeout)
	return out, meta, nil
}

// relayStream forwards chunks from source to out until source closes, the
// request context ends, or the stream stays idle for longer than idle. It
// always cancels the attempt context and closes out when it returns.
func relayStream(ctx context.Context, cancel context.CancelFunc, source <-chan provider.ChatResponseChunk, first provider.ChatResponseChunk, out chan<- provider.ChatResponseChunk, idle time.Duration) {
	sourceClosed := false
	defer func() {
		cancel()
		if !sourceClosed {
			// The upstream sees the cancellation and should close soon;
			// keep reading so a producer blocked on a send can exit.
			go drainChunks(source)
		}
		close(out)
	}()

	send := func(chunk provider.ChatResponseChunk) bool {
		select {
		case out <- chunk:
			return true
		case <-ctx.Done():
			return false
		}
	}
	sendFinal := func(err error) {
		final := provider.ChatResponseChunk{Err: err}
		if ctx.Err() == nil {
			send(final)
			return
		}
		grace := time.NewTimer(finalChunkGrace)
		defer grace.Stop()
		select {
		case out <- final:
		case <-grace.C:
		}
	}

	if !send(first) {
		sendFinal(ctx.Err())
		return
	}
	var idleC <-chan time.Time
	var idleTimer *time.Timer
	if idle > 0 {
		idleTimer = time.NewTimer(idle)
		defer idleTimer.Stop()
		idleC = idleTimer.C
	}
	for {
		select {
		case chunk, ok := <-source:
			if !ok {
				sourceClosed = true
				return
			}
			if idleTimer != nil {
				idleTimer.Reset(idle)
			}
			if !send(chunk) {
				sendFinal(ctx.Err())
				return
			}
		case <-ctx.Done():
			sendFinal(ctx.Err())
			return
		case <-idleC:
			sendFinal(fmt.Errorf("%w after %s", errStreamIdleTimeout, idle))
			return
		}
	}
}

func drainChunks(source <-chan provider.ChatResponseChunk) {
	for range source {
	}
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
		if strings.TrimSpace(variant.Condition) != "" && !r.matchesCondition(variant.Condition, req) {
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

// recordAttemptFailure counts a failed provider call against the circuit
// breaker unless the failure was caused by the caller: a cancelled request
// (client disconnect) or an expired parent deadline says nothing about the
// provider's health, and counting it would let a handful of aborted requests
// take a healthy provider out of rotation for every tenant.
func (r *Router) recordAttemptFailure(ctx context.Context, providerName string, err error) {
	if !countsAsProviderFailure(ctx, err) {
		return
	}
	r.recordFailure(providerName)
}

func (r *Router) recordLatency(providerName string, latency time.Duration) {
	r.mu.Lock()
	tracker := r.latency
	r.mu.Unlock()
	if tracker != nil {
		tracker.record(providerName, latency)
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
	if useRouteFallbacks && route.Strategy != StrategyDirect {
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

	if hint == "" {
		provs = r.applyStrategy(logicalModel, model, route.Strategy, provs)
	}
	return provs
}

func (r *Router) applyStrategy(logicalModel, model string, strategy Strategy, providers []provider.Provider) []provider.Provider {
	if len(providers) <= 1 {
		return providers
	}
	ordered := append([]provider.Provider(nil), providers...)
	switch strategy {
	case StrategyRoundRobin:
		r.mu.Lock()
		index := r.rrCounters[logicalModel]
		r.rrCounters[logicalModel] = index + 1
		r.mu.Unlock()
		offset := index % len(ordered)
		return append(ordered[offset:], ordered[:offset]...)
	case StrategyLeastLatency:
		sort.SliceStable(ordered, func(i, j int) bool {
			left := r.latency.average(ordered[i].Name())
			right := r.latency.average(ordered[j].Name())
			if left == 0 && right == 0 {
				return i < j
			}
			if left == 0 {
				return false
			}
			if right == 0 {
				return true
			}
			return left < right
		})
	case StrategyCostOptimal:
		sort.SliceStable(ordered, func(i, j int) bool {
			left := provider.InputPricePerMillion(priceProviderName(ordered[i].Name(), model), model)
			right := provider.InputPricePerMillion(priceProviderName(ordered[j].Name(), model), model)
			if left == 0 {
				left = 1_000_000
			}
			if right == 0 {
				right = 1_000_000
			}
			return left < right
		})
	}
	return ordered
}

func priceProviderName(providerName, model string) string {
	if strings.HasPrefix(providerName, "@") {
		return guessProvider(model)
	}
	return providerName
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
	duration := routeTimeout(timeout)
	if duration <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, duration)
}

// routeTimeout parses a route timeout; zero means no timeout.
func routeTimeout(timeout string) time.Duration {
	duration, err := time.ParseDuration(strings.TrimSpace(timeout))
	if err != nil || duration <= 0 {
		return 0
	}
	return duration
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

// conditionEnv is the shared CEL environment for conditional variants.
var conditionEnv = sync.OnceValues(func() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("model", cel.StringType),
		cel.Variable("prompt", cel.StringType),
	)
})

// compiledCondition is a conditional-variant expression compiled once.
// A nil program with a non-nil err marks an invalid expression, which never
// matches.
type compiledCondition struct {
	program cel.Program
	err     error
}

func compileCondition(expression string) compiledCondition {
	env, err := conditionEnv()
	if err != nil {
		return compiledCondition{err: err}
	}
	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return compiledCondition{err: issues.Err()}
	}
	if ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
		return compiledCondition{err: fmt.Errorf("expression must return bool, got %s", ast.OutputType())}
	}
	program, err := env.Program(ast)
	if err != nil {
		return compiledCondition{err: err}
	}
	return compiledCondition{program: program}
}

// compileRouteConditions compiles every conditional variant of the routes
// into cache, logging each invalid expression once.
func compileRouteConditions(cache map[string]compiledCondition, routes map[string]Route) {
	for model, route := range routes {
		for _, variant := range route.Variants {
			expression := strings.TrimSpace(variant.Condition)
			if expression == "" {
				continue
			}
			if _, ok := cache[expression]; ok {
				continue
			}
			compiled := compileCondition(expression)
			if compiled.err != nil {
				log.Printf("router: route %q variant %q has an invalid condition %q; the variant will never be selected: %v", model, variant.Name, expression, compiled.err)
			}
			cache[expression] = compiled
		}
	}
}

// matchesCondition evaluates a conditional variant. Every route is stored
// through SetRoute, ReplaceRoutes or CloneWithRoutes, which compile all of
// its conditions, so a missing entry cannot happen and means no match.
func (r *Router) matchesCondition(expression string, req provider.ChatRequest) bool {
	r.mu.Lock()
	compiled := r.conditions[strings.TrimSpace(expression)]
	r.mu.Unlock()
	if compiled.program == nil {
		return false
	}
	result, _, err := compiled.program.Eval(map[string]any{"model": req.Model, "prompt": promptText(req)})
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
	case "response_format":
		return req.ResponseFormat != nil
	case "max_completion_tokens":
		return req.MaxCompletionTokens != nil
	case "n":
		return req.N != nil
	case "seed":
		return req.Seed != nil
	case "presence_penalty":
		return req.PresencePenalty != nil
	case "frequency_penalty":
		return req.FrequencyPenalty != nil
	case "logit_bias":
		return len(req.LogitBias) > 0
	case "user":
		return req.User != ""
	case "stream_options":
		return req.StreamOptions != nil
	case "parallel_tool_calls":
		return req.ParallelToolCalls != nil
	case "reasoning_effort":
		return req.ReasoningEffort != ""
	case "service_tier":
		return req.ServiceTier != ""
	case "store":
		return req.Store != nil
	case "metadata":
		return len(req.Metadata) > 0
	default:
		_, ok := req.Extra[field]
		return ok
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
	case "max_completion_tokens":
		number, ok := intValue(value)
		if !ok || number < 1 {
			return fmt.Errorf("must be a positive integer")
		}
		req.MaxCompletionTokens = &number
	case "n":
		number, ok := intValue(value)
		if !ok || number < 1 {
			return fmt.Errorf("must be a positive integer")
		}
		req.N = &number
	case "seed":
		number, ok := intValue(value)
		if !ok {
			return fmt.Errorf("must be an integer")
		}
		req.Seed = &number
	case "presence_penalty":
		number, ok := floatValue(value)
		if !ok {
			return fmt.Errorf("must be a number")
		}
		req.PresencePenalty = &number
	case "frequency_penalty":
		number, ok := floatValue(value)
		if !ok {
			return fmt.Errorf("must be a number")
		}
		req.FrequencyPenalty = &number
	case "user":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		req.User = text
	case "parallel_tool_calls":
		flag, ok := value.(bool)
		if !ok {
			return fmt.Errorf("must be a boolean")
		}
		req.ParallelToolCalls = &flag
	case "reasoning_effort":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		req.ReasoningEffort = text
	case "service_tier":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		req.ServiceTier = text
	case "store":
		flag, ok := value.(bool)
		if !ok {
			return fmt.Errorf("must be a boolean")
		}
		req.Store = &flag
	case "logit_bias", "stream_options", "metadata", "response_format":
		// Structured fields: round-trip through JSON so router configs can
		// express them as plain YAML/JSON maps without a bespoke decoder.
		if err := setStructuredRequestField(req, field, value); err != nil {
			return err
		}
	default:
		// Unknown parameter: forward it verbatim via Extra instead of
		// rejecting, so future/beta OpenAI fields configured through
		// default_params/override_params still reach OpenAI-compatible
		// upstreams.
		if err := setExtraRequestField(req, field, value); err != nil {
			return err
		}
	}
	return nil
}

// setStructuredRequestField assigns a map/array-shaped field by marshaling
// the dynamically typed config value to JSON and unmarshaling it into the
// field's concrete Go type.
func setStructuredRequestField(req *provider.ChatRequest, field string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("must be JSON-encodable: %w", err)
	}
	switch field {
	case "logit_bias":
		var bias map[string]int
		if err := json.Unmarshal(data, &bias); err != nil {
			return fmt.Errorf("must be a map of token to bias: %w", err)
		}
		req.LogitBias = bias
	case "stream_options":
		var opts provider.StreamOptions
		if err := json.Unmarshal(data, &opts); err != nil {
			return fmt.Errorf("must be a stream_options object: %w", err)
		}
		req.StreamOptions = &opts
	case "metadata":
		var metadata map[string]string
		if err := json.Unmarshal(data, &metadata); err != nil {
			return fmt.Errorf("must be a map of string to string: %w", err)
		}
		req.Metadata = metadata
	case "response_format":
		var format provider.ResponseFormat
		if err := json.Unmarshal(data, &format); err != nil {
			return fmt.Errorf("must be a response_format object: %w", err)
		}
		req.ResponseFormat = &format
	}
	return nil
}

// setExtraRequestField stores an arbitrary config-supplied value under the
// request's Extra map, keyed by its wire field name.
func setExtraRequestField(req *provider.ChatRequest, field string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("must be JSON-encodable: %w", err)
	}
	if req.Extra == nil {
		req.Extra = map[string]json.RawMessage{}
	}
	req.Extra[field] = json.RawMessage(data)
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
	case "response_format":
		req.ResponseFormat = nil
	case "max_completion_tokens":
		req.MaxCompletionTokens = nil
	case "n":
		req.N = nil
	case "seed":
		req.Seed = nil
	case "presence_penalty":
		req.PresencePenalty = nil
	case "frequency_penalty":
		req.FrequencyPenalty = nil
	case "logit_bias":
		req.LogitBias = nil
	case "user":
		req.User = ""
	case "stream_options":
		req.StreamOptions = nil
	case "parallel_tool_calls":
		req.ParallelToolCalls = nil
	case "reasoning_effort":
		req.ReasoningEffort = ""
	case "service_tier":
		req.ServiceTier = ""
	case "store":
		req.Store = nil
	case "metadata":
		req.Metadata = nil
	default:
		delete(req.Extra, field)
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
