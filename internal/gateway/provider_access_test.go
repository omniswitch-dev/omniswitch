package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestProviderAllowlistAcrossInference(t *testing.T) {
	st := newGatewayTestStore(t)
	var blockedCalls int
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "openai", model: "approved-model", content: "approved response"})
	registry.Register(gatewayProvider{name: "private", model: "private-model", calls: &blockedCalls})
	registry.Register(embeddingGatewayProvider{})
	registry.Register(rerankGatewayProvider{})
	rt := router.New(registry)
	rt.SetRoute("fallback", router.Route{Provider: "private", Fallbacks: []string{"openai"}})
	rt.SetRoute("selected", router.Route{Variants: []router.Variant{
		{Provider: "private", Model: "private-model", Weight: 100},
		{Provider: "openai", Model: "approved-model", Weight: 1},
	}})
	rt.SetRoute("restricted-alias", router.Route{Variants: []router.Variant{
		{Provider: "private", Model: "private-model", Weight: 1},
	}})
	rt.SetRoute("embed", router.Route{Provider: "private", Fallbacks: []string{"embeddings"}})
	rt.SetRoute("rank", router.Route{Provider: "private", Fallbacks: []string{"rerank"}})
	h := New(registry, rt, st, nil)
	routes := http.NewServeMux()
	routes.HandleFunc("/v1/chat/completions", h.ChatCompletions)
	routes.HandleFunc("/v1/responses", h.Responses)
	routes.HandleFunc("/v1/messages", h.Messages)
	routes.HandleFunc("/v1/embeddings", h.Embeddings)
	routes.HandleFunc("/v1/rerank", h.Rerank)
	routes.HandleFunc("/v1/models", h.ListModels)
	routes.HandleFunc("/a2a", h.A2A)
	const rawKey = "provider-policy-test"
	hash := sha256.Sum256([]byte(rawKey))
	if err := st.InsertAPIKey(context.Background(), store.APIKey{
		ID: "restricted", Name: "restricted", Enabled: true, CreatedAt: time.Now(),
		KeyHash: hex.EncodeToString(hash[:]), AllowedProviders: []string{"openai", "embeddings", "rerank"},
	}); err != nil {
		t.Fatal(err)
	}
	handler := NewAuthMiddleware(st, true).Wrap(NewAuthorizationMiddleware(true).Wrap(routes))
	for _, tc := range []struct {
		name, path, body, hint, contains string
		status                           int
	}{
		{"fallback", "/v1/chat/completions", `{"model":"fallback","messages":[{"role":"user","content":"hello"}]}`, "", "approved response", 200},
		{"weighted stream", "/v1/chat/completions", `{"model":"selected","stream":true,"messages":[{"role":"user","content":"hello"}]}`, "", `"model":"approved-model"`, 200},
		{"explicit denied", "/v1/chat/completions", `{"model":"fallback","messages":[{"role":"user","content":"hello"}]}`, "PRIVATE", "provider_access_denied", 403},
		{"no eligible variant", "/v1/chat/completions", `{"model":"restricted-alias","messages":[{"role":"user","content":"hello"}]}`, "", "provider_access_denied", 403},
		{"responses stream denied", "/v1/responses", `{"model":"private-model","input":"hello","stream":true}`, "", "provider_access_denied", 403},
		{"messages allowed", "/v1/messages", `{"model":"approved-model","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`, "OpenAI", "approved response", 200},
		{"embeddings fallback", "/v1/embeddings", `{"model":"embed","input":"hello"}`, "", "embedding", 200},
		{"rerank fallback", "/v1/rerank", `{"model":"rank","query":"hello","documents":["hi"]}`, "", "results", 200},
		{"embeddings denied", "/v1/embeddings", `{"model":"private-model","input":"hello"}`, "", "provider_access_denied", 403},
		{"rerank denied", "/v1/rerank", `{"model":"private-model","query":"hello","documents":["hi"]}`, "", "provider_access_denied", 403},
		{"A2A denied", "/a2a", `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"parts":[{"text":"hello"}]},"metadata":{"model":"private-model"}}}`, "", "inference failed (HTTP 403)", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+rawKey)
			req.Header.Set("x-omniswitch-provider", tc.hint)
			req.Header.Set("x-omniswitch-allowed-providers", "private")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.contains) {
				t.Fatalf("response = %d %s", rec.Code, rec.Body)
			}
		})
	}
	if blockedCalls != 0 {
		t.Fatalf("blocked upstream received %d calls", blockedCalls)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var result struct {
		Data []provider.ModelInfo
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, model := range result.Data {
		names = append(names, model.ID)
	}
	if want := []string{"approved-model", "embed", "embedding-model", "fallback", "rank", "rerank-model", "selected"}; !slices.Equal(names, want) {
		t.Fatalf("visible models = %v, want %v", names, want)
	}
}

func TestProviderAllowlistIsolatesSharedCache(t *testing.T) {
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "private", model: "private-model", content: "private response"})
	registry.Register(gatewayProvider{name: "approved", model: "approved-model", content: "approved response"})
	rt := router.New(registry)
	rt.SetRoute("alias", router.Route{Provider: "private", Fallbacks: []string{"approved"}})
	h := New(registry, rt, newGatewayTestStore(t), nil)
	h.SetCacheScope("global")
	h.SetSemanticCache(0.9)

	for _, tc := range []struct {
		prompt, response, cache string
		allowed                 []string
	}{
		{"hello from the support team", "private response", "MISS", nil},
		{"hello from the support team", "approved response", "MISS", []string{"approved"}},
		{"hello from the support team", "approved response", "HIT", []string{"APPROVED"}},
		{"Hello from the support team!", "approved response", "HIT", []string{"approved"}},
		{"hello from the support team", "private response", "MISS", []string{"approved", "private"}},
		{"Hello from the support team!", "private response", "HIT", []string{"private", "approved", "PRIVATE"}},
	} {
		body := `{"model":"alias","messages":[{"role":"user","content":"` + tc.prompt + `"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req = req.WithContext(withIdentity(req.Context(), Identity{AllowedProviders: tc.allowed}))
		rec := httptest.NewRecorder()
		h.ChatCompletions(rec, req)
		if rec.Code != 200 || rec.Header().Get("x-omniswitch-cache") != tc.cache || !strings.Contains(rec.Body.String(), tc.response) {
			t.Fatalf("allowed providers %v: got %d cache=%s body=%s", tc.allowed, rec.Code, rec.Header().Get("x-omniswitch-cache"), rec.Body)
		}
	}
}

func TestProviderAllowlistAppliesToGuardrailsAndShadowTraffic(t *testing.T) {
	registry := provider.NewRegistry()
	var approvedCalls int
	registry.Register(gatewayProvider{name: "approved", model: "approved-model", content: "output marker", calls: &approvedCalls})
	registry.Register(provider.NewAlias("private", gatewayProvider{name: "private", model: "private-model"}))
	rt := router.New(registry)
	engine := guardrail.NewEngineWithConfig(guardrail.Config{Rules: []guardrail.Rule{
		{Name: "input", Stage: "input", Pattern: "input marker", Action: "fallback", Fallback: "@private/private-model"},
		{Name: "output", Stage: "output", Pattern: "output marker", Action: "reroute", RerouteProvider: "@private"},
	}})
	st := newGatewayTestStore(t)
	h := New(registry, rt, st, engine)
	h.SetStreamGuardrailBuffer(true)
	h.SetShadowProvider("@private")

	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req = req.WithContext(withIdentity(req.Context(), Identity{AllowedProviders: []string{"approved"}}))
		rec := httptest.NewRecorder()
		h.ChatCompletions(rec, req)
		return rec
	}
	for _, body := range []string{
		`{"model":"approved-model","messages":[{"role":"user","content":"input marker"}]}`,
		`{"model":"approved-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
	} {
		rec := request(body)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "provider_access_denied") || strings.Contains(rec.Body.String(), "output marker") {
			t.Fatalf("guardrail reroute returned %d %s", rec.Code, rec.Body)
		}
	}
	if approvedCalls != 1 {
		t.Fatalf("approved provider saw %d calls, want one before the output guardrail", approvedCalls)
	}

	// A mirror is either still holding its slot or has already written its
	// shadow log before releasing the slot.
	ctx := withIdentity(context.Background(), Identity{AllowedProviders: []string{"approved"}})
	h.shadow(ctx, rt, "shadow-denied", "trace", "approved", provider.ChatRequest{Model: "approved-model"}, "@private")
	if len(h.shadowSlots) != 0 {
		t.Fatal("disallowed shadow traffic was queued")
	}
	rows, err := st.ListShadowLogs(ctx, "shadow-denied")
	if err != nil || len(rows) > 0 {
		t.Fatalf("shadow logs = %v, error = %v", rows, err)
	}
}
