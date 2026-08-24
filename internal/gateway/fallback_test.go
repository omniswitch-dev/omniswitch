package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

type trackingProvider struct {
	name  string
	calls int
}

func (p *trackingProvider) Name() string { return p.name }

func (p *trackingProvider) ChatCompletion(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	p.calls++
	return provider.ChatResponse{
		ID: "chat_" + p.name, Object: "chat.completion", Created: 1, Model: req.Model,
		Choices: []provider.Choice{{Index: 0, Message: provider.Message{Role: "assistant", Content: "from " + p.name}, FinishReason: "stop"}},
	}, provider.ProviderMeta{Provider: p.name, Model: req.Model}, nil
}

func (p *trackingProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "safe-model", Object: "model", OwnedBy: p.name}}
}

func TestGuardrailFallbackReroutesRequest(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	primary := &trackingProvider{name: "primary"}
	safe := &trackingProvider{name: "safe"}
	registry.Register(primary)
	registry.Register(safe)

	rules := guardrail.Config{Rules: []guardrail.Rule{{
		Name: "leak-guard", Stage: "input", Pattern: "(?i)internal-secret",
		Action: "fallback", Fallback: "safe/safe-model", Message: "rerouting to safe model",
	}}}
	h := New(registry, router.New(registry), st, guardrail.NewEngineWithConfig(rules))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"safe-model","messages":[{"role":"user","content":"please read internal-secret notes"}]}`))
	rec := httptest.NewRecorder()
	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := jsonUnmarshalHelper(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v %s", err, rec.Body.String())
	}
	if primary.calls != 0 {
		t.Fatalf("primary provider should not have been called")
	}
	if safe.calls != 1 {
		t.Fatalf("expected exactly one call on the safe provider, got %d", safe.calls)
	}
	if !strings.Contains(resp.Model, "safe") && len(resp.Choices) > 0 && !strings.Contains(resp.Choices[0].Message.Content, "safe") {
		t.Fatalf("response not from safe provider: %+v", resp)
	}
}

func jsonUnmarshalHelper(data []byte, v any) error { return json.Unmarshal(data, v) }
