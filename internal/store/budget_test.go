package store

import (
	"context"
	"testing"
	"time"
)

// TestDataMigrationSplitsCopiedLifetimeBudget covers STORE-02 for existing
// databases: older releases copied monthly_cost_budget into budget_usd, so
// the key was blocked for good once its total spend reached one month's
// budget. The one-time data migration clears the copy and runs only once.
func TestDataMigrationSplitsCopiedLifetimeBudget(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		insert := func(id string, lifetime, monthly float64) {
			t.Helper()
			if err := st.InsertAPIKey(ctx, APIKey{ID: id, Name: id, KeyHash: "h-" + id, KeyPrefix: "p", CreatedAt: time.Now(), Enabled: true, BudgetUSD: lifetime, MonthlyCostBudget: monthly}); err != nil {
				t.Fatal(err)
			}
		}
		budgets := func(id string) (float64, float64) {
			t.Helper()
			key, err := st.GetAPIKeyByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			return key.BudgetUSD, key.MonthlyCostBudget
		}

		insert("copied", 10, 10)   // what older releases stored for a monthly-only key
		insert("distinct", 25, 10) // an explicit lifetime budget
		insert("lifetime", 5, 0)   // a lifetime budget only

		// Simulate a database created before the migration existed.
		mustExec(t, st, "DELETE FROM schema_migrations")
		if err := st.migrate(); err != nil {
			t.Fatalf("migrate() error = %v", err)
		}
		if lifetime, monthly := budgets("copied"); lifetime != 0 || monthly != 10 {
			t.Fatalf("copied key budgets = %v/%v, want 0/10", lifetime, monthly)
		}
		if lifetime, monthly := budgets("distinct"); lifetime != 25 || monthly != 10 {
			t.Fatalf("distinct key budgets = %v/%v, want 25/10", lifetime, monthly)
		}
		if lifetime, _ := budgets("lifetime"); lifetime != 5 {
			t.Fatalf("lifetime-only key budget = %v, want 5", lifetime)
		}

		// The migration is recorded, so equal budgets set on purpose later
		// survive every restart.
		insert("explicit", 7, 7)
		if err := st.migrate(); err != nil {
			t.Fatalf("second migrate() error = %v", err)
		}
		if lifetime, _ := budgets("explicit"); lifetime != 7 {
			t.Fatalf("explicit key lifetime budget = %v after restart, want 7", lifetime)
		}
	})
}

// TestGetBudgetUsageCountsIncurredCost checks that monthly usage counts every
// upstream call that cost money, like spend_usd, and skips cache hits.
func TestGetBudgetUsageCountsIncurredCost(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		since := time.Now().UTC().Add(-time.Hour)
		rows := []RequestLog{
			{ID: "allowed", APIKeyID: "k", Timestamp: time.Now().UTC(), Status: "success", Decision: "ALLOW", Cost: 1, TotalTokens: 10},
			// An output guardrail blocked this response after the upstream
			// call was paid for.
			{ID: "denied", APIKeyID: "k", Timestamp: time.Now().UTC(), Status: "denied", Decision: "DENY", Cost: 2, TotalTokens: 20},
			{ID: "cached", APIKeyID: "k", Timestamp: time.Now().UTC(), Status: "success", Decision: "ALLOW", Cached: true, TotalTokens: 30},
			{ID: "old", APIKeyID: "k", Timestamp: since.Add(-time.Minute), Status: "success", Decision: "ALLOW", Cost: 4, TotalTokens: 40},
			{ID: "other", APIKeyID: "other", Timestamp: time.Now().UTC(), Status: "success", Decision: "ALLOW", Cost: 8, TotalTokens: 80},
		}
		for _, row := range rows {
			if err := st.InsertLog(ctx, row); err != nil {
				t.Fatal(err)
			}
		}
		usage, err := st.GetBudgetUsage(ctx, "k", since)
		if err != nil {
			t.Fatal(err)
		}
		if usage.Cost != 3 || usage.Tokens != 30 {
			t.Fatalf("usage = %+v, want cost 3 and 30 tokens", usage)
		}
	})
}
