package store

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer that the log package and a test can share
// while background goroutines still write to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLog sends the standard logger to a buffer until the test ends.
// Store tests do not run in parallel, so swapping the global output is safe.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

func openPostgresDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func columnType(t *testing.T, db *sql.DB, table, column string) string {
	t.Helper()
	var dataType string
	if err := db.QueryRow(`SELECT data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).Scan(&dataType); err != nil {
		t.Fatalf("type of %s.%s: %v", table, column, err)
	}
	return dataType
}

func indexState(t *testing.T, db *sql.DB, name string) (exists, valid bool) {
	t.Helper()
	exists, valid, err := pgIndexState(context.Background(), db, name)
	if err != nil {
		t.Fatalf("state of index %s: %v", name, err)
	}
	return exists, valid
}

// TestPostgresWidenLeavesOtherTablesAlone checks that the REAL to DOUBLE
// PRECISION conversion stays inside the store's own tables: a schema can be
// shared with other applications.
func TestPostgresWidenLeavesOtherTablesAlone(t *testing.T) {
	dsn := newPostgresTestDSN(t)
	db := openPostgresDB(t, dsn)
	if _, err := db.Exec(`CREATE TABLE other_app_metrics (id INTEGER PRIMARY KEY, score REAL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE api_keys (id TEXT PRIMARY KEY, name TEXT NOT NULL, key_hash TEXT UNIQUE NOT NULL, key_prefix TEXT NOT NULL,
		created_at TEXT NOT NULL, expires_at TEXT, rate_limit INTEGER DEFAULT 60, budget_usd REAL DEFAULT 0, spend_usd REAL DEFAULT 0,
		enabled INTEGER DEFAULT 1, metadata TEXT)`); err != nil {
		t.Fatal(err)
	}
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	defer st.Close()
	if got := columnType(t, db, "other_app_metrics", "score"); got != "real" {
		t.Fatalf("other_app_metrics.score = %s, want real (not owned by the store)", got)
	}
	if got := columnType(t, db, "api_keys", "spend_usd"); got != "double precision" {
		t.Fatalf("api_keys.spend_usd = %s, want double precision", got)
	}
}

// TestPostgresRestartTakesNoTableLocks checks that a restart against an
// up-to-date schema changes nothing and so waits for no table lock. Before,
// every start ran ALTER TABLE ... ADD COLUMN IF NOT EXISTS, which queues for
// an ACCESS EXCLUSIVE lock behind any open transaction and then blocks all
// traffic on the table until the migration commits.
func TestPostgresRestartTakesNoTableLocks(t *testing.T) {
	dsn := newPostgresTestDSN(t)
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	defer st.Close()

	db := openPostgresDB(t, dsn)
	ctx := context.Background()
	lockTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockTx.Rollback() }()
	if _, err := lockTx.Exec("LOCK TABLE " + strings.Join(storeTables(), ", ") + " IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- st.migrate() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("migrate() with every table locked error = %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = lockTx.Rollback()
		<-done
		t.Fatal("migrate() waited for a table lock on a schema that needs no change")
	}
}

// TestPostgresMigrationRetriesOnLockTimeout checks that a one-time schema
// change that cannot get its table lock fails fast and is retried, instead
// of waiting without limit and stalling every query queued behind it.
func TestPostgresMigrationRetriesOnLockTimeout(t *testing.T) {
	dsn := newPostgresTestDSN(t)
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	defer st.Close()
	db := openPostgresDB(t, dsn)
	// Simulate a database from before api_keys.role existed.
	if _, err := db.Exec(`ALTER TABLE api_keys DROP COLUMN role`); err != nil {
		t.Fatal(err)
	}

	prevTimeout, prevDelays := pgMigrationLockTimeout, pgMigrationRetryDelays
	pgMigrationLockTimeout = "200ms"
	pgMigrationRetryDelays = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond,
		50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond}
	t.Cleanup(func() { pgMigrationLockTimeout, pgMigrationRetryDelays = prevTimeout, prevDelays })
	logs := captureLog(t)

	// An open read transaction, such as a long report query.
	reader, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Exec(`SELECT COUNT(*) FROM api_keys`); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(600 * time.Millisecond)
		_ = reader.Rollback()
	}()

	if err := st.migrateWithRetry(); err != nil {
		t.Fatalf("migrateWithRetry() error = %v", err)
	}
	if !strings.Contains(logs.String(), "timed out waiting for a table lock") {
		t.Fatalf("logs = %q, want a lock timeout retry", logs.String())
	}
	if got := columnType(t, db, "api_keys", "role"); got != "text" {
		t.Fatalf("api_keys.role = %s after retry, want text", got)
	}
}

// TestPostgresDefersChangesToLargeTables checks the upgrade of a large
// table: the startup transaction neither rewrites it nor indexes it. The
// missing index is built with CREATE INDEX CONCURRENTLY in the background,
// an invalid index left by an interrupted build is rebuilt, the index it
// replaces is dropped, and the REAL column is left for a maintenance window.
func TestPostgresDefersChangesToLargeTables(t *testing.T) {
	dsn := newPostgresTestDSN(t)
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := st.InsertLog(context.Background(), RequestLog{ID: "r" + string(rune('a'+i)), Timestamp: time.Now(), Provider: "openai",
			Model: "gpt-4o", APIKeyID: "k", Status: "success", Cost: 0.25}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	db := openPostgresDB(t, dsn)
	// Recreate what an older release left behind: a REAL cost column, the
	// single-column API key index, no composite index, and an invalid
	// idx_logs_status from a failed concurrent build (a unique index over
	// duplicate values fails that way).
	for _, stmt := range []string{
		`ALTER TABLE request_logs ALTER COLUMN cost TYPE REAL`,
		`DROP INDEX idx_logs_api_key_time`,
		`CREATE INDEX idx_logs_api_key ON request_logs(api_key_id)`,
		`DROP INDEX idx_logs_status`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX CONCURRENTLY idx_logs_status ON request_logs(status)`); err == nil {
		t.Fatal("unique index over duplicate values succeeded, want a failed build")
	}
	if exists, valid := indexState(t, db, "idx_logs_status"); !exists || valid {
		t.Fatalf("idx_logs_status exists=%v valid=%v, want an invalid index", exists, valid)
	}

	prev := pgOnlineRewriteRows
	pgOnlineRewriteRows = 3
	t.Cleanup(func() { pgOnlineRewriteRows = prev })
	logs := captureLog(t)

	st, err = OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres() on a large table error = %v", err)
	}
	defer st.Close()

	if got := columnType(t, db, "request_logs", "cost"); got != "real" {
		t.Fatalf("request_logs.cost = %s, want real (too large to rewrite at startup)", got)
	}
	if !strings.Contains(logs.String(), "ALTER TABLE \"request_logs\" ALTER COLUMN \"cost\" TYPE DOUBLE PRECISION") {
		t.Fatalf("logs = %q, want the statement to run in a maintenance window", logs.String())
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		newExists, newValid := indexState(t, db, "idx_logs_api_key_time")
		oldExists, _ := indexState(t, db, "idx_logs_api_key")
		statusExists, statusValid := indexState(t, db, "idx_logs_status")
		if newExists && newValid && !oldExists && statusExists && statusValid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background build: idx_logs_api_key_time exists=%v valid=%v, idx_logs_api_key exists=%v, idx_logs_status exists=%v valid=%v; logs = %q",
				newExists, newValid, oldExists, statusExists, statusValid, logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	var unique bool
	if err := db.QueryRow(`SELECT indisunique FROM pg_index WHERE indexrelid = 'idx_logs_status'::regclass`).Scan(&unique); err != nil {
		t.Fatal(err)
	}
	if unique {
		t.Fatal("idx_logs_status is still the failed unique index, want it rebuilt from the schema")
	}
}

// TestPostgresRebuildsInvalidIndexOnSmallTable checks that an invalid index
// on a small table is dropped and created again inside the migration.
func TestPostgresRebuildsInvalidIndexOnSmallTable(t *testing.T) {
	dsn := newPostgresTestDSN(t)
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	defer st.Close()
	for _, id := range []string{"a", "b"} {
		if err := st.InsertLog(context.Background(), RequestLog{ID: id, Timestamp: time.Now(), Provider: "openai", Model: "gpt-4o", Status: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	db := openPostgresDB(t, dsn)
	if _, err := db.Exec(`DROP INDEX idx_logs_status`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX CONCURRENTLY idx_logs_status ON request_logs(status)`); err == nil {
		t.Fatal("unique index over duplicate values succeeded, want a failed build")
	}
	if err := st.migrate(); err != nil {
		t.Fatalf("migrate() error = %v", err)
	}
	if exists, valid := indexState(t, db, "idx_logs_status"); !exists || !valid {
		t.Fatalf("idx_logs_status exists=%v valid=%v after migrate, want a valid index", exists, valid)
	}
}
