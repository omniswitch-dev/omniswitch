package router

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

func TestExecuteUsesFallback(t *testing.T) {
	registry := provider.NewRegistry()
	registry.Register(&fakeProvider{name: "primary", model: "test-model", err: errors.New("primary failed")})
	registry.Register(&fakeProvider{name: "fallback", model: "fallback-model"})

	rtr := New(registry)
	rtr.SetRoute("test-model", Route{Provider: "primary", Fallbacks: []string{"fallback"}})

	resp, meta, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "test-model"}, "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if resp.ID != "chat_fallback" {
		t.Fatalf("response ID = %q, want chat_fallback", resp.ID)
	}
	if !meta.Fallback || meta.Provider != "fallback" {
		t.Fatalf("meta = %+v, want fallback provider", meta)
	}
}

func TestExecuteProviderHint(t *testing.T) {
	registry := provider.NewRegistry()
	registry.Register(&fakeProvider{name: "hinted", model: "other-model"})

	rtr := New(registry)
	_, meta, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "unknown"}, "hinted")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if meta.Provider != "hinted" {
		t.Fatalf("Provider = %q, want hinted", meta.Provider)
	}
}

func TestExecuteWeightedVariant(t *testing.T) {
	registry := provider.NewRegistry()
	registry.Register(&fakeProvider{name: "variant", model: "variant-model"})

	rtr := New(registry)
	rtr.SetRoute("logical-model", Route{
		Variants: []Variant{{Name: "variant", Provider: "variant", Model: "variant-model", Weight: 100}},
	})

	resp, meta, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "logical-model"}, "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if meta.Provider != "variant" || meta.Model != "variant-model" || resp.Model != "variant-model" {
		t.Fatalf("resp/meta = %+v/%+v, want variant-model through variant", resp, meta)
	}
}

func TestExecuteVariantRetainsConfiguredFallback(t *testing.T) {
	registry := provider.NewRegistry()
	registry.Register(&fakeProvider{name: "variant", model: "variant-model", err: errors.New("variant failed")})
	registry.Register(&fakeProvider{name: "fallback", model: "fallback-model"})

	rtr := New(registry)
	rtr.SetRoute("logical-model", Route{
		Fallbacks: []string{"fallback"},
		Variants:  []Variant{{Name: "variant", Provider: "variant", Model: "variant-model", Weight: 100}},
	})

	_, meta, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "logical-model"}, "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !meta.Fallback || meta.Provider != "fallback" {
		t.Fatalf("meta = %+v, want configured fallback after variant failure", meta)
	}
}

func TestTransformRequestUsesDefaultOverrideAndDrop(t *testing.T) {
	registry := provider.NewRegistry()
	rtr := New(registry)
	rtr.SetRoute("logical", Route{
		DefaultParams:  map[string]any{"max_tokens": 32, "temperature": 0.1},
		OverrideParams: map[string]any{"top_p": 0.7},
		DropParams:     []string{"temperature"},
	})

	got, err := rtr.TransformRequest(provider.ChatRequest{Model: "logical"})
	if err != nil {
		t.Fatalf("TransformRequest() error = %v", err)
	}
	if got.MaxTokens == nil || *got.MaxTokens != 32 || got.TopP == nil || *got.TopP != 0.7 || got.Temperature != nil {
		t.Fatalf("transformed request = %+v, want default max tokens, override top_p, and dropped temperature", got)
	}
}

// TestTransformRequestHandlesPassthroughFields verifies default/override/drop
// shaping works for the new passthrough-fidelity fields (A6), including
// fields OmniSwitch does not model by name (forwarded through Extra).
func TestTransformRequestHandlesPassthroughFields(t *testing.T) {
	registry := provider.NewRegistry()
	rtr := New(registry)
	rtr.SetRoute("logical", Route{
		DefaultParams: map[string]any{
			"seed":     42,
			"metadata": map[string]any{"env": "staging"},
		},
		OverrideParams: map[string]any{
			"max_completion_tokens": 256,
			"n":                     2,
			"presence_penalty":      0.3,
			"frequency_penalty":     0.4,
			"user":                  "user-42",
			"parallel_tool_calls":   false,
			"reasoning_effort":      "low",
			"service_tier":          "priority",
			"store":                 true,
			"logit_bias":            map[string]any{"1234": -100},
			"stream_options":        map[string]any{"include_usage": true},
			"response_format":       map[string]any{"type": "json_object"},
			"prediction":            map[string]any{"type": "content", "content": "hi"},
		},
		DropParams: []string{"seed", "unused_field"},
	})

	got, err := rtr.TransformRequest(provider.ChatRequest{Model: "logical"})
	if err != nil {
		t.Fatalf("TransformRequest() error = %v", err)
	}
	if got.Seed != nil {
		t.Fatalf("Seed = %v, want dropped", got.Seed)
	}
	if got.Metadata["env"] != "staging" {
		t.Fatalf("Metadata = %v, want default env=staging", got.Metadata)
	}
	if got.MaxCompletionTokens == nil || *got.MaxCompletionTokens != 256 {
		t.Fatalf("MaxCompletionTokens = %v, want 256", got.MaxCompletionTokens)
	}
	if got.N == nil || *got.N != 2 {
		t.Fatalf("N = %v, want 2", got.N)
	}
	if got.PresencePenalty == nil || *got.PresencePenalty != 0.3 {
		t.Fatalf("PresencePenalty = %v, want 0.3", got.PresencePenalty)
	}
	if got.FrequencyPenalty == nil || *got.FrequencyPenalty != 0.4 {
		t.Fatalf("FrequencyPenalty = %v, want 0.4", got.FrequencyPenalty)
	}
	if got.User != "user-42" {
		t.Fatalf("User = %q, want user-42", got.User)
	}
	if got.ParallelToolCalls == nil || *got.ParallelToolCalls {
		t.Fatalf("ParallelToolCalls = %v, want false", got.ParallelToolCalls)
	}
	if got.ReasoningEffort != "low" {
		t.Fatalf("ReasoningEffort = %q, want low", got.ReasoningEffort)
	}
	if got.ServiceTier != "priority" {
		t.Fatalf("ServiceTier = %q, want priority", got.ServiceTier)
	}
	if got.Store == nil || !*got.Store {
		t.Fatalf("Store = %v, want true", got.Store)
	}
	if got.LogitBias["1234"] != -100 {
		t.Fatalf("LogitBias = %v, want {1234: -100}", got.LogitBias)
	}
	if got.StreamOptions == nil || !got.StreamOptions.IncludeUsage {
		t.Fatalf("StreamOptions = %+v, want include_usage=true", got.StreamOptions)
	}
	if got.ResponseFormat == nil || got.ResponseFormat.Type != "json_object" {
		t.Fatalf("ResponseFormat = %+v, want json_object", got.ResponseFormat)
	}
	if raw, ok := got.Extra["prediction"]; !ok || string(raw) != `{"content":"hi","type":"content"}` && string(raw) != `{"type":"content","content":"hi"}` {
		t.Fatalf("Extra[prediction] = %s, ok=%v, want forwarded prediction object", raw, ok)
	}

	// dropping an unknown field should be a no-op, not an error.
	if _, err := rtr.TransformRequest(provider.ChatRequest{Model: "logical"}); err != nil {
		t.Fatalf("TransformRequest() second call error = %v", err)
	}
}

func TestCircuitBreakerSkipsOpenProvider(t *testing.T) {
	primaryCalls := 0
	registry := provider.NewRegistry()
	registry.Register(&fakeProvider{name: "primary", model: "test-model", err: errors.New("primary failed"), calls: &primaryCalls})
	registry.Register(&fakeProvider{name: "fallback", model: "fallback-model"})

	rtr := New(registry)
	rtr.SetCircuitBreaker(NewCircuitBreaker(1, time.Hour))
	rtr.SetRoute("test-model", Route{Provider: "primary", Fallbacks: []string{"fallback"}})

	for i := 0; i < 2; i++ {
		if _, _, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "test-model"}, ""); err != nil {
			t.Fatalf("Execute(%d) error = %v", i, err)
		}
	}
	if primaryCalls != 1 {
		t.Fatalf("primary calls = %d, want 1 because circuit should open after first failure", primaryCalls)
	}
}

type fakeProvider struct {
	name  string
	model string
	err   error
	calls *int
}

func (f *fakeProvider) Name() string {
	return f.name
}

func (f *fakeProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: f.model, Object: "model", OwnedBy: f.name, Provider: f.name}}
}

func (f *fakeProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	if f.calls != nil {
		*f.calls = *f.calls + 1
	}
	meta := provider.ProviderMeta{Provider: f.name, Model: req.Model, Timestamp: time.Now().UTC()}
	if f.err != nil {
		meta.Error = f.err.Error()
		return provider.ChatResponse{}, meta, f.err
	}
	return provider.ChatResponse{
		ID:      "chat_" + f.name,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []provider.Choice{{Index: 0, Message: provider.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		Usage:   provider.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}, meta, nil
}
