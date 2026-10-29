package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

// dataMigration is a one-time data fix. Schema statements are idempotent
// and run on every start; a data fix must run exactly once per database, so
// it is recorded by name in schema_migrations.
type dataMigration struct {
	name  string
	apply func(ctx context.Context, s *Store, tx *sql.Tx) error
}

// dataMigrations run in order, inside the schema migration transaction.
// Append new entries; never rename or reorder existing ones.
var dataMigrations = []dataMigration{
	{name: splitBudgetsMigration, apply: splitLifetimeAndMonthlyBudgets},
}

const splitBudgetsMigration = "2026_10_split_lifetime_and_monthly_budgets"

// maxLoggedKeys bounds the key IDs that one migration log line lists.
const maxLoggedKeys = 100

// splitLifetimeAndMonthlyBudgets clears the lifetime budget that older
// releases copied from monthly_cost_budget when a key was created with only
// a monthly budget. Such a key was blocked for good once its total spend
// reached one month's budget. A key whose two budgets were set equal on
// purpose looks the same in the database, so every key it changes is
// logged with its old value: the operator can set a lifetime budget again.
func splitLifetimeAndMonthlyBudgets(ctx context.Context, s *Store, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `UPDATE api_keys SET budget_usd = 0
		WHERE monthly_cost_budget > 0 AND budget_usd = monthly_cost_budget
		RETURNING id, monthly_cost_budget`)
	if err != nil {
		return err
	}
	var cleared []string
	total := 0
	for rows.Next() {
		var id string
		var budget float64
		if err := rows.Scan(&id, &budget); err != nil {
			rows.Close()
			return err
		}
		total++
		if len(cleared) < maxLoggedKeys {
			cleared = append(cleared, fmt.Sprintf("%s (%s)", id, strconv.FormatFloat(budget, 'f', -1, 64)))
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if total == 0 {
		return nil
	}
	sort.Strings(cleared)
	list := strings.Join(cleared, ", ")
	if total > len(cleared) {
		list += fmt.Sprintf(" and %d more", total-len(cleared))
	}
	log.Printf("store: migration %s: removed the lifetime budget (budget_usd) from %d API key(s) whose budget_usd was equal to monthly_cost_budget, "+
		"because older releases copied the monthly budget into it. Each key keeps its monthly budget. Keys and old values in USD: %s. "+
		"To keep a lifetime budget on one of these keys, set it again: UPDATE api_keys SET budget_usd = <usd> WHERE id = '<id>'",
		splitBudgetsMigration, total, list)
	return nil
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
		if err := m.apply(ctx, s, tx); err != nil {
			return fmt.Errorf("migration failed: %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, s.txSQL(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`), m.name, formatTime(time.Now())); err != nil {
			return fmt.Errorf("migration failed: record %s: %w", m.name, err)
		}
	}
	return nil
}
