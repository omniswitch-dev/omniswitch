package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// TestBudgetsAreComparedToTheirOwnMeasure covers STORE-02: budget_usd caps
// lifetime spend_usd, while the monthly budgets cap usage since the start of
// the current UTC month. Before the fix a monthly-only key was compared to
// its lifetime spend and stayed blocked after its first expensive month.
func TestBudgetsAreComparedToTheirOwnMeasure(t *testing.T) {
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	thisMonth := monthStart.Add(time.Second)
	if thisMonth.After(now) {
		thisMonth = now
	}
	lastMonth := monthStart.Add(-time.Hour)

	type logRow struct {
		at     time.Time
		cost   float64
		tokens int
	}
	tests := []struct {
		name       string
		key        store.APIKey
		logs       []logRow
		wantStatus int
		wantReason string
	}{
		{
			name:       "monthly budget ignores lifetime spend",
			key:        store.APIKey{MonthlyCostBudget: 1, SpendUSD: 50},
			logs:       []logRow{{at: thisMonth, cost: 0.5}, {at: lastMonth, cost: 49.5}},
			wantStatus: http.StatusOK,
		},
		{
			name:       "monthly cost budget reached",
			key:        store.APIKey{MonthlyCostBudget: 1, SpendUSD: 1},
			logs:       []logRow{{at: thisMonth, cost: 1}},
			wantStatus: http.StatusPaymentRequired,
			wantReason: "monthly cost budget exceeded",
		},
		{
			name:       "lifetime budget reached",
			key:        store.APIKey{BudgetUSD: 2, SpendUSD: 2},
			wantStatus: http.StatusPaymentRequired,
			wantReason: "cost budget exceeded",
		},
		{
			name:       "lifetime budget ignores the monthly window",
			key:        store.APIKey{BudgetUSD: 2, SpendUSD: 1},
			logs:       []logRow{{at: thisMonth, cost: 1.5}},
			wantStatus: http.StatusOK,
		},
		{
			name:       "monthly token budget reached",
			key:        store.APIKey{BudgetUSD: 100, MonthlyTokenBudget: 100},
			logs:       []logRow{{at: thisMonth, tokens: 100}, {at: lastMonth, tokens: 1000}},
			wantStatus: http.StatusPaymentRequired,
			wantReason: "monthly token budget exceeded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newGatewayTestStore(t)
			ctx := context.Background()
			hash := sha256.Sum256([]byte("sk-budget-" + tt.name))
			key := tt.key
			key.ID, key.Name, key.KeyHash, key.KeyPrefix = "key_budget", "budget", hex.EncodeToString(hash[:]), "sk-budget-..."
			key.CreatedAt, key.RateLimit, key.Enabled = now, 60, true
			if err := st.InsertAPIKey(ctx, key); err != nil {
				t.Fatal(err)
			}
			for i, row := range tt.logs {
				if err := st.InsertLog(ctx, store.RequestLog{
					ID: "req_" + string(rune('a'+i)), Timestamp: row.at, APIKeyID: "key_budget",
					Status: "success", Decision: "ALLOW", Cost: row.cost, TotalTokens: row.tokens,
				}); err != nil {
					t.Fatal(err)
				}
			}

			registry := provider.NewRegistry()
			registry.Register(gatewayProvider{name: "test", model: "test-model"})
			handler := New(registry, router.New(registry), st, guardrail.NewEngine())
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`))
			req.Header.Set("x-omniswitch-key-id", "key_budget")
			handler.ChatCompletions(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantReason != "" && !strings.Contains(rec.Body.String(), tt.wantReason) {
				t.Fatalf("body = %s, want reason %q", rec.Body.String(), tt.wantReason)
			}
		})
	}
}
