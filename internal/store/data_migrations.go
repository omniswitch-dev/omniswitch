package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// dataMigration is a one-time data fix. Schema statements are idempotent
// and run on every start; a data fix must run exactly once per database, so
// it is recorded by name in schema_migrations.
type dataMigration struct {
	name string
	stmt string
}

// dataMigrations run in order, inside the schema migration transaction.
// Append new entries; never rename or reorder existing ones.
var dataMigrations = []dataMigration{
	{
		// Older releases copied monthly_cost_budget into budget_usd (the
		// lifetime budget) when a key was created with only a monthly
		// budget. Such a key was blocked for good once its total spend
		// reached one month's budget. Clear the copied lifetime budget.
		name: "2026_10_split_lifetime_and_monthly_budgets",
		stmt: `UPDATE api_keys SET budget_usd = 0 WHERE monthly_cost_budget > 0 AND budget_usd = monthly_cost_budget`,
	},
}

func (s *Store) applyDataMigrations(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("migration failed: create schema_migrations: %w", err)
	}
	for _, m := range dataMigrations {
		var applied int
		if err := tx.QueryRowContext(ctx, s.txSQL(`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`), m.name).Scan(&applied); err != nil {
			return fmt.Errorf("migration failed: check %s: %w", m.name, err)
		}
		if applied > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, s.txSQL(m.stmt)); err != nil {
			return fmt.Errorf("migration failed: %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, s.txSQL(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`), m.name, formatTime(time.Now())); err != nil {
			return fmt.Errorf("migration failed: record %s: %w", m.name, err)
		}
	}
	return nil
}

// txSQL rewrites ? placeholders for PostgreSQL in statements that run on a
// transaction directly instead of through the Store helpers.
func (s *Store) txSQL(query string) string {
	if s.pg {
		return rewriteSQL(query)
	}
	return query
}
