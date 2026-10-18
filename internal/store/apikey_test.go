package store

import (
	"context"
	"testing"
	"time"
)

// TestInsertAPIKeyKeepsSpend covers STORE-08: the second, separate UPDATE in
// InsertAPIKey reset spend_usd on every upsert, so the bootstrap key's
// spend went back to zero on each restart.
func TestInsertAPIKeyKeepsSpend(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		key := APIKey{ID: "bootstrap-admin", Name: "Bootstrap", KeyHash: "h", KeyPrefix: "p", CreatedAt: time.Now(), RateLimit: 120, Enabled: true, MonthlyCostBudget: 10, MonthlyTokenBudget: 1000}
		if err := st.InsertAPIKey(ctx, key); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetAPIKeyByID(ctx, key.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.BudgetUSD != 10 || got.MonthlyCostBudget != 10 || got.MonthlyTokenBudget != 1000 || got.SpendUSD != 0 {
			t.Fatalf("new key budgets = %+v", got)
		}
		if err := st.IncrementAPIKeySpend(ctx, key.ID, 4.5); err != nil {
			t.Fatal(err)
		}
		key.Name = "Renamed"
		key.KeyHash = "h2"
		key.MonthlyCostBudget = 20
		if err := st.InsertAPIKey(ctx, key); err != nil {
			t.Fatal(err)
		}
		got, err = st.GetAPIKeyByHash(ctx, "h2")
		if err != nil {
			t.Fatal(err)
		}
		if got.SpendUSD != 4.5 {
			t.Fatalf("spend after re-upsert = %v, want 4.5", got.SpendUSD)
		}
		if got.Name != "Renamed" || got.MonthlyCostBudget != 20 || got.BudgetUSD != 20 {
			t.Fatalf("re-upsert did not update config: %+v", got)
		}
	})
}
