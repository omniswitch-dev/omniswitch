package provider

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestRegistry(t *testing.T) {
	registry := NewRegistry()
	registry.Register(testProvider{name: "OpenAI", model: "gpt-test"})

	if _, err := registry.Get("openai"); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	resolved, err := registry.ResolveModel("gpt-test")
	if err != nil {
		t.Fatalf("ResolveModel() error = %v", err)
	}
	if resolved.Name() != "OpenAI" {
		t.Fatalf("resolved provider = %q, want OpenAI", resolved.Name())
	}
	if len(registry.AllModels()) != 1 || len(registry.Names()) != 1 {
		t.Fatalf("registry models/names not indexed correctly")
	}
}

func TestModelPricingCost(t *testing.T) {
	pricing := ModelPricing{InputPerMillion: 1, OutputPerMillion: 2}
	if got := pricing.Cost(1_000_000, 500_000); got != 2 {
		t.Fatalf("Cost() = %v, want 2", got)
	}
}

func TestEstimateCost(t *testing.T) {
	usage := Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}
	if got := EstimateCost("openai", "gpt-4o-mini", usage); got != 0.75 {
		t.Fatalf("EstimateCost(openai) = %v, want 0.75", got)
	}
	if got := EstimateCost("unknown", "model", usage); got != 0 {
		t.Fatalf("EstimateCost(unknown) = %v, want 0", got)
	}
}

func TestMessageContentPartsRoundTrip(t *testing.T) {
	raw := []byte(`{"role":"user","content":[{"type":"text","text":"describe this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,abc","detail":"low"}}]}`)
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if msg.Content != "describe this" || msg.Text() != "describe this" || len(msg.ContentParts) != 2 {
		t.Fatalf("message = %+v, want text plus two parts", msg)
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !json.Valid(data) || string(data) == `{"role":"user","content":"describe this"}` {
		t.Fatalf("marshaled message = %s, want preserved content array", data)
	}
}

func TestAliasProviderUsesVirtualModelID(t *testing.T) {
	inner := &capturingProvider{name: "openai", model: "gpt-test"}
	alias := NewAlias("openai-prod", inner)
	models := alias.Models()
	if len(models) != 1 || models[0].ID != "@openai-prod/gpt-test" || models[0].Provider != "@openai-prod" {
		t.Fatalf("models = %+v, want virtual model id", models)
	}

	resp, meta, err := alias.ChatCompletion(context.Background(), ChatRequest{Model: "@openai-prod/gpt-test"})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if inner.lastModel != "gpt-test" {
		t.Fatalf("inner model = %q, want stripped model", inner.lastModel)
	}
	if meta.Provider != "@openai-prod" || meta.ProviderType != "openai" || meta.Model != "@openai-prod/gpt-test" {
		t.Fatalf("meta = %+v, want alias provider metadata", meta)
	}
	if resp.Model != "@openai-prod/gpt-test" {
		t.Fatalf("response model = %q, want virtual model", resp.Model)
	}
}

type testProvider struct {
	name  string
	model string
}

func (p testProvider) Name() string {
	return p.name
}

func (p testProvider) Models() []ModelInfo {
	return []ModelInfo{{ID: p.model, Object: "model", OwnedBy: p.name, Provider: p.name}}
}

func (p testProvider) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error) {
	return ChatResponse{}, ProviderMeta{Provider: p.name, Timestamp: time.Now().UTC()}, nil
}

type capturingProvider struct {
	name      string
	model     string
	lastModel string
}

func (p *capturingProvider) Name() string {
	return p.name
}

func (p *capturingProvider) Models() []ModelInfo {
	return []ModelInfo{{ID: p.model, Object: "model", OwnedBy: p.name, Provider: p.name}}
}

func (p *capturingProvider) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error) {
	p.lastModel = req.Model
	return ChatResponse{
		ID:      "chat_alias",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
	}, ProviderMeta{Provider: p.name, Model: req.Model, Timestamp: time.Now().UTC()}, nil
}
