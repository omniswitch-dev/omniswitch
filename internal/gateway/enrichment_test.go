package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

type enrichmentProvider struct {
	calls   int
	request provider.ChatRequest
}

func (p *enrichmentProvider) Name() string { return "enrichment" }
func (p *enrichmentProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "support", Object: "model"}}
}
func (p *enrichmentProvider) ChatCompletion(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	p.calls++
	p.request = req
	return provider.ChatResponse{ID: "chat_enriched", Model: req.Model, Object: "chat.completion", Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "answer"}, FinishReason: "stop"}}}, provider.ProviderMeta{Provider: p.Name(), Model: req.Model}, nil
}

func TestPromptEnrichmentScannedAndSeparatesCache(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			st := newGatewayTestStore(t)
			registry := provider.NewRegistry()
			upstream := &enrichmentProvider{}
			registry.Register(upstream)
			rtr := router.New(registry)
			route := router.Route{Provider: upstream.Name(), PrependMessages: []provider.Message{{Role: "system", Content: "first instruction"}}}
			rtr.SetRoute("support", route)
			h := New(registry, rtr, st, nil)
			h.SetSemanticCache(0.95)
			call := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"support","messages":[{"role":"user","content":"question"}],"stream":%v}`, stream)))
				rec := httptest.NewRecorder()
				h.ChatCompletions(rec, req)
				return rec
			}
			for i := 0; i < 2; i++ {
				if rec := call(); rec.Code != 200 {
					t.Fatalf("status/body = %d/%s", rec.Code, rec.Body)
				}
			}
			if upstream.calls != 1 || len(upstream.request.Messages) != 2 || upstream.request.Messages[0].Content != "first instruction" {
				t.Fatalf("calls/request = %d/%+v", upstream.calls, upstream.request)
			}
			// Identical token frequencies must still miss after a policy change.
			route.PrependMessages = []provider.Message{{Role: "system", Content: "instruction first"}}
			rtr.SetRoute("support", route)
			if rec := call(); rec.Code != 200 || upstream.calls != 2 {
				t.Fatalf("new instructions reused old cache: status=%d calls=%d body=%s", rec.Code, upstream.calls, rec.Body)
			}
			h.SetGuardrails(guardrail.NewEngineWithConfig(guardrail.Config{Rules: []guardrail.Rule{{Name: "scan-enrichment", Stage: "input", Pattern: "instruction first", Action: "deny"}}}))
			if rec := call(); rec.Code != 403 || upstream.calls != 2 {
				t.Fatalf("enrichment bypassed guardrails: status=%d calls=%d", rec.Code, upstream.calls)
			}
		})
	}
}

func TestModelRestrictionCannotBeBypassedThroughA2A(t *testing.T) {
	registry := provider.NewRegistry()
	upstream := &enrichmentProvider{}
	registry.Register(upstream)
	h := New(registry, router.New(registry), newGatewayTestStore(t), nil)
	req := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"parts":[{"text":"question"}]},"metadata":{"model":"support"}}}`))
	req = req.WithContext(withIdentity(req.Context(), Identity{AllowedModels: []string{"other"}}))
	rec := httptest.NewRecorder()
	h.A2A(rec, req)
	if upstream.calls != 0 || !strings.Contains(rec.Body.String(), "inference failed") {
		t.Fatalf("A2A bypass: calls=%d body=%s", upstream.calls, rec.Body)
	}
}
