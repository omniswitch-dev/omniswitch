package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// schemaIndex is one index the store maintains. It is created after every
// table and column exists, so it may reference a column that ensureColumn
// adds to an older database. replaces names an older index that this one
// supersedes; the older index is dropped once this one exists.
type schemaIndex struct {
	name     string
	table    string
	columns  string // column list in parentheses, plus an optional WHERE clause
	unique   bool
	replaces string
}

func (ix schemaIndex) createSQL(concurrently bool) string {
	var b strings.Builder
	b.WriteString("CREATE ")
	if ix.unique {
		b.WriteString("UNIQUE ")
	}
	b.WriteString("INDEX ")
	if concurrently {
		b.WriteString("CONCURRENTLY ")
	}
	b.WriteString("IF NOT EXISTS " + ix.name + " ON " + ix.table + ix.columns)
	return b.String()
}

var schemaIndexes = []schemaIndex{
	{name: "idx_logs_timestamp", table: "request_logs", columns: "(timestamp)"},
	{name: "idx_logs_trace", table: "request_logs", columns: "(trace_id)"},
	{name: "idx_logs_session", table: "request_logs", columns: "(session_id)"},
	// The composite indexes cover the same prefix as the single-column
	// indexes they replace and also serve the ORDER BY / range part of the
	// hot queries.
	{name: "idx_logs_api_key_time", table: "request_logs", columns: "(api_key_id, timestamp)", replaces: "idx_logs_api_key"},
	{name: "idx_logs_provider", table: "request_logs", columns: "(provider)"},
	{name: "idx_logs_status", table: "request_logs", columns: "(status)"},
	{name: "idx_guardrail_request", table: "guardrail_events", columns: "(request_id)"},
	{name: "idx_cache_model_created", table: "semantic_cache", columns: "(model, created_at)", replaces: "idx_cache_model"},
	{name: "idx_cache_expires", table: "semantic_cache", columns: "(expires_at)"},
	{name: "idx_guardrail_timestamp", table: "guardrail_events", columns: "(timestamp)"},
	{name: "idx_shadow_timestamp", table: "shadow_requests", columns: "(timestamp)"},
	{name: "idx_shadow_request", table: "shadow_requests", columns: "(request_id)"},
	{name: "idx_feedback_request", table: "feedback", columns: "(request_id)"},
	{name: "idx_feedback_trace", table: "feedback", columns: "(trace_id)"},
	// Tenant-scoped dashboard reads filter on the caller's workspace or
	// organization and order or range on timestamp.
	{name: "idx_logs_workspace_time", table: "request_logs", columns: "(workspace_id, timestamp)"},
	{name: "idx_logs_org_time", table: "request_logs", columns: "(organization_id, timestamp)"},
	{name: "idx_feedback_workspace_time", table: "feedback", columns: "(workspace_id, timestamp)"},
	{name: "idx_feedback_org_time", table: "feedback", columns: "(organization_id, timestamp)"},
	{name: "idx_workspaces_org", table: "workspaces", columns: "(organization_id)"},
	{name: "idx_workspace_members_user", table: "workspace_members", columns: "(user_id)"},
	{name: "idx_virtual_keys_name", table: "virtual_keys", columns: "(name)"},
	{name: "idx_gateway_configs_name", table: "gateway_configs", columns: "(name)"},
	{name: "idx_datasets_created", table: "datasets", columns: "(created_at)"},
	{name: "idx_experiments_status_created", table: "experiments", columns: "(status, created_at)"},
	{name: "idx_experiments_created", table: "experiments", columns: "(created_at)"},
	// SaveCacheEntry upserts with ON CONFLICT on this index, so it is
	// never deferred.
	{name: "idx_cache_key", table: "semantic_cache", columns: "(cache_key) WHERE cache_key IS NOT NULL AND cache_key != ''", unique: true},
}

// pgOnlineRewriteRows is the largest table, in rows, that a PostgreSQL
// startup migration rewrites or indexes inside its transaction. A larger
// table would hold an exclusive lock for too long: about 9 seconds per
// 100,000 request_logs rows. Larger tables get their indexes built
// concurrently in the background, and REAL columns are left for the
// operator to convert (see pgWidenRealColumns). A variable so tests can
// lower it.
var pgOnlineRewriteRows = 50000

// pgMigrationLockTimeout bounds how long a PostgreSQL startup migration
// waits for a table lock. A DDL statement that waits for a lock also makes
// every later query on that table wait behind it, so a blocked migration
// must fail fast (the gateway exits and is restarted) instead of stalling
// traffic on every replica. A variable so tests can shorten it.
var pgMigrationLockTimeout = "10s"

// indexBuildLockID is the PostgreSQL session advisory lock that lets one
// replica at a time build deferred indexes.
const indexBuildLockID int64 = migrationLockID + 1

// pgMigrationRetryDelays are the waits before each new attempt of a
// PostgreSQL migration that timed out waiting for a table lock, for example
// behind a long-running report query. A variable so tests can shorten it.
var pgMigrationRetryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// migrateWithRetry runs migrate and retries it while it fails on a lock
// timeout. Each attempt rolls back completely, so a retry starts clean.
func (s *Store) migrateWithRetry() error {
	err := s.migrate()
	for _, delay := range pgMigrationRetryDelays {
		if !isLockTimeout(err) {
			return err
		}
		log.Printf("store: migration timed out waiting for a table lock, retrying in %s: %v", delay, err)
		time.Sleep(delay)
		err = s.migrate()
	}
	return err
}

// isLockTimeout reports whether err is PostgreSQL's lock_not_available
// error, which lock_timeout raises.
func isLockTimeout(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// migrate creates or upgrades the schema inside one transaction. On
// PostgreSQL the transaction holds an advisory lock, so replicas that start
// at the same time run their migrations one after another instead of racing
// on CREATE TABLE IF NOT EXISTS, and it changes the schema only where
// something is missing, so a normal restart takes no table locks. On SQLite
// the write transaction (BEGIN IMMEDIATE) gives the same guarantee across
// processes.
func (s *Store) migrate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration failed: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if s.pg {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
			return fmt.Errorf("migration failed: advisory lock: %w", err)
		}
		// Set after the advisory lock: waiting for another replica's
		// migration is expected, waiting for a table lock is not.
		if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '"+pgMigrationLockTimeout+"'"); err != nil {
			return fmt.Errorf("migration failed: set lock_timeout: %w", err)
		}
	}
	for _, m := range schemaTables {
		if _, err := tx.ExecContext(ctx, s.dialectSQL(m)); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	for _, c := range legacyColumns {
		if err := s.ensureColumn(ctx, tx, c.table, c.column, s.dialectSQL(c.definition)); err != nil {
			return err
		}
	}
	var deferred []schemaIndex
	if s.pg {
		if err := s.pgWidenRealColumns(ctx, tx); err != nil {
			return err
		}
		if deferred, err = s.pgEnsureIndexes(ctx, tx); err != nil {
			return err
		}
	} else {
		for _, ix := range schemaIndexes {
			if _, err := tx.ExecContext(ctx, ix.createSQL(false)); err != nil {
				return fmt.Errorf("migration failed: %w", err)
			}
			if ix.replaces != "" {
				if _, err := tx.ExecContext(ctx, "DROP INDEX IF EXISTS "+ix.replaces); err != nil {
					return fmt.Errorf("migration failed: %w", err)
				}
			}
		}
	}
	if err := s.optionalIndex(ctx, tx, schemaIndex{name: "idx_prompts_name_version", table: "prompts", columns: "(name, version)", unique: true},
		"find the duplicate rows with: SELECT name, version, COUNT(*) FROM prompts GROUP BY name, version HAVING COUNT(*) > 1; "+
			"delete or renumber them and the index is created on the next start"); err != nil {
		return err
	}
	if err := s.applyDataMigrations(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration failed: commit: %w", err)
	}
	if len(deferred) > 0 {
		s.runInBackground(func(ctx context.Context) { s.buildDeferredIndexes(ctx, deferred) })
	}
	return nil
}

// optionalIndex creates an index that existing data may prevent, such as a
// unique index over rows that already hold duplicates. A failure is logged
// with hint and the migration continues: startup must not fail on old data.
// The savepoint keeps a PostgreSQL error from aborting the whole
// transaction.
func (s *Store) optionalIndex(ctx context.Context, tx *sql.Tx, ix schemaIndex, hint string) error {
	if s.pg {
		exists, _, err := pgIndexState(ctx, tx, ix.name)
		if err != nil {
			return fmt.Errorf("migration failed: check index %s: %w", ix.name, err)
		}
		if exists {
			return nil
		}
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT optional_index"); err != nil {
		return fmt.Errorf("migration failed: savepoint: %w", err)
	}
	if _, err := tx.ExecContext(ctx, ix.createSQL(false)); err != nil {
		log.Printf("store: warning: could not create index %s, continuing without it: %v; %s", ix.name, err, hint)
		if _, rbErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT optional_index"); rbErr != nil {
			return fmt.Errorf("migration failed: rollback to savepoint: %w", rbErr)
		}
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT optional_index"); err != nil {
		return fmt.Errorf("migration failed: release savepoint: %w", err)
	}
	return nil
}

// pgEnsureIndexes creates missing indexes inside the migration transaction
// and returns the ones whose table is too large to index there. It changes
// nothing when every index is in place, so a normal restart takes no lock
// on any table: CREATE INDEX IF NOT EXISTS would take a SHARE lock, and
// DROP INDEX IF EXISTS an ACCESS EXCLUSIVE lock, even when there is nothing
// to do.
func (s *Store) pgEnsureIndexes(ctx context.Context, tx *sql.Tx) ([]schemaIndex, error) {
	var deferred []schemaIndex
	large := map[string]bool{}
	for _, ix := range schemaIndexes {
		exists, valid, err := pgIndexState(ctx, tx, ix.name)
		if err != nil {
			return nil, fmt.Errorf("migration failed: check index %s: %w", ix.name, err)
		}
		replacedExists := false
		if ix.replaces != "" {
			if replacedExists, _, err = pgIndexState(ctx, tx, ix.replaces); err != nil {
				return nil, fmt.Errorf("migration failed: check index %s: %w", ix.replaces, err)
			}
		}
		if exists && valid && !replacedExists {
			continue
		}
		isLarge, seen := large[ix.table]
		if !seen {
			if isLarge, err = pgTableIsLarge(ctx, tx, ix.table); err != nil {
				return nil, fmt.Errorf("migration failed: count %s: %w", ix.table, err)
			}
			large[ix.table] = isLarge
		}
		if isLarge && !ix.unique {
			deferred = append(deferred, ix)
			continue
		}
		var stmts []string
		if exists && !valid {
			stmts = append(stmts, "DROP INDEX IF EXISTS "+ix.name)
		}
		if !exists || !valid {
			stmts = append(stmts, ix.createSQL(false))
		}
		if replacedExists {
			stmts = append(stmts, "DROP INDEX IF EXISTS "+ix.replaces)
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return nil, fmt.Errorf("migration failed: %w", err)
			}
		}
	}
	return deferred, nil
}

// buildDeferredIndexes builds indexes on large tables with CREATE INDEX
// CONCURRENTLY, which does not block reads or writes. It runs in the
// background after the store opens. A session advisory lock lets one
// replica build at a time; the others skip. A failed or cancelled build is
// logged and retried on the next start.
func (s *Store) buildDeferredIndexes(ctx context.Context, indexes []schemaIndex) {
	conn, err := s.wdb.Conn(ctx)
	if err != nil {
		log.Printf("store: warning: deferred index build: %v", err)
		return
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", indexBuildLockID).Scan(&locked); err != nil {
		log.Printf("store: warning: deferred index build: %v", err)
		return
	}
	if !locked {
		return
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", indexBuildLockID)
	}()
	for _, ix := range indexes {
		started := time.Now()
		log.Printf("store: building index %s on %s in the background", ix.name, ix.table)
		if err := buildIndexConcurrently(ctx, conn, ix); err != nil {
			log.Printf("store: warning: could not build index %s on %s: %v; it is retried on the next start", ix.name, ix.table, err)
			continue
		}
		log.Printf("store: built index %s on %s in %s", ix.name, ix.table, time.Since(started).Round(time.Millisecond))
	}
}

func buildIndexConcurrently(ctx context.Context, conn *sql.Conn, ix schemaIndex) error {
	exists, valid, err := pgIndexState(ctx, conn, ix.name)
	if err != nil {
		return err
	}
	// A cancelled or failed concurrent build leaves an invalid index that
	// IF NOT EXISTS would skip, so drop it first.
	if exists && !valid {
		if _, err := conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+ix.name); err != nil {
			return fmt.Errorf("drop invalid index: %w", err)
		}
	}
	if !exists || !valid {
		if _, err := conn.ExecContext(ctx, ix.createSQL(true)); err != nil {
			return err
		}
	}
	if ix.replaces != "" {
		if _, err := conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+ix.replaces); err != nil {
			return fmt.Errorf("drop replaced index %s: %w", ix.replaces, err)
		}
	}
	return nil
}

// createTablePattern finds the table names in schemaTables.
var createTablePattern = regexp.MustCompile(`CREATE TABLE IF NOT EXISTS (\w+)`)

// storeTables lists the tables the store owns. A database or schema can be
// shared with other applications, so schema changes that scan the catalog
// must stay inside this list.
func storeTables() []string {
	tables := make([]string, 0, len(schemaTables))
	for _, stmt := range schemaTables {
		if match := createTablePattern.FindStringSubmatch(stmt); match != nil {
			tables = append(tables, match[1])
		}
	}
	return tables
}

// pgWidenRealColumns converts float4 (REAL) columns that older releases
// created in the store's own tables to DOUBLE PRECISION. The value goes
// through its text form so a stored 0.01 becomes 0.01, not
// 0.009999999776482582. The conversion rewrites the table under an
// exclusive lock, so a table with more than pgOnlineRewriteRows rows is
// left as it is (REAL still works, with float4 precision) and a warning
// gives the statement to run in a maintenance window.
func (s *Store) pgWidenRealColumns(ctx context.Context, tx *sql.Tx) error {
	tables := storeTables()
	quoted := make([]string, len(tables))
	for i, table := range tables {
		quoted[i] = "'" + table + "'"
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND data_type = 'real'
		AND table_name IN (`+strings.Join(quoted, ", ")+`)
		ORDER BY table_name, column_name`)
	if err != nil {
		return fmt.Errorf("migration failed: list real columns: %w", err)
	}
	type col struct{ table, column string }
	var cols []col
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.table, &c.column); err != nil {
			rows.Close()
			return fmt.Errorf("migration failed: list real columns: %w", err)
		}
		cols = append(cols, c)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	large := map[string]bool{}
	for _, c := range cols {
		isLarge, seen := large[c.table]
		if !seen {
			if isLarge, err = pgTableIsLarge(ctx, tx, c.table); err != nil {
				return fmt.Errorf("migration failed: count %s: %w", c.table, err)
			}
			large[c.table] = isLarge
		}
		table, column := pgQuoteIdent(c.table), pgQuoteIdent(c.column)
		stmt := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE DOUBLE PRECISION USING CAST(CAST(%s AS TEXT) AS DOUBLE PRECISION)", table, column, column)
		if isLarge {
			log.Printf("store: warning: %s.%s is a 4-byte REAL column and %s has more than %d rows, so it is not converted at startup "+
				"(the rewrite locks the table); run this in a maintenance window: %s", c.table, c.column, c.table, pgOnlineRewriteRows, stmt)
			continue
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration failed: widen %s.%s: %w", c.table, c.column, err)
		}
	}
	return nil
}

// pgTableIsLarge reports whether table holds more than pgOnlineRewriteRows
// rows. It reads at most pgOnlineRewriteRows+1 rows and runs only when a
// table needs a one-time change.
func pgTableIsLarge(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var n int64
	query := fmt.Sprintf("SELECT COUNT(*) FROM (SELECT 1 FROM %s LIMIT %d) AS bounded", pgQuoteIdent(table), pgOnlineRewriteRows+1)
	if err := tx.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return false, err
	}
	return n > int64(pgOnlineRewriteRows), nil
}

// queryRower is the query method that *sql.Tx and *sql.Conn share.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// pgIndexState reports whether an index named name exists in the current
// schema and whether it is valid. A failed or cancelled CREATE INDEX
// CONCURRENTLY leaves an invalid index behind, which PostgreSQL maintains
// but never uses. The catalog lookup takes no lock on the table.
func pgIndexState(ctx context.Context, q queryRower, name string) (exists, valid bool, err error) {
	var state sql.NullBool
	err = q.QueryRowContext(ctx, `SELECT (SELECT i.indisvalid FROM pg_index i
		WHERE i.indexrelid = to_regclass(quote_ident(current_schema()) || '.' || quote_ident($1)))`, name).Scan(&state)
	return state.Valid, state.Bool, err
}

func pgQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// ensureColumn adds a column that older databases lack. It checks the
// catalog first: on PostgreSQL even ALTER TABLE ... ADD COLUMN IF NOT EXISTS
// takes an ACCESS EXCLUSIVE lock, which would block every query on the
// table until the migration commits.
func (s *Store) ensureColumn(ctx context.Context, tx *sql.Tx, table, column, definition string) error {
	var exists bool
	var err error
	if s.pg {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2)`, table, column).Scan(&exists)
	} else {
		exists, err = sqliteColumnExists(ctx, tx, table, column)
	}
	if err != nil {
		return fmt.Errorf("check column %s.%s: %w", table, column, err)
	}
	if exists {
		return nil
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

func sqliteColumnExists(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// runInBackground runs fn on a goroutine that Close cancels and waits for.
func (s *Store) runInBackground(fn func(context.Context)) {
	s.bgMu.Lock()
	if s.bgCancel == nil {
		s.bgCtx, s.bgCancel = context.WithCancel(context.Background())
	}
	ctx := s.bgCtx
	s.bgWG.Add(1)
	s.bgMu.Unlock()
	go func() {
		defer s.bgWG.Done()
		fn(ctx)
	}()
}

// stopBackground cancels background work and waits for it to return.
func (s *Store) stopBackground() {
	s.bgMu.Lock()
	cancel := s.bgCancel
	s.bgMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.bgWG.Wait()
}
