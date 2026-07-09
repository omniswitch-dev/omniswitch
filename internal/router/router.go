package router

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"

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
	Provider   string    `json:"provider" yaml:"provider"`
	Fallbacks  []string  `json:"fallbacks,omitempty" yaml:"fallbacks,omitempty"`
	MaxRetries int       `json:"max_retries,omitempty" yaml:"max_retries,omitempty"`
	Variants   []Variant `json:"variants,omitempty" yaml:"variants,omitempty"`
}

type Variant struct {
	Name     string `json:"name" yaml:"name"`
	Provider string `json:"provider" yaml:"provider"`
	Model    string `json:"model" yaml:"model"`
	Weight   int    `json:"weight" yaml:"weight"`
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

// Execute routes a chat request to the appropriate provider,
// handling retries and fallbacks automatically.
func (r *Router) Execute(ctx context.Context, req provider.ChatRequest, providerHint string) (provider.ChatResponse, provider.ProviderMeta, error) {
	if providerHint == "" {
		if variant, ok := r.selectVariant(req.Model); ok {
			providerHint = variant.Provider
			if variant.Model != "" {
				req.Model = variant.Model
			}
		}
	}
	providers := r.resolveProviders(req.Model, providerHint)
	if len(providers) == 0 {
		return provider.ChatResponse{}, provider.ProviderMeta{}, fmt.Errorf("no provider found for model %q", req.Model)
	}

	var lastErr error
	for i, prov := range providers {
		if !r.allowProvider(prov.Name()) {
			lastErr = fmt.Errorf("provider %q circuit is open", prov.Name())
			continue
		}
		maxRetries := 1
		if route, ok := r.routes[req.Model]; ok {
			maxRetries = route.MaxRetries + 1
		}

		for attempt := 0; attempt < maxRetries; attempt++ {
			resp, meta, err := prov.ChatCompletion(ctx, req)
			if err == nil {
				r.recordSuccess(prov.Name())
				meta.Retries = attempt
				meta.Fallback = i > 0
				if meta.Model == "" {
					meta.Model = req.Model
				}
				return resp, meta, nil
			}
			r.recordFailure(prov.Name())
			lastErr = err
			meta.Retries = attempt

			// Don't retry on context cancellation.
			if ctx.Err() != nil {
				return provider.ChatResponse{}, meta, ctx.Err()
			}

			// Brief backoff before retry.
			if attempt < maxRetries-1 {
				time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
			}
		}
	}

	return provider.ChatResponse{}, provider.ProviderMeta{
		Error: errorString(lastErr),
	}, fmt.Errorf("all providers exhausted: %w", lastErr)
}

func (r *Router) ExecuteStream(ctx context.Context, req provider.ChatRequest, providerHint string) (<-chan provider.ChatResponseChunk, provider.ProviderMeta, error) {
	if providerHint == "" {
		if variant, ok := r.selectVariant(req.Model); ok {
			providerHint = variant.Provider
			if variant.Model != "" {
				req.Model = variant.Model
			}
		}
	}

	providers := r.resolveProviders(req.Model, providerHint)
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
			chunks, meta, err := streamer.ChatCompletionStream(ctx, req)
			if err == nil {
				r.recordSuccess(prov.Name())
				meta.Fallback = i > 0
				if meta.Model == "" {
					meta.Model = req.Model
				}
				return chunks, meta, nil
			}
			r.recordFailure(prov.Name())
			lastErr = err
			if ctx.Err() != nil {
				return nil, meta, ctx.Err()
			}
			continue
		}

		resp, meta, err := prov.ChatCompletion(ctx, req)
		if err == nil {
			r.recordSuccess(prov.Name())
			meta.Fallback = i > 0
			if meta.Model == "" {
				meta.Model = req.Model
			}
			return provider.StreamFromResponse(ctx, resp), meta, nil
		}
		r.recordFailure(prov.Name())
		lastErr = err
		if ctx.Err() != nil {
			return nil, meta, ctx.Err()
		}
	}

	return nil, provider.ProviderMeta{Error: errorString(lastErr)}, fmt.Errorf("all providers exhausted: %w", lastErr)
}

func (r *Router) selectVariant(model string) (Variant, bool) {
	r.mu.Lock()
	route, ok := r.routes[model]
	r.mu.Unlock()
	if !ok || len(route.Variants) == 0 {
		return Variant{}, false
	}

	total := 0
	for _, variant := range route.Variants {
		if variant.Weight > 0 {
			total += variant.Weight
		}
	}
	if total <= 0 {
		return Variant{}, false
	}

	roll := randomInt(total)
	accumulated := 0
	for _, variant := range route.Variants {
		if variant.Weight <= 0 {
			continue
		}
		accumulated += variant.Weight
		if roll < accumulated {
			return variant, true
		}
	}
	return route.Variants[len(route.Variants)-1], true
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
func (r *Router) resolveProviders(model, hint string) []provider.Provider {
	var provs []provider.Provider

	// If a provider is explicitly requested via header, use it.
	if hint != "" {
		if p, err := r.registry.Get(hint); err == nil {
			provs = append(provs, p)
		}
	}

	// Check for a configured route.
	r.mu.Lock()
	route, hasRoute := r.routes[model]
	r.mu.Unlock()

	if hasRoute && len(provs) == 0 {
		if p, err := r.registry.Get(route.Provider); err == nil {
			provs = append(provs, p)
		}
		for _, fb := range route.Fallbacks {
			if p, err := r.registry.Get(fb); err == nil {
				provs = append(provs, p)
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

func guessProvider(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.HasPrefix(lower, "gpt") || strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3"):
		return "openai"
	case strings.HasPrefix(lower, "claude"):
		return "anthropic"
	case strings.HasPrefix(lower, "gemini"):
		return "google"
	case strings.HasPrefix(lower, "llama") || strings.HasPrefix(lower, "mixtral") || strings.HasPrefix(lower, "gemma"):
		return "groq"
	default:
		return "openai"
	}
}
