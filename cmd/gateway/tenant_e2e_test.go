package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/admin"
	"github.com/omniswitch-dev/omniswitch/internal/dashboard"
	"github.com/omniswitch-dev/omniswitch/internal/eval"
	"github.com/omniswitch-dev/omniswitch/internal/feedback"
	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/org"
	"github.com/omniswitch-dev/omniswitch/internal/prompt"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
	"github.com/omniswitch-dev/omniswitch/internal/vault"
)

// tenantMockProvider answers every chat request with a fixed reply and
// cost, without the network.
type tenantMockProvider struct{}

func (tenantMockProvider) Name() string { return "mock" }

func (tenantMockProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "mock-model", Object: "model", OwnedBy: "mock", Provider: "mock"}}
}

func (tenantMockProvider) ChatCompletion(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	usage := provider.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}
	return provider.ChatResponse{
			ID: "chat_mock", Object: "chat.completion", Created: time.Now().Unix(), Model: req.Model,
			Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
			Usage:   usage,
		}, provider.ProviderMeta{Provider: "mock", Model: req.Model, Latency: time.Millisecond, Timestamp: time.Now().UTC(), Cost: 0.25},
		nil
}

// TestTenantIsolationEndToEnd covers SEC-3 part B through the real
// middleware chain: two workspace-scoped admin keys send requests through
// a mock provider, and each sees only its own logs, traces, metrics, cost
// analytics and feedback, while a global owner sees everything.
func TestTenantIsolationEndToEnd(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	for _, o := range []string{"org-a", "org-b"} {
		if err := st.InsertOrganization(ctx, store.Organization{ID: o, Name: o}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.InsertWorkspace(ctx, store.Workspace{ID: "ws-a", OrganizationID: "org-a", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertWorkspace(ctx, store.Workspace{ID: "ws-b", OrganizationID: "org-b", Name: "b"}); err != nil {
		t.Fatal(err)
	}
	settings := runtimeSettings{
		authEnabled: true, bootstrapAPIKey: routeTestKey, rateLimitRequests: 1000, rateLimitWindow: time.Minute,
		prometheusEnabled: true,
	}
	if err := ensureBootstrapKey(ctx, st, settings); err != nil {
		t.Fatal(err)
	}
	registry := provider.NewRegistry()
	registry.Register(tenantMockProvider{})
	dash := dashboard.New(st)
	mux := newGatewayMux(routeHandlers{
		settings: settings, registry: registry, gateway: gateway.New(registry, router.New(registry), st, guardrail.NewEngine()),
		admin: admin.New(st), feedback: feedback.New(st), prompt: prompt.New(st), org: org.New(st), eval: eval.NewHandler(st),
		vault: vault.NewHandler(vault.New(st, "")), dash: dash,
	})
	handler, closeRateLimiter, err := wrapGatewayMiddleware(settings, st, mux)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeRateLimiter)
	h := newTopMux(newHealthState(st, registry, false), handler)
	registerPublicMetrics(h, settings, dash)

	keyA := createTestKey(t, h, routeTestKey, `{"name":"a","workspace_id":"ws-a","role":"admin"}`)
	keyB := createTestKey(t, h, routeTestKey, `{"name":"b","workspace_id":"ws-b","role":"admin"}`)

	// Each prompt differs so no response comes from the response cache.
	chat := func(key, trace, prompt string) {
		t.Helper()
		rec := doRequest(h, http.MethodPost, "/v1/chat/completions", key,
			`{"model":"mock-model","messages":[{"role":"user","content":"`+prompt+`"}]}`, map[string]string{"x-omniswitch-trace-id": trace})
		if rec.Code != http.StatusOK {
			t.Fatalf("chat status = %d body = %s", rec.Code, rec.Body.String())
		}
	}
	chat(keyA, "shared", "one")
	chat(keyB, "shared", "two")
	chat(keyB, "only-b", "three")

	getJSON := func(key, path string, out any) int {
		t.Helper()
		rec := doRequest(h, http.MethodGet, path, key, "", nil)
		if rec.Code == http.StatusOK && out != nil {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v (%s)", path, err, rec.Body.String())
			}
		}
		return rec.Code
	}
	logsOf := func(key string) []store.RequestLog {
		t.Helper()
		var out struct {
			Logs []store.RequestLog `json:"logs"`
		}
		if code := getJSON(key, "/api/logs", &out); code != http.StatusOK {
			t.Fatalf("GET /api/logs = %d", code)
		}
		return out.Logs
	}

	logsA, logsB, logsAll := logsOf(keyA), logsOf(keyB), logsOf(routeTestKey)
	if len(logsA) != 1 || len(logsB) != 2 || len(logsAll) != 3 {
		t.Fatalf("logs: a=%d b=%d global=%d, want 1, 2, 3", len(logsA), len(logsB), len(logsAll))
	}
	for _, l := range logsA {
		if l.WorkspaceID != "ws-a" || l.OrganizationID != "org-a" {
			t.Fatalf("key A log tenant = %q/%q, want ws-a/org-a", l.WorkspaceID, l.OrganizationID)
		}
	}
	for _, l := range logsB {
		if l.WorkspaceID != "ws-b" || l.OrganizationID != "org-b" {
			t.Fatalf("key B log tenant = %q/%q, want ws-b/org-b", l.WorkspaceID, l.OrganizationID)
		}
	}

	for _, tc := range []struct {
		key      string
		requests int
	}{{keyA, 1}, {keyB, 2}, {routeTestKey, 3}} {
		var m store.Metrics
		if code := getJSON(tc.key, "/api/metrics", &m); code != http.StatusOK || m.TotalRequests != tc.requests {
			t.Errorf("GET /api/metrics = %d, %d requests; want %d", code, m.TotalRequests, tc.requests)
		}
		var pm struct {
			Providers []store.ProviderMetrics `json:"providers"`
		}
		if code := getJSON(tc.key, "/api/metrics/providers", &pm); code != http.StatusOK || len(pm.Providers) != 1 || pm.Providers[0].RequestCount != tc.requests {
			t.Errorf("GET /api/metrics/providers = %d, %+v; want %d requests", code, pm.Providers, tc.requests)
		}
		var cost struct {
			Rows []store.CostAnalyticsRow `json:"rows"`
		}
		want := 0.25 * float64(tc.requests)
		if code := getJSON(tc.key, "/api/analytics/cost?groupBy=provider", &cost); code != http.StatusOK || len(cost.Rows) != 1 || cost.Rows[0].TotalCost != want {
			t.Errorf("GET /api/analytics/cost = %d, %+v; want cost %v", code, cost.Rows, want)
		}
	}

	// Traces: the shared trace id shows each tenant only its own part; a
	// trace of the other tenant is not found.
	for _, tc := range []struct {
		key, trace string
		want       int
	}{{keyA, "shared", 1}, {keyB, "shared", 1}, {routeTestKey, "shared", 2}, {keyA, "only-b", 0}, {keyB, "only-b", 1}} {
		var detail store.TraceDetail
		code := getJSON(tc.key, "/api/traces/"+tc.trace, &detail)
		if tc.want == 0 {
			if code != http.StatusNotFound {
				t.Errorf("GET /api/traces/%s = %d, want 404", tc.trace, code)
			}
			continue
		}
		if code != http.StatusOK || len(detail.Logs) != tc.want {
			t.Errorf("GET /api/traces/%s = %d with %d logs, want %d", tc.trace, code, len(detail.Logs), tc.want)
		}
	}

	// Feedback: a key may rate its own request but not the other tenant's.
	postFeedback := func(key, body string) int {
		return doRequest(h, http.MethodPost, "/api/feedback", key, body, nil).Code
	}
	if code := postFeedback(keyA, fmt.Sprintf(`{"request_id":%q,"score":1}`, logsA[0].ID)); code != http.StatusCreated {
		t.Fatalf("key A feedback on own request = %d, want 201", code)
	}
	if code := postFeedback(keyA, fmt.Sprintf(`{"request_id":%q,"score":-1}`, logsB[0].ID)); code != http.StatusNotFound {
		t.Fatalf("key A feedback on key B's request = %d, want 404", code)
	}
	if code := postFeedback(keyA, `{"trace_id":"only-b","score":-1}`); code != http.StatusNotFound {
		t.Fatalf("key A feedback on key B's trace = %d, want 404", code)
	}
	if code := postFeedback(keyB, `{"trace_id":"only-b","score":1}`); code != http.StatusCreated {
		t.Fatalf("key B feedback on own trace = %d, want 201", code)
	}
	for _, tc := range []struct {
		key  string
		want int
	}{{keyA, 1}, {keyB, 1}, {routeTestKey, 2}} {
		var out struct {
			Feedback []store.Feedback `json:"feedback"`
		}
		if code := getJSON(tc.key, "/api/feedback", &out); code != http.StatusOK || len(out.Feedback) != tc.want {
			t.Errorf("GET /api/feedback = %d with %d entries, want %d", code, len(out.Feedback), tc.want)
		}
	}
	var trace store.TraceDetail
	if code := getJSON(keyB, "/api/traces/only-b", &trace); code != http.StatusOK || len(trace.Feedback) != 1 {
		t.Errorf("key B trace feedback = %d, %+v; want its own entry", code, trace.Feedback)
	}

	// Gateway-wide endpoints are for global callers only.
	for _, path := range []string{"/api/configs", "/api/config/raw", "/metrics"} {
		for _, key := range []string{keyA, keyB} {
			if code := getJSON(key, path, nil); code != http.StatusForbidden {
				t.Errorf("scoped GET %s = %d, want 403", path, code)
			}
		}
	}
	if code := getJSON(routeTestKey, "/api/configs", nil); code != http.StatusOK {
		t.Errorf("global GET /api/configs = %d, want 200", code)
	}
	if code := getJSON(routeTestKey, "/metrics", nil); code != http.StatusOK {
		t.Errorf("global GET /metrics = %d, want 200", code)
	}
	if code := getJSON(keyA, "/api/health", nil); code != http.StatusOK {
		t.Errorf("scoped GET /api/health = %d, want 200", code)
	}
}

// With OMNISWITCH_METRICS_PUBLIC=true, /metrics stays public, also for
// scoped callers.
func TestPublicMetricsStayPublicForScopedCallers(t *testing.T) {
	h := newSecurityTestServer(t, func(s *runtimeSettings) { s.metricsPublic = true })
	if rec := doRequest(h, http.MethodGet, "/metrics", "", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("public GET /metrics without credentials = %d, want 200", rec.Code)
	}
}
