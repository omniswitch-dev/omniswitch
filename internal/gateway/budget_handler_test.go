package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// pricedProvider is a slow "openai" provider for gpt-4o-mini whose cost
// is computed from the catalog price, like the real adapters do.
type pricedProvider struct {
	delay  time.Duration
	calls  *atomic.Int64
	fail   bool
	usage  provider.Usage
	stream func(ctx context.Context, chunks chan<- provider.ChatResponseChunk)
}

func (p pricedProvider) Name() string { return "openai" }

func (p pricedProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "gpt-4o-mini", Object: "model", OwnedBy: "openai", Provider: "openai"}}
}

func (p pricedProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	if p.calls != nil {
		p.calls.Add(1)
	}
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
		return provider.ChatResponse{}, provider.ProviderMeta{Provider: "openai"}, ctx.Err()
	}
	meta := provider.ProviderMeta{Provider: "openai", Model: req.Model, Timestamp: time.Now().UTC()}
	if p.fail {
		return provider.ChatResponse{}, meta, errors.New("upstream failed")
	}
	meta.Cost = provider.EstimateCost("openai", req.Model, p.usage)
	return provider.ChatResponse{
		ID: "chat_priced", Object: "chat.completion", Model: req.Model,
		Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		Usage:   p.usage,
	}, meta, nil
}

func (p pricedProvider) ChatCompletionStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatResponseChunk, provider.ProviderMeta, error) {
	chunks := make(chan provider.ChatResponseChunk, 4)
	go func() {
		defer close(chunks)
		p.stream(ctx, chunks)
	}()
	return chunks, provider.ProviderMeta{Provider: "openai", Model: req.Model, Timestamp: time.Now().UTC()}, nil
}

func insertBudgetKey(t *testing.T, st *store.Store, secret string, key store.APIKey) store.APIKey {
	t.Helper()
	hash := sha256.Sum256([]byte(secret))
	key.ID, key.Name, key.KeyHash, key.KeyPrefix = "key_"+secret, secret, hex.EncodeToString(hash[:]), secret[:4]+"..."
	key.CreatedAt, key.RateLimit, key.Enabled = time.Now().UTC(), 100000, true
	if err := st.InsertAPIKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	return key
}

// ledgerIdle reports whether every budget entry has no running reservation.
func ledgerIdle(h *Handler) (bool, string) {
	l := h.budget.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	for scope, e := range l.entries {
		if e.reservations != 0 || e.reserved != (budgetAmount{}) || len(e.pending) != 0 {
			return false, scope.ID
		}
	}
	return true, ""
}

const pricedChatBody = `{"model":"gpt-4o-mini","max_tokens":1000,"messages":[{"role":"user","content":"hello"}]}`

// TestBudgetConcurrentRequestsCannotOverspend sends many concurrent requests
// through the real handler chain (auth middleware + gateway) for a key whose
// lifetime budget fits only a few of them. Before GW-14 every request passed
// the check before any was charged.
func TestBudgetConcurrentRequestsCannotOverspend(t *testing.T) {
	for _, monthly := range []bool{false, true} {
		name := map[bool]string{false: "lifetime", true: "monthly"}[monthly]
		t.Run(name, func(t *testing.T) {
			st := newGatewayTestStore(t)
			usage := provider.Usage{PromptTokens: 10, CompletionTokens: 1000, TotalTokens: 1010}
			cost := provider.EstimateCost("openai", "gpt-4o-mini", usage)
			budget := 4.5 * cost
			key := store.APIKey{BudgetUSD: budget}
			if monthly {
				key = store.APIKey{MonthlyCostBudget: budget}
			}
			key = insertBudgetKey(t, st, "sk-concurrent-"+name, key)

			var calls atomic.Int64
			registry := provider.NewRegistry()
			registry.Register(pricedProvider{delay: 30 * time.Millisecond, calls: &calls, usage: usage})
			handler := New(registry, router.New(registry), st, guardrail.NewEngine())
			handler.SetSemanticCache(0)
			chain := NewAuthMiddleware(st, true).Wrap(http.HandlerFunc(handler.ChatCompletions))
			estimate := handler.estimateChatBudget(provider.ChatRequest{Model: "gpt-4o-mini", MaxTokens: intPtr(1000), Messages: []provider.Message{{Role: "user", Content: "hello"}}}, "")
			if estimate.Cost < cost {
				t.Fatalf("estimate %v is below the actual cost %v", estimate.Cost, cost)
			}

			const n = 40
			var wg sync.WaitGroup
			var ok, denied atomic.Int64
			start := make(chan struct{})
			for range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					rec := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(pricedChatBody))
					req.Header.Set("Authorization", "Bearer sk-concurrent-"+name)
					chain.ServeHTTP(rec, req)
					switch rec.Code {
					case http.StatusOK:
						ok.Add(1)
					case http.StatusPaymentRequired:
						if !strings.Contains(rec.Body.String(), "budget_exceeded") {
							t.Errorf("402 body = %s", rec.Body.String())
						}
						denied.Add(1)
					default:
						t.Errorf("status = %d, body = %s", rec.Code, rec.Body.String())
					}
				}()
			}
			close(start)
			wg.Wait()

			stored, err := st.GetAPIKeyByID(context.Background(), key.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.SpendUSD > budget+estimate.Cost {
				t.Fatalf("spend = %v after %d successful requests, want at most budget %v + one estimate %v", stored.SpendUSD, ok.Load(), budget, estimate.Cost)
			}
			if ok.Load() == 0 || denied.Load() == 0 || ok.Load()+denied.Load() != n {
				t.Fatalf("ok/denied = %d/%d, want both and %d total", ok.Load(), denied.Load(), n)
			}
			if calls.Load() != ok.Load() {
				t.Fatalf("upstream calls = %d, want %d (denied requests must not reach the provider)", calls.Load(), ok.Load())
			}
			if idle, id := ledgerIdle(handler); !idle {
				t.Fatalf("reservation of %s not released", id)
			}
		})
	}
}

// TestBudgetAuthChainDeniesExhaustedKey covers the end-to-end path: the auth
// middleware hands the key's budgets to the gateway, and an exhausted key
// gets the existing 402 response.
func TestBudgetAuthChainDeniesExhaustedKey(t *testing.T) {
	st := newGatewayTestStore(t)
	insertBudgetKey(t, st, "sk-exhausted", store.APIKey{BudgetUSD: 1, SpendUSD: 1})
	insertBudgetKey(t, st, "sk-unlimited", store.APIKey{SpendUSD: 100})
	registry := provider.NewRegistry()
	registry.Register(gatewayProvider{name: "test", model: "test-model"})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())
	chain := NewAuthMiddleware(st, true).Wrap(http.HandlerFunc(handler.ChatCompletions))

	send := func(secret string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Authorization", "Bearer "+secret)
		chain.ServeHTTP(rec, req)
		return rec
	}
	if rec := send("sk-exhausted"); rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), "cost budget exceeded: 1.0000 of 1.0000 USD used") {
		t.Fatalf("exhausted key = %d %s, want 402 cost budget exceeded", rec.Code, rec.Body.String())
	}
	if rec := send("sk-unlimited"); rec.Code != http.StatusOK {
		t.Fatalf("unlimited key = %d %s, want 200", rec.Code, rec.Body.String())
	}
	// A key without any budget is not tracked by the ledger.
	handler.budget.ledger.mu.Lock()
	_, tracked := handler.budget.ledger.entries[budgetScope{Kind: budgetScopeAPIKey, ID: "key_sk-unlimited"}]
	handler.budget.ledger.mu.Unlock()
	if tracked {
		t.Fatal("ledger tracks a key that has no budget")
	}
}

// TestBudgetStoreFailureModes: when the budget state of a key with a budget
// cannot be read, the default is HTTP 503; budget_fail_open lets the request
// through; a key without a budget is never blocked. Before GW-14 a store
// error always let the request through.
func TestBudgetStoreFailureModes(t *testing.T) {
	cases := []struct {
		name     string
		failOpen bool
		key      store.APIKey
		want     int
	}{
		{name: "fail closed", key: store.APIKey{ID: "key_budgeted", BudgetUSD: 5}, want: http.StatusServiceUnavailable},
		{name: "fail open", failOpen: true, key: store.APIKey{ID: "key_budgeted", BudgetUSD: 5}, want: http.StatusOK},
		{name: "no budget", key: store.APIKey{ID: "key_free"}, want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newGatewayTestStore(t)
			registry := provider.NewRegistry()
			registry.Register(gatewayProvider{name: "test", model: "test-model"})
			handler := New(registry, router.New(registry), st, guardrail.NewEngine())
			handler.SetBudgetFailOpen(tc.failOpen)
			_ = st.Close() // every store call now fails

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`))
			req = req.WithContext(withAPIKeyBudget(req.Context(), tc.key))
			req.Header.Set("x-omniswitch-key-id", tc.key.ID)
			handler.ChatCompletions(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusServiceUnavailable {
				body := rec.Body.String()
				if !strings.Contains(body, `"code":"budget_unavailable"`) || !strings.Contains(body, budgetUnavailableMessage) || strings.Contains(body, "sql") {
					t.Fatalf("body = %s, want a budget_unavailable error without internal details", body)
				}
			}
		})
	}
}

// TestBudgetReservationReleasedOnEveryPath checks that a request's
// reservation is released after an upstream error, a client cancel, a
// normal stream end, embeddings and rerank, and that the actual cost is
// charged.
func TestBudgetReservationReleasedOnEveryPath(t *testing.T) {
	usage := provider.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}
	streamDone := func(ctx context.Context, chunks chan<- provider.ChatResponseChunk) {
		chunks <- provider.ChatResponseChunk{ID: "s", Object: "chat.completion.chunk", Model: "gpt-4o-mini", Choices: []provider.ChunkChoice{{Delta: provider.Message{Content: "hi"}}}}
		chunks <- provider.ChatResponseChunk{ID: "s", Object: "chat.completion.chunk", Model: "gpt-4o-mini", Choices: []provider.ChunkChoice{{FinishReason: "stop"}}, Usage: &usage}
	}
	streamUntilCancel := func(ctx context.Context, chunks chan<- provider.ChatResponseChunk) {
		chunks <- provider.ChatResponseChunk{ID: "s", Object: "chat.completion.chunk", Model: "gpt-4o-mini", Choices: []provider.ChunkChoice{{Delta: provider.Message{Content: "hi"}}}}
		<-ctx.Done()
	}
	cases := []struct {
		name     string
		provider pricedProvider
		body     string
		cancel   bool
	}{
		{name: "upstream error", provider: pricedProvider{fail: true}, body: pricedChatBody},
		{name: "success", provider: pricedProvider{usage: usage}, body: pricedChatBody},
		{name: "stream end", provider: pricedProvider{stream: streamDone}, body: `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hello"}]}`},
		{name: "client cancel", provider: pricedProvider{stream: streamUntilCancel}, body: `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hello"}]}`, cancel: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newGatewayTestStore(t)
			key := insertBudgetKey(t, st, "sk-release", store.APIKey{BudgetUSD: 100, MonthlyTokenBudget: 1 << 30})
			registry := provider.NewRegistry()
			registry.Register(tc.provider)
			handler := New(registry, router.New(registry), st, guardrail.NewEngine())
			handler.SetSemanticCache(0)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body)).WithContext(withAPIKeyBudget(ctx, key))
			req.Header.Set("x-omniswitch-key-id", key.ID)
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				handler.ChatCompletions(rec, req)
			}()
			if tc.cancel {
				// Wait until the request holds its reservation, then
				// disconnect the client.
				deadline := time.Now().Add(5 * time.Second)
				for {
					if idle, _ := ledgerIdle(handler); !idle {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("request never reserved its budget")
					}
					time.Sleep(time.Millisecond)
				}
				cancel()
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("handler did not return")
			}
			if idle, id := ledgerIdle(handler); !idle {
				t.Fatalf("reservation of %s not released (status %d)", id, rec.Code)
			}
		})
	}

	t.Run("embeddings and rerank", func(t *testing.T) {
		st := newGatewayTestStore(t)
		key := insertBudgetKey(t, st, "sk-release-aux", store.APIKey{BudgetUSD: 100})
		registry := provider.NewRegistry()
		registry.Register(embeddingGatewayProvider{})
		registry.Register(rerankGatewayProvider{})
		handler := New(registry, router.New(registry), st, guardrail.NewEngine())
		for _, call := range []struct {
			serve func(http.ResponseWriter, *http.Request)
			path  string
			body  string
		}{
			{handler.Embeddings, "/v1/embeddings", `{"model":"embedding-model","input":"hello"}`},
			{handler.Rerank, "/v1/rerank", `{"model":"rerank-model","query":"q","documents":["a"]}`},
		} {
			req := httptest.NewRequest(http.MethodPost, call.path, strings.NewReader(call.body)).WithContext(withAPIKeyBudget(context.Background(), key))
			req.Header.Set("x-omniswitch-key-id", key.ID)
			rec := httptest.NewRecorder()
			call.serve(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s status = %d, body = %s", call.path, rec.Code, rec.Body.String())
			}
		}
		if idle, id := ledgerIdle(handler); !idle {
			t.Fatalf("reservation of %s not released", id)
		}
	})
}

// TestBudgetChargeVisibleImmediately: the next admission sees this
// process's charge before any refresh, without reading the store per
// request, and a refresh reads spend that another process wrote.
func TestBudgetChargeVisibleImmediately(t *testing.T) {
	st := newGatewayTestStore(t)
	usage := provider.Usage{PromptTokens: 10, CompletionTokens: 1000, TotalTokens: 1010}
	cost := provider.EstimateCost("openai", "gpt-4o-mini", usage)
	insertBudgetKey(t, st, "sk-visible", store.APIKey{BudgetUSD: 1.5 * cost})
	other := insertBudgetKey(t, st, "sk-replica", store.APIKey{BudgetUSD: 10 * cost})
	registry := provider.NewRegistry()
	registry.Register(pricedProvider{usage: usage})
	handler := New(registry, router.New(registry), st, guardrail.NewEngine())
	handler.SetSemanticCache(0)
	handler.SetBudgetRefreshInterval(time.Hour)
	chain := NewAuthMiddleware(st, true).Wrap(http.HandlerFunc(handler.ChatCompletions))
	send := func(secret string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(pricedChatBody))
		req.Header.Set("Authorization", "Bearer "+secret)
		chain.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := send("sk-visible"); code != http.StatusOK {
		t.Fatalf("first request = %d", code)
	}
	if code := send("sk-visible"); code != http.StatusPaymentRequired {
		t.Fatalf("second request = %d, want 402 from the local charge", code)
	}

	if code := send("sk-replica"); code != http.StatusOK {
		t.Fatalf("replica key first request = %d", code)
	}
	// Another replica spends the rest of the budget.
	if err := st.IncrementAPIKeySpend(context.Background(), other.ID, 9*cost); err != nil {
		t.Fatal(err)
	}
	// Within the refresh interval the snapshot is reused (no per-request
	// store read), so the other replica's spend is not visible yet.
	if code := send("sk-replica"); code != http.StatusOK {
		t.Fatalf("before refresh = %d, want 200 from the cached snapshot", code)
	}
	handler.SetBudgetRefreshInterval(time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	_ = send("sk-replica") // starts the refresh
	handler.budget.ledger.loads.Wait()
	if code := send("sk-replica"); code != http.StatusPaymentRequired {
		t.Fatalf("after refresh = %d, want 402 from the other replica's spend", code)
	}
}
