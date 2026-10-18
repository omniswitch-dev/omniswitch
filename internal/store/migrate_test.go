package store

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestMigrateUpgradesLegacyRequestLogs covers STORE-10: a request_logs table
// from before trace_id/session_id existed must upgrade cleanly (the indexes on
// those columns used to be created before the columns were added), and the
// NULLs left in old rows must not break the log, trace and metric queries.
func TestMigrateUpgradesLegacyRequestLogs(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "sentinel.db"))
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE request_logs (id TEXT PRIMARY KEY, timestamp TEXT NOT NULL, provider TEXT, model TEXT, api_key_id TEXT, status TEXT,
			input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0, latency_ms REAL DEFAULT 0, cost REAL DEFAULT 0,
			request_body TEXT, response_body TEXT, decision TEXT, decision_reason TEXT, error_message TEXT, cached INTEGER DEFAULT 0)`,
		`INSERT INTO request_logs (id, timestamp, provider, api_key_id) VALUES ('old', '2026-01-01T00:00:00Z', NULL, NULL)`,
		`CREATE TABLE semantic_cache (id TEXT PRIMARY KEY, created_at TEXT NOT NULL, model TEXT NOT NULL, prompt_text TEXT NOT NULL, vector_json TEXT NOT NULL, response_json TEXT NOT NULL)`,
		`INSERT INTO semantic_cache (id, created_at, model, prompt_text, vector_json, response_json) VALUES ('c-old', '2026-01-01T00:00:00Z', 'm', 'hello', '{"hello":1}', '{"id":"x"}')`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	db.Close()

	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() on legacy database error = %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	logs, total, err := st.ListLogs(ctx, 10, 0, "", "")
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	if total != 1 || len(logs) != 1 || logs[0].ID != "old" {
		t.Fatalf("ListLogs() = %d/%v, want the legacy row", total, logs)
	}
	if _, err := st.GetMetrics(ctx, time.Time{}); err != nil {
		t.Fatalf("GetMetrics() error = %v", err)
	}
	if _, err := st.GetProviderMetrics(ctx, time.Time{}); err != nil {
		t.Fatalf("GetProviderMetrics() error = %v", err)
	}
	if _, ok, err := st.FindSemanticCache(ctx, "m", map[string]float64{"hello": 1}, 0.5); err != nil || !ok {
		t.Fatalf("FindSemanticCache() ok=%v err=%v, want legacy row", ok, err)
	}
}

// TestNullColumnsDoNotBreakScans covers the COALESCE half of STORE-10 on
// every backend.
func TestNullColumnsDoNotBreakScans(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		mustExec(t, st, `INSERT INTO request_logs (id, timestamp, trace_id) VALUES ('old', '2026-01-01T00:00:00Z', 'tr')`)
		mustExec(t, st, `INSERT INTO guardrail_events (id, request_id, timestamp) VALUES ('g', 'old', '2026-01-01T00:00:00Z')`)
		mustExec(t, st, `INSERT INTO shadow_requests (id, request_id, trace_id, timestamp) VALUES ('s', 'old', 'tr', '2026-01-01T00:00:00Z')`)
		mustExec(t, st, `INSERT INTO feedback (id, trace_id, timestamp) VALUES ('f', 'tr', '2026-01-01T00:00:00Z')`)
		mustExec(t, st, `INSERT INTO api_keys (id, name, key_hash, key_prefix, created_at) VALUES ('k', 'n', 'h', 'p', '2026-01-01T00:00:00Z')`)
		mustExec(t, st, `INSERT INTO prompts (id, name, template, created_at, updated_at) VALUES ('p', 'n', 't', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		mustExec(t, st, `INSERT INTO organizations (id, name, created_at) VALUES ('o', 'n', '2026-01-01T00:00:00Z')`)
		mustExec(t, st, `INSERT INTO workspaces (id, organization_id, name, created_at) VALUES ('w', 'o', 'n', '2026-01-01T00:00:00Z')`)
		mustExec(t, st, `INSERT INTO users (id, email, created_at) VALUES ('u', 'a@x', '2026-01-01T00:00:00Z')`)

		if _, _, err := st.ListLogs(ctx, 10, 0, "", ""); err != nil {
			t.Fatalf("ListLogs() error = %v", err)
		}
		detail, err := st.GetTrace(ctx, "tr", "")
		if err != nil {
			t.Fatalf("GetTrace() error = %v", err)
		}
		if len(detail.GuardrailEvents) != 1 || len(detail.ShadowLogs) != 1 || len(detail.Feedback) != 1 {
			t.Fatalf("GetTrace() = %+v, want one of each child record", detail)
		}
		if _, err := st.ListShadowLogs(ctx, "old"); err != nil {
			t.Fatalf("ListShadowLogs() error = %v", err)
		}
		if _, err := st.GetAPIKeyByHash(ctx, "h"); err != nil {
			t.Fatalf("GetAPIKeyByHash() error = %v", err)
		}
		if _, err := st.GetAPIKeyByID(ctx, "k"); err != nil {
			t.Fatalf("GetAPIKeyByID() error = %v", err)
		}
		if _, err := st.ListPrompts(ctx); err != nil {
			t.Fatalf("ListPrompts() error = %v", err)
		}
		if _, err := st.GetPrompt(ctx, "p"); err != nil {
			t.Fatalf("GetPrompt() error = %v", err)
		}
		if _, err := st.ListOrganizations(ctx); err != nil {
			t.Fatalf("ListOrganizations() error = %v", err)
		}
		if _, err := st.ListWorkspaces(ctx, ""); err != nil {
			t.Fatalf("ListWorkspaces() error = %v", err)
		}
		if _, err := st.GetWorkspaceByID(ctx, "w"); err != nil {
			t.Fatalf("GetWorkspaceByID() error = %v", err)
		}
		if _, err := st.ListUsers(ctx); err != nil {
			t.Fatalf("ListUsers() error = %v", err)
		}
	})
}

// TestMoneyColumnsKeepDoublePrecision covers STORE-04. On PostgreSQL, REAL is
// a 4-byte float and accumulated spend drifted after a few digits.
func TestMoneyColumnsKeepDoublePrecision(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		if err := st.InsertAPIKey(ctx, APIKey{ID: "k1", Name: "k", KeyHash: "h", KeyPrefix: "p", CreatedAt: time.Now(), Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if err := st.IncrementAPIKeySpend(ctx, "k1", 123.456789); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 200; i++ {
			if err := st.IncrementAPIKeySpend(ctx, "k1", 0.000123); err != nil {
				t.Fatal(err)
			}
		}
		key, err := st.GetAPIKeyByID(ctx, "k1")
		if err != nil {
			t.Fatal(err)
		}
		if want := 123.456789 + 200*0.000123; math.Abs(key.SpendUSD-want) > 1e-9 {
			t.Fatalf("spend_usd = %.12f, want %.12f", key.SpendUSD, want)
		}
		if err := st.InsertLog(ctx, RequestLog{ID: "x", Timestamp: time.Now().UTC(), Cost: 0.000123456789, LatencyMs: 1234.56789}); err != nil {
			t.Fatal(err)
		}
		logs, _, err := st.ListLogs(ctx, 1, 0, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if logs[0].Cost != 0.000123456789 || logs[0].LatencyMs != 1234.56789 {
			t.Fatalf("log cost/latency = %.15f/%.10f, want exact values", logs[0].Cost, logs[0].LatencyMs)
		}
	})
}

// TestPostgresWidensLegacyRealColumns checks that a database created by an
// older release (float4 columns) is converted to DOUBLE PRECISION, keeping
// the stored values readable.
func TestPostgresWidensLegacyRealColumns(t *testing.T) {
	dsn := newPostgresTestDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE api_keys (id TEXT PRIMARY KEY, name TEXT NOT NULL, key_hash TEXT UNIQUE NOT NULL, key_prefix TEXT NOT NULL,
		created_at TEXT NOT NULL, expires_at TEXT, rate_limit INTEGER DEFAULT 60, budget_usd REAL DEFAULT 0, spend_usd REAL DEFAULT 0,
		enabled INTEGER DEFAULT 1, metadata TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO api_keys (id, name, key_hash, key_prefix, created_at, spend_usd) VALUES ('k', 'n', 'h', 'p', '2026-01-01T00:00:00Z', 0.01)`); err != nil {
		t.Fatal(err)
	}
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	defer st.Close()
	rows, err := db.Query(`SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND data_type = 'real'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		t.Errorf("column %s is still REAL", name)
	}
	key, err := st.GetAPIKeyByID(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if key.SpendUSD != 0.01 {
		t.Fatalf("spend_usd after widening = %.17f, want 0.01", key.SpendUSD)
	}
}

// TestPostgresConcurrentMigrations covers STORE-06: replicas starting at the
// same time against an empty database used to fail on pg_type collisions.
func TestPostgresConcurrentMigrations(t *testing.T) {
	for round := 0; round < 3; round++ {
		dsn := newPostgresTestDSN(t)
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				st, err := OpenPostgres(dsn)
				if err != nil {
					errs <- err
					return
				}
				_ = st.Close()
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: concurrent OpenPostgres error = %v", round, err)
		}
	}
}

// TestMigrateIsIdempotent re-opens an existing database on every backend.
func TestMigrateIsIdempotent(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		for i := 0; i < 2; i++ {
			if err := st.migrate(); err != nil {
				t.Fatalf("migrate() run %d error = %v", i+2, err)
			}
		}
	})
}
