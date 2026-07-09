package provider

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Provider is the interface every LLM backend must implement.
type Provider interface {
	// Name returns the canonical provider name (e.g. "openai", "anthropic").
	Name() string

	// ChatCompletion sends a chat request and returns the response.
	ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error)

	// Models returns the list of models this provider supports.
	Models() []ModelInfo
}

// Registry holds all registered providers keyed by name.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	models    map[string]string // model ID -> provider name
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
		models:    make(map[string]string),
	}
}

// Register adds a provider to the registry and indexes its models.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := strings.ToLower(p.Name())
	r.providers[name] = p
	for _, m := range p.Models() {
		r.models[m.ID] = name
	}
}

// Get returns the provider with the given name.
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[strings.ToLower(name)]
	if !ok {
		return nil, fmt.Errorf("provider %q not registered", name)
	}
	return p, nil
}

// ResolveModel finds the provider that owns the given model ID.
func (r *Registry) ResolveModel(modelID string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	providerName, ok := r.models[modelID]
	if !ok {
		return nil, fmt.Errorf("model %q not found in any provider", modelID)
	}
	return r.providers[providerName], nil
}

// AllModels returns every model across all registered providers.
func (r *Registry) AllModels() []ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := []ModelInfo{}
	for _, p := range r.providers {
		all = append(all, p.Models()...)
	}
	return all
}

// Names returns the list of registered provider names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}
