package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

// TestChatCompletionsUnsupportedParameterReturnsOpenAIStyle400 verifies that
// when an adapter (Anthropic) cannot honour a request field (seed), the
// gateway returns an OpenAI-style 400 unsupported_parameter error instead of
// a generic 502 provider error or a silently ignored field (A6).
func TestChatCompletionsUnsupportedParameterReturnsOpenAIStyle400(t *testing.T) {
	st := newGatewayTestStore(t)
	registry := provider.NewRegistry()
	registry.Register(provider.NewAnthropic("test-key"))
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"claude-3-5-haiku-20241022",
		"messages":[{"role":"user","content":"hello"}],
		"seed": 42
	}`))
	handler.ChatCompletions(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s, want 400", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"code":"unsupported_parameter"`, `"param":"seed"`, `"type":"invalid_request_error"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body = %s, want to contain %q", body, want)
		}
	}
}
