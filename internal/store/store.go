package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/cache"
	"github.com/omniswitch-dev/omniswitch/internal/model"
	"github.com/omniswitch-dev/omniswitch/internal/provider"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Store provides persistent storage for the gateway.
type Store struct {
	// db serves reads. On PostgreSQL it also serves writes.
	db *sql.DB
	// wdb serves writes. On SQLite it is a separate pool limited to one
	// connection so writers queue in Go instead of failing with SQLITE_BUSY.
	// On PostgreSQL it is the same pool as db.
	wdb *sql.DB
	pg  bool // PostgreSQL dialect when true; SQLite otherwise
}

// RequestLog represents a logged gateway request.
type RequestLog struct {
	ID             string    `json:"id"`
	Timestamp      time.Time `json:"timestamp"`
	TraceID        string    `json:"trace_id,omitempty"`
	SessionID      string    `json:"session_id,omitempty"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	APIKeyID       string    `json:"api_key_id"`
	Status         string    `json:"status"`
	InputTokens    int       `json:"input_tokens"`
	OutputTokens   int       `json:"output_tokens"`
	TotalTokens    int       `json:"total_tokens"`
	LatencyMs      float64   `json:"latency_ms"`
	Cost           float64   `json:"cost"`
	RequestBody    string    `json:"request_body,omitempty"`
	ResponseBody   string    `json:"response_body,omitempty"`
	Decision       string    `json:"decision"`
	DecisionReason string    `json:"decision_reason,omitempty"`
	ErrorMessage   string    `json:"error,omitempty"`
	Cached         bool      `json:"cached"`
	Metadata       string    `json:"metadata,omitempty"`
}

// TraceSpan captures a single stage of request processing for waterfall views.
type TraceSpan struct {
	Name       string         `json:"name"`
	Provider   string         `json:"provider,omitempty"`
	Start      time.Time      `json:"start"`
	DurationMs float64        `json:"duration_ms"`
	Status     string         `json:"status"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// APIKey represents a stored API key.
type APIKey struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	KeyHash            string            `json:"-"`
	KeyPrefix          string            `json:"key_prefix"`
	WorkspaceID        string            `json:"workspace_id,omitempty"`
	Role               string            `json:"role,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	ExpiresAt          *time.Time        `json:"expires_at,omitempty"`
	RateLimit          int               `json:"rate_limit"`
	BudgetUSD          float64           `json:"budget_usd,omitempty"`
	SpendUSD           float64           `json:"spend_usd,omitempty"`
	MonthlyCostBudget  float64           `json:"monthly_cost_budget,omitempty"`
	MonthlyTokenBudget int               `json:"monthly_token_budget,omitempty"`
	Enabled            bool              `json:"enabled"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

// Prompt represents a stored prompt template.
type Prompt struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Version   int               `json:"version"`
	Template  string            `json:"template"`
	Variables []string          `json:"variables,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// GuardrailEvent logs a guardrail trigger.
type GuardrailEvent struct {
	ID        string    `json:"id"`
	RequestID string    `json:"request_id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Action    string    `json:"action"`
	Matched   bool      `json:"matched"`
	Details   string    `json:"details"`
}

// Metrics holds aggregated metrics.
type Metrics struct {
	TotalRequests  int     `json:"total_requests"`
	AllowedCount   int     `json:"allowed_count"`
	DeniedCount    int     `json:"denied_count"`
	ErrorCount     int     `json:"error_count"`
	AvgLatencyMs   float64 `json:"avg_latency_ms"`
	TotalCost      float64 `json:"total_cost"`
	TotalTokens    int     `json:"total_tokens"`
	CacheHits      int     `json:"cache_hits"`
	RequestsPerMin float64 `json:"requests_per_min"`
}

// ProviderMetrics holds per-provider aggregated metrics.
type ProviderMetrics struct {
	Provider     string  `json:"provider"`
	RequestCount int     `json:"request_count"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	TotalCost    float64 `json:"total_cost"`
	ErrorRate    float64 `json:"error_rate"`
}

type BudgetUsage struct {
	Cost   float64 `json:"cost"`
	Tokens int     `json:"tokens"`
}

type CacheEntry struct {
	ID         string                `json:"id"`
	Key        string                `json:"key,omitempty"`
	CreatedAt  time.Time             `json:"created_at"`
	ExpiresAt  *time.Time            `json:"expires_at,omitempty"`
	Model      string                `json:"model"`
	Prompt     string                `json:"prompt"`
	Vector     map[string]float64    `json:"vector"`
	Response   provider.ChatResponse `json:"response"`
	Similarity float64               `json:"similarity,omitempty"`
}

type ShadowLog struct {
	ID              string    `json:"id"`
	RequestID       string    `json:"request_id"`
	TraceID         string    `json:"trace_id,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
	PrimaryProvider string    `json:"primary_provider"`
	ShadowProvider  string    `json:"shadow_provider"`
	Model           string    `json:"model"`
	LatencyMs       float64   `json:"latency_ms"`
	Cost            float64   `json:"cost"`
	Status          string    `json:"status"`
	ErrorMessage    string    `json:"error,omitempty"`
}

type Feedback struct {
	ID        string            `json:"id"`
	RequestID string            `json:"request_id,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Score     int               `json:"score"`
	Rating    string            `json:"rating,omitempty"`
	Comment   string            `json:"comment,omitempty"`
	UserID    string            `json:"user_id,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type Organization struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	CreatedAt time.Time         `json:"created_at"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type Workspace struct {
	ID             string            `json:"id"`
	OrganizationID string            `json:"organization_id"`
	Name           string            `json:"name"`
	CreatedAt      time.Time         `json:"created_at"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type User struct {
	ID        string            `json:"id"`
	Email     string            `json:"email"`
	Name      string            `json:"name,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type WorkspaceMember struct {
	WorkspaceID string    `json:"workspace_id"`
	UserID      string    `json:"user_id"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

type GatewayConfigRecord struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Format      string    `json:"format"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Enabled     bool      `json:"enabled"`
}

type CostAnalyticsRow struct {
	Group        string  `json:"group"`
	RequestCount int     `json:"request_count"`
	TotalCost    float64 `json:"total_cost"`
	TotalTokens  int     `json:"total_tokens"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

type TraceDetail struct {
	TraceID         string           `json:"trace_id"`
	Logs            []RequestLog     `json:"logs"`
	GuardrailEvents []GuardrailEvent `json:"guardrail_events"`
	ShadowLogs      []ShadowLog      `json:"shadow_logs"`
	Feedback        []Feedback       `json:"feedback,omitempty"`
}

// VirtualKeyRecord stores an encrypted provider API key mapping.
type VirtualKeyRecord struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	ProviderType string    `json:"provider_type"`
	ProviderName string    `json:"provider_name"`
	BaseURL      string    `json:"base_url,omitempty"`
	EncryptedKey string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Enabled      bool      `json:"enabled"`
	MetadataJSON string    `json:"-"`
}

// sqliteBusyTimeoutMs is how long a SQLite connection waits for a lock held
// by another connection before it gives up with SQLITE_BUSY.
const sqliteBusyTimeoutMs = 10000

// sqliteDSN builds a modernc.org/sqlite DSN for path. The _pragma parameters
// run on every new pooled connection (a plain "PRAGMA ..." statement would
// only configure whichever connection happened to execute it), and
// _txlock=immediate makes write transactions take the write lock at BEGIN
// so they never fail half-way when upgrading from a read lock.
func sqliteDSN(path string) string {
	params := []string{
		"_pragma=busy_timeout(" + strconv.Itoa(sqliteBusyTimeoutMs) + ")",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_txlock=immediate",
	}
	return path + "?" + strings.Join(params, "&")
}

// New creates a new Store backed by a SQLite database at the given directory.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	dsn := sqliteDSN(filepath.Join(dir, "sentinel.db"))
	wdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// SQLite allows one writer at a time. A single-connection write pool
	// queues writers in database/sql instead of letting them collide.
	wdb.SetMaxOpenConns(1)
	wdb.SetMaxIdleConns(1)
	wdb.SetConnMaxLifetime(0)
	if err := wdb.Ping(); err != nil {
		wdb.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		wdb.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)

	store := &Store{db: db, wdb: wdb}
	if err := store.migrate(); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

// OpenPostgres creates a Store backed by a PostgreSQL database. It enables
// horizontally scaled gateway replicas to share keys, logs, budgets, and
// configuration. The DSN is a standard postgres:// connection string.
func OpenPostgres(dsn string) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("postgres DSN is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(25)
	store := &Store{db: db, wdb: db, pg: true}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return store, nil
}

// Ping verifies the underlying database connection is reachable. It is used
// by the gateway's readiness probe and should be called with a short,
// caller-supplied timeout so a stalled database cannot hang the check.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("store is not initialized")
	}
	return s.db.PingContext(ctx)
}

// rewriteSQL converts SQLite-style ? placeholders into PostgreSQL $n
// placeholders. Placeholders inside single-quoted strings are preserved.
func rewriteSQL(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	inString := false
	n := 0
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if ch == '\'' {
			inString = !inString
			b.WriteByte(ch)
			continue
		}
		if ch == '?' && !inString {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if s.pg {
		query = rewriteSQL(query)
	}
	return s.wdb.ExecContext(ctx, query, args...)
}

func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if s.pg {
		query = rewriteSQL(query)
	}
	return s.db.QueryContext(ctx, query, args...)
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	if s.pg {
		query = rewriteSQL(query)
	}
	return s.db.QueryRowContext(ctx, query, args...)
}

// writeQueryRow runs a statement that writes and returns a row (for example
// INSERT ... RETURNING) on the write pool.
func (s *Store) writeQueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	if s.pg {
		query = rewriteSQL(query)
	}
	return s.wdb.QueryRowContext(ctx, query, args...)
}

// migrationLockID is the PostgreSQL advisory lock key that serializes schema
// migrations across gateway replicas sharing one database.
const migrationLockID int64 = 0x6f6d6e6973770001

// schemaTables creates every table. {{FLOAT}} is replaced with the dialect's
// 8-byte floating point type: REAL is a double in SQLite but a 4-byte float4
// in PostgreSQL, which loses precision on money and latency columns.
var schemaTables = []string{
	`CREATE TABLE IF NOT EXISTS request_logs (
			id TEXT PRIMARY KEY,
			timestamp TEXT NOT NULL,
			trace_id TEXT,
			session_id TEXT,
			provider TEXT,
			model TEXT,
			api_key_id TEXT,
			status TEXT,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0,
			latency_ms {{FLOAT}} DEFAULT 0,
			cost {{FLOAT}} DEFAULT 0,
			request_body TEXT,
			response_body TEXT,
			decision TEXT,
			decision_reason TEXT,
			error_message TEXT,
			cached INTEGER DEFAULT 0
		)`,
	`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			key_hash TEXT UNIQUE NOT NULL,
			key_prefix TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT,
			rate_limit INTEGER DEFAULT 60,
			budget_usd {{FLOAT}} DEFAULT 0,
			spend_usd {{FLOAT}} DEFAULT 0,
			monthly_cost_budget {{FLOAT}} DEFAULT 0,
			monthly_token_budget INTEGER DEFAULT 0,
			enabled INTEGER DEFAULT 1,
			workspace_id TEXT,
			role TEXT,
			metadata TEXT
		)`,
	`CREATE TABLE IF NOT EXISTS prompts (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			version INTEGER DEFAULT 1,
			template TEXT NOT NULL,
			variables TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			metadata TEXT
		)`,
	`CREATE TABLE IF NOT EXISTS guardrail_events (
			id TEXT PRIMARY KEY,
			request_id TEXT,
			timestamp TEXT NOT NULL,
			guardrail_type TEXT,
			action TEXT,
			matched INTEGER DEFAULT 0,
			details TEXT
		)`,
	`CREATE TABLE IF NOT EXISTS semantic_cache (
			id TEXT PRIMARY KEY,
			cache_key TEXT,
			created_at TEXT NOT NULL,
			expires_at TEXT,
			model TEXT NOT NULL,
			prompt_text TEXT NOT NULL,
			vector_json TEXT NOT NULL,
			response_json TEXT NOT NULL
		)`,
	`CREATE TABLE IF NOT EXISTS shadow_requests (
			id TEXT PRIMARY KEY,
			request_id TEXT NOT NULL,
			trace_id TEXT,
			timestamp TEXT NOT NULL,
			primary_provider TEXT,
			shadow_provider TEXT,
			model TEXT,
			latency_ms {{FLOAT}} DEFAULT 0,
			cost {{FLOAT}} DEFAULT 0,
			status TEXT,
			error_message TEXT
		)`,
	`CREATE TABLE IF NOT EXISTS feedback (
			id TEXT PRIMARY KEY,
			request_id TEXT,
			trace_id TEXT,
			timestamp TEXT NOT NULL,
			score INTEGER DEFAULT 0,
			rating TEXT,
			comment TEXT,
			user_id TEXT,
			metadata TEXT
		)`,
	`CREATE TABLE IF NOT EXISTS organizations (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL,
			metadata TEXT
		)`,
	`CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT PRIMARY KEY,
			organization_id TEXT NOT NULL,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL,
			metadata TEXT
		)`,
	`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			email TEXT UNIQUE NOT NULL,
			name TEXT,
			created_at TEXT NOT NULL,
			metadata TEXT
		)`,
	`CREATE TABLE IF NOT EXISTS workspace_members (
			workspace_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			role TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (workspace_id, user_id)
		)`,
	`CREATE TABLE IF NOT EXISTS virtual_keys (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			provider_type TEXT NOT NULL,
			provider_name TEXT,
			base_url TEXT,
			encrypted_key TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			enabled INTEGER DEFAULT 1,
			metadata TEXT
		)`,
	`CREATE TABLE IF NOT EXISTS gateway_configs (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			description TEXT,
			format TEXT NOT NULL DEFAULT 'yaml',
			body TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			enabled INTEGER DEFAULT 1
		)`,
	`CREATE TABLE IF NOT EXISTS datasets (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT,
			version INTEGER DEFAULT 1,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			metadata TEXT,
			examples TEXT
		)`,
	`CREATE TABLE IF NOT EXISTS experiments (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT,
			dataset_id TEXT,
			config TEXT,
			status TEXT,
			results TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			created_by TEXT
		)`,
}

// schemaIndexes runs after every table and column exists, so an index may
// reference a column that ensureColumn adds to an older database.
var schemaIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_logs_timestamp ON request_logs(timestamp)`,
	`CREATE INDEX IF NOT EXISTS idx_logs_trace ON request_logs(trace_id)`,
	`CREATE INDEX IF NOT EXISTS idx_logs_session ON request_logs(session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_logs_api_key_time ON request_logs(api_key_id, timestamp)`,
	`CREATE INDEX IF NOT EXISTS idx_logs_provider ON request_logs(provider)`,
	`CREATE INDEX IF NOT EXISTS idx_logs_status ON request_logs(status)`,
	`CREATE INDEX IF NOT EXISTS idx_guardrail_request ON guardrail_events(request_id)`,
	`CREATE INDEX IF NOT EXISTS idx_cache_model_created ON semantic_cache(model, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_cache_expires ON semantic_cache(expires_at)`,
	`CREATE INDEX IF NOT EXISTS idx_guardrail_timestamp ON guardrail_events(timestamp)`,
	`CREATE INDEX IF NOT EXISTS idx_shadow_timestamp ON shadow_requests(timestamp)`,
	`CREATE INDEX IF NOT EXISTS idx_shadow_request ON shadow_requests(request_id)`,
	`CREATE INDEX IF NOT EXISTS idx_feedback_request ON feedback(request_id)`,
	`CREATE INDEX IF NOT EXISTS idx_feedback_trace ON feedback(trace_id)`,
	`CREATE INDEX IF NOT EXISTS idx_workspaces_org ON workspaces(organization_id)`,
	`CREATE INDEX IF NOT EXISTS idx_workspace_members_user ON workspace_members(user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_virtual_keys_name ON virtual_keys(name)`,
	`CREATE INDEX IF NOT EXISTS idx_gateway_configs_name ON gateway_configs(name)`,
	`CREATE INDEX IF NOT EXISTS idx_datasets_created ON datasets(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_experiments_status_created ON experiments(status, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_experiments_created ON experiments(created_at)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_cache_key ON semantic_cache(cache_key) WHERE cache_key IS NOT NULL AND cache_key != ''`,
	// Superseded by the composite indexes above, which cover the same
	// prefix and also serve the ORDER BY / range part of the hot queries.
	`DROP INDEX IF EXISTS idx_logs_api_key`,
	`DROP INDEX IF EXISTS idx_cache_model`,
}

// legacyColumns are columns added after the first release. They are added to
// older databases before any index that references them is created.
var legacyColumns = []struct{ table, column, definition string }{
	{"request_logs", "trace_id", "TEXT"},
	{"request_logs", "session_id", "TEXT"},
	{"request_logs", "metadata", "TEXT"},
	{"api_keys", "monthly_cost_budget", "{{FLOAT}} DEFAULT 0"},
	{"api_keys", "monthly_token_budget", "INTEGER DEFAULT 0"},
	{"api_keys", "budget_usd", "{{FLOAT}} DEFAULT 0"},
	{"api_keys", "spend_usd", "{{FLOAT}} DEFAULT 0"},
	{"api_keys", "workspace_id", "TEXT"},
	{"api_keys", "role", "TEXT"},
	{"semantic_cache", "cache_key", "TEXT"},
	{"semantic_cache", "expires_at", "TEXT"},
}

func (s *Store) dialectSQL(stmt string) string {
	if s.pg {
		return strings.ReplaceAll(stmt, "{{FLOAT}}", "DOUBLE PRECISION")
	}
	return strings.ReplaceAll(stmt, "{{FLOAT}}", "REAL")
}

// migrate creates or upgrades the schema inside one transaction. On
// PostgreSQL the transaction holds an advisory lock, so replicas that start
// at the same time run their migrations one after another instead of racing
// on CREATE TABLE IF NOT EXISTS. On SQLite the write transaction (BEGIN
// IMMEDIATE) gives the same guarantee across processes.
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
	if s.pg {
		if err := s.pgWidenRealColumns(ctx, tx); err != nil {
			return err
		}
	}
	for _, m := range schemaIndexes {
		if _, err := tx.ExecContext(ctx, s.dialectSQL(m)); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	if err := optionalIndex(ctx, tx, "idx_prompts_name_version",
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_prompts_name_version ON prompts(name, version)`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration failed: commit: %w", err)
	}
	return nil
}

// optionalIndex creates an index that existing data may prevent, such as a
// unique index over rows that already hold duplicates. A failure is logged
// and the migration continues: startup must not fail on old data. The
// savepoint keeps a PostgreSQL error from aborting the whole transaction.
func optionalIndex(ctx context.Context, tx *sql.Tx, name, stmt string) error {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT optional_index"); err != nil {
		return fmt.Errorf("migration failed: savepoint: %w", err)
	}
	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		log.Printf("store: warning: could not create index %s, continuing without it: %v", name, err)
		if _, rbErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT optional_index"); rbErr != nil {
			return fmt.Errorf("migration failed: rollback to savepoint: %w", rbErr)
		}
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT optional_index"); err != nil {
		return fmt.Errorf("migration failed: release savepoint: %w", err)
	}
	return nil
}

// pgWidenRealColumns converts float4 (REAL) columns created by older
// releases to DOUBLE PRECISION. The value goes through its text form so a
// stored 0.01 becomes 0.01, not 0.009999999776482582.
func (s *Store) pgWidenRealColumns(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND data_type = 'real'`)
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
	for _, c := range cols {
		table, column := pgQuoteIdent(c.table), pgQuoteIdent(c.column)
		stmt := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE DOUBLE PRECISION USING CAST(CAST(%s AS TEXT) AS DOUBLE PRECISION)", table, column, column)
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration failed: widen %s.%s: %w", c.table, c.column, err)
		}
	}
	return nil
}

func pgQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// requestLogColumns selects a request_logs row in RequestLog scan order.
// Nullable columns are COALESCEd: rows written before a column existed, or
// by older releases, hold NULL and would otherwise fail to scan into string.
const requestLogColumns = `id, timestamp, COALESCE(trace_id, ''), COALESCE(session_id, ''), COALESCE(provider, ''),
	COALESCE(model, ''), COALESCE(api_key_id, ''), COALESCE(status, ''), COALESCE(input_tokens, 0),
	COALESCE(output_tokens, 0), COALESCE(total_tokens, 0), COALESCE(latency_ms, 0), COALESCE(cost, 0),
	COALESCE(request_body, ''), COALESCE(response_body, ''), COALESCE(decision, ''), COALESCE(decision_reason, ''),
	COALESCE(error_message, ''), COALESCE(cached, 0), COALESCE(metadata, '')`

// InsertLog stores a request log entry.
func (s *Store) InsertLog(ctx context.Context, log RequestLog) error {
	_, err := s.exec(ctx,
		`INSERT INTO request_logs (id, timestamp, trace_id, session_id, provider, model, api_key_id, status,
			input_tokens, output_tokens, total_tokens, latency_ms, cost,
			request_body, response_body, decision, decision_reason, error_message, cached, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.ID, formatTime(log.Timestamp), log.TraceID, log.SessionID, log.Provider, log.Model,
		log.APIKeyID, log.Status, log.InputTokens, log.OutputTokens, log.TotalTokens,
		log.LatencyMs, log.Cost, log.RequestBody, log.ResponseBody,
		log.Decision, log.DecisionReason, log.ErrorMessage, boolToInt(log.Cached), log.Metadata,
	)
	return err
}

// ListLogs returns paginated request logs ordered by timestamp descending.
func (s *Store) ListLogs(ctx context.Context, limit, offset int, provider, status string) ([]RequestLog, int, error) {
	return s.listLogs(ctx, limit, offset, provider, status, "")
}

// ListLogsForAPIKey scopes dashboard access to the authenticated workload key.
func (s *Store) ListLogsForAPIKey(ctx context.Context, apiKeyID string, limit, offset int, provider, status string) ([]RequestLog, int, error) {
	return s.listLogs(ctx, limit, offset, provider, status, apiKeyID)
}

// Page size bounds applied by every paginated store method.
const (
	DefaultPageSize = 50
	MaxPageSize     = 1000
)

// ClampPage normalises pagination input so every backend sees valid values:
// a non-positive limit becomes DefaultPageSize, a limit above MaxPageSize is
// capped, and a negative offset becomes 0 (PostgreSQL rejects a negative
// OFFSET, SQLite silently treats it as 0).
func ClampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func (s *Store) listLogs(ctx context.Context, limit, offset int, provider, status, apiKeyID string) ([]RequestLog, int, error) {
	limit, offset = ClampPage(limit, offset)
	where := "1=1"
	args := []any{}
	if apiKeyID != "" {
		where += " AND api_key_id = ?"
		args = append(args, apiKeyID)
	}
	if provider != "" {
		where += " AND provider = ?"
		args = append(args, provider)
	}
	if status != "" {
		where += " AND status = ?"
		args = append(args, status)
	}

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM request_logs WHERE %s", where)
	if err := s.queryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf(
		"SELECT "+requestLogColumns+" FROM request_logs WHERE %s ORDER BY timestamp DESC LIMIT ? OFFSET ?",
		where,
	)
	args = append(args, limit, offset)
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var logs []RequestLog
	for rows.Next() {
		var l RequestLog
		var ts string
		var cached int
		if err := rows.Scan(&l.ID, &ts, &l.TraceID, &l.SessionID, &l.Provider, &l.Model, &l.APIKeyID,
			&l.Status, &l.InputTokens, &l.OutputTokens, &l.TotalTokens,
			&l.LatencyMs, &l.Cost, &l.RequestBody, &l.ResponseBody, &l.Decision, &l.DecisionReason, &l.ErrorMessage, &cached, &l.Metadata); err != nil {
			return nil, 0, err
		}
		l.Timestamp = parseTime(ts)
		l.Cached = cached == 1
		logs = append(logs, l)
	}
	return logs, total, rows.Err()
}

func (s *Store) UpsertGatewayConfig(ctx context.Context, record GatewayConfigRecord) error {
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	if strings.TrimSpace(record.Format) == "" {
		record.Format = "yaml"
	}
	if strings.TrimSpace(record.ID) != "" {
		result, err := s.exec(ctx,
			`UPDATE gateway_configs
			SET name = ?, description = ?, format = ?, body = ?, updated_at = ?, enabled = ?
			WHERE id = ?`,
			record.Name,
			record.Description,
			record.Format,
			record.Body,
			formatTime(record.UpdatedAt),
			boolToInt(record.Enabled),
			record.ID,
		)
		if err != nil {
			return err
		}
		if affected, err := result.RowsAffected(); err == nil && affected > 0 {
			return nil
		}
	}
	_, err := s.exec(ctx,
		`INSERT INTO gateway_configs (id, name, description, format, body, created_at, updated_at, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			description = excluded.description,
			format = excluded.format,
			body = excluded.body,
			updated_at = excluded.updated_at,
			enabled = excluded.enabled`,
		record.ID,
		record.Name,
		record.Description,
		record.Format,
		record.Body,
		formatTime(record.CreatedAt),
		formatTime(record.UpdatedAt),
		boolToInt(record.Enabled),
	)
	return err
}

func (s *Store) SetGatewayConfigEnabled(ctx context.Context, ref string, enabled bool) error {
	result, err := s.exec(ctx,
		`UPDATE gateway_configs SET enabled = ?, updated_at = ? WHERE id = ? OR name = ?`,
		boolToInt(enabled),
		formatTime(time.Now().UTC()),
		ref,
		ref,
	)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetGatewayConfig(ctx context.Context, ref string) (GatewayConfigRecord, error) {
	var record GatewayConfigRecord
	var createdAt, updatedAt string
	var enabled int
	err := s.queryRow(ctx,
		`SELECT id, name, COALESCE(description, ''), format, body, created_at, updated_at, enabled
		FROM gateway_configs
		WHERE (id = ? OR name = ?) AND enabled = 1`,
		ref,
		ref,
	).Scan(&record.ID, &record.Name, &record.Description, &record.Format, &record.Body, &createdAt, &updatedAt, &enabled)
	if err != nil {
		return record, err
	}
	record.CreatedAt = parseTime(createdAt)
	record.UpdatedAt = parseTime(updatedAt)
	record.Enabled = enabled == 1
	return record, nil
}

func (s *Store) ListGatewayConfigs(ctx context.Context, includeDisabled bool) ([]GatewayConfigRecord, error) {
	where := "enabled = 1"
	if includeDisabled {
		where = "1=1"
	}
	rows, err := s.query(ctx,
		fmt.Sprintf(`SELECT id, name, COALESCE(description, ''), format, body, created_at, updated_at, enabled
		FROM gateway_configs WHERE %s ORDER BY updated_at DESC`, where),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []GatewayConfigRecord
	for rows.Next() {
		var record GatewayConfigRecord
		var createdAt, updatedAt string
		var enabled int
		if err := rows.Scan(&record.ID, &record.Name, &record.Description, &record.Format, &record.Body, &createdAt, &updatedAt, &enabled); err != nil {
			return nil, err
		}
		record.CreatedAt = parseTime(createdAt)
		record.UpdatedAt = parseTime(updatedAt)
		record.Enabled = enabled == 1
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) DeleteGatewayConfig(ctx context.Context, ref string) error {
	result, err := s.exec(ctx, `UPDATE gateway_configs SET enabled = 0, updated_at = ? WHERE id = ? OR name = ?`,
		formatTime(time.Now().UTC()),
		ref,
		ref,
	)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetCostAnalytics(ctx context.Context, since time.Time, groupBy, apiKeyID string) ([]CostAnalyticsRow, error) {
	expr, err := analyticsGroupExpression(groupBy)
	if err != nil {
		return nil, err
	}
	where := "timestamp >= ?"
	args := []any{formatTime(since)}
	if apiKeyID != "" {
		where += " AND api_key_id = ?"
		args = append(args, apiKeyID)
	}
	rows, err := s.query(ctx, fmt.Sprintf(`
		SELECT %s AS bucket,
			COUNT(*) AS request_count,
			COALESCE(SUM(cost), 0) AS total_cost,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(AVG(latency_ms), 0) AS avg_latency_ms
		FROM request_logs
		WHERE %s
		GROUP BY bucket
		ORDER BY total_cost DESC, bucket ASC`, expr, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rowsOut []CostAnalyticsRow
	for rows.Next() {
		var row CostAnalyticsRow
		if err := rows.Scan(&row.Group, &row.RequestCount, &row.TotalCost, &row.TotalTokens, &row.AvgLatencyMs); err != nil {
			return nil, err
		}
		rowsOut = append(rowsOut, row)
	}
	return rowsOut, rows.Err()
}

func (s *Store) GetTrace(ctx context.Context, traceID, apiKeyID string) (TraceDetail, error) {
	detail := TraceDetail{TraceID: traceID}
	where := "trace_id = ?"
	args := []any{traceID}
	if apiKeyID != "" {
		where += " AND api_key_id = ?"
		args = append(args, apiKeyID)
	}
	rows, err := s.query(ctx,
		fmt.Sprintf("SELECT "+requestLogColumns+" FROM request_logs WHERE %s ORDER BY timestamp ASC", where),
		args...,
	)
	if err != nil {
		return detail, err
	}
	defer rows.Close()
	requestIDs := []string{}
	for rows.Next() {
		var l RequestLog
		var ts string
		var cached int
		if err := rows.Scan(&l.ID, &ts, &l.TraceID, &l.SessionID, &l.Provider, &l.Model, &l.APIKeyID,
			&l.Status, &l.InputTokens, &l.OutputTokens, &l.TotalTokens, &l.LatencyMs, &l.Cost,
			&l.RequestBody, &l.ResponseBody, &l.Decision, &l.DecisionReason, &l.ErrorMessage, &cached, &l.Metadata); err != nil {
			return detail, err
		}
		l.Timestamp = parseTime(ts)
		l.Cached = cached == 1
		detail.Logs = append(detail.Logs, l)
		requestIDs = append(requestIDs, l.ID)
	}
	if err := rows.Err(); err != nil {
		return detail, err
	}
	if len(requestIDs) == 0 {
		return detail, sql.ErrNoRows
	}
	guardrailEvents, err := s.listGuardrailEventsForRequests(ctx, requestIDs)
	if err != nil {
		return detail, err
	}
	detail.GuardrailEvents = guardrailEvents
	shadowLogs, err := s.listShadowLogsForTrace(ctx, traceID)
	if err != nil {
		return detail, err
	}
	detail.ShadowLogs = shadowLogs
	feedback, err := s.ListFeedback(ctx, 100, "", traceID)
	if err != nil {
		return detail, err
	}
	detail.Feedback = feedback
	return detail, nil
}

func analyticsGroupExpression(groupBy string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(groupBy)) {
	case "", "day":
		return "substr(timestamp, 1, 10)", nil
	case "provider":
		return "COALESCE(NULLIF(provider, ''), 'unknown')", nil
	case "model":
		return "COALESCE(NULLIF(model, ''), 'unknown')", nil
	case "api_key_id", "api_key":
		return "COALESCE(NULLIF(api_key_id, ''), 'anonymous')", nil
	case "session_id", "session":
		return "COALESCE(NULLIF(session_id, ''), 'none')", nil
	case "status":
		return "COALESCE(NULLIF(status, ''), 'unknown')", nil
	default:
		return "", fmt.Errorf("unsupported groupBy %q", groupBy)
	}
}

// GetMetrics returns aggregated metrics for the given time window.
func (s *Store) GetMetrics(ctx context.Context, since time.Time) (Metrics, error) {
	return s.getMetrics(ctx, since, "")
}

// GetMetricsForAPIKey returns metrics only for one workload key.
func (s *Store) GetMetricsForAPIKey(ctx context.Context, since time.Time, apiKeyID string) (Metrics, error) {
	return s.getMetrics(ctx, since, apiKeyID)
}

func (s *Store) getMetrics(ctx context.Context, since time.Time, apiKeyID string) (Metrics, error) {
	var m Metrics
	sinceStr := formatTime(since)
	where := "timestamp >= ?"
	args := []any{sinceStr}
	if apiKeyID != "" {
		where += " AND api_key_id = ?"
		args = append(args, apiKeyID)
	}
	err := s.queryRow(ctx, fmt.Sprintf(`
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN decision = 'ALLOW' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN decision = 'DENY' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(latency_ms), 0),
			COALESCE(SUM(cost), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(CASE WHEN cached = 1 THEN 1 ELSE 0 END), 0)
		FROM request_logs WHERE %s`, where), args...,
	).Scan(&m.TotalRequests, &m.AllowedCount, &m.DeniedCount, &m.ErrorCount,
		&m.AvgLatencyMs, &m.TotalCost, &m.TotalTokens, &m.CacheHits)
	if err != nil {
		return m, err
	}

	elapsed := time.Since(since).Minutes()
	if elapsed > 0 && m.TotalRequests > 0 {
		m.RequestsPerMin = float64(m.TotalRequests) / elapsed
	}
	return m, nil
}

// GetProviderMetrics returns per-provider metrics.
func (s *Store) GetProviderMetrics(ctx context.Context, since time.Time) ([]ProviderMetrics, error) {
	return s.getProviderMetrics(ctx, since, "")
}

// GetProviderMetricsForAPIKey returns per-provider metrics only for one key.
func (s *Store) GetProviderMetricsForAPIKey(ctx context.Context, since time.Time, apiKeyID string) ([]ProviderMetrics, error) {
	return s.getProviderMetrics(ctx, since, apiKeyID)
}

func (s *Store) getProviderMetrics(ctx context.Context, since time.Time, apiKeyID string) ([]ProviderMetrics, error) {
	sinceStr := formatTime(since)
	where := "timestamp >= ?"
	args := []any{sinceStr}
	if apiKeyID != "" {
		where += " AND api_key_id = ?"
		args = append(args, apiKeyID)
	}
	rows, err := s.query(ctx, fmt.Sprintf(`
		SELECT COALESCE(provider, ''),
			COUNT(*),
			COALESCE(AVG(latency_ms), 0),
			COALESCE(SUM(cost), 0),
			COALESCE(CAST(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) AS REAL) / NULLIF(COUNT(*), 0), 0)
		FROM request_logs WHERE %s
		GROUP BY COALESCE(provider, '')`, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []ProviderMetrics
	for rows.Next() {
		var pm ProviderMetrics
		if err := rows.Scan(&pm.Provider, &pm.RequestCount, &pm.AvgLatencyMs, &pm.TotalCost, &pm.ErrorRate); err != nil {
			return nil, err
		}
		metrics = append(metrics, pm)
	}
	return metrics, rows.Err()
}

// apiKeyColumns selects a full api_keys row for scanAPIKey-style reads.
const apiKeyColumns = `id, name, key_hash, key_prefix, created_at, expires_at, COALESCE(rate_limit, 0), COALESCE(budget_usd, 0),
	COALESCE(spend_usd, 0), COALESCE(monthly_cost_budget, 0), COALESCE(monthly_token_budget, 0), COALESCE(enabled, 0),
	workspace_id, role, COALESCE(metadata, '')`

// InsertAPIKey stores an API key.
func (s *Store) InsertAPIKey(ctx context.Context, key APIKey) error {
	meta, _ := json.Marshal(key.Metadata)
	var expiresAt *string
	if key.ExpiresAt != nil {
		s := formatTime(*key.ExpiresAt)
		expiresAt = &s
	}
	// One statement, so a concurrent IncrementAPIKeySpend can never land
	// between an insert and a follow-up update. spend_usd is only written
	// for a new key: re-running the upsert (the bootstrap key on every
	// restart) must not reset accumulated spend.
	_, err := s.exec(ctx,
		`INSERT INTO api_keys (id, name, key_hash, key_prefix, created_at, expires_at, rate_limit,
			budget_usd, spend_usd, monthly_cost_budget, monthly_token_budget, enabled, workspace_id, role, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			key_hash = excluded.key_hash,
			key_prefix = excluded.key_prefix,
			expires_at = excluded.expires_at,
			rate_limit = excluded.rate_limit,
			budget_usd = excluded.budget_usd,
			monthly_cost_budget = excluded.monthly_cost_budget,
			monthly_token_budget = excluded.monthly_token_budget,
			enabled = excluded.enabled,
			workspace_id = excluded.workspace_id,
			role = excluded.role,
			metadata = excluded.metadata`,
		key.ID, key.Name, key.KeyHash, key.KeyPrefix,
		formatTime(key.CreatedAt), expiresAt, key.RateLimit,
		firstPositive(key.BudgetUSD, key.MonthlyCostBudget), key.SpendUSD, key.MonthlyCostBudget, key.MonthlyTokenBudget,
		boolToInt(key.Enabled), key.WorkspaceID, key.Role, string(meta),
	)
	return err
}

// GetAPIKeyByHash looks up an API key by its SHA-256 hash.
func (s *Store) GetAPIKeyByHash(ctx context.Context, hash string) (APIKey, error) {
	var key APIKey
	var createdAt, metaStr string
	var expiresAt, workspaceID, role sql.NullString
	var enabled int

	err := s.queryRow(ctx,
		"SELECT "+apiKeyColumns+" FROM api_keys WHERE key_hash = ?",
		hash,
	).Scan(&key.ID, &key.Name, &key.KeyHash, &key.KeyPrefix, &createdAt, &expiresAt, &key.RateLimit, &key.BudgetUSD, &key.SpendUSD, &key.MonthlyCostBudget, &key.MonthlyTokenBudget, &enabled, &workspaceID, &role, &metaStr)
	if err != nil {
		return key, err
	}

	key.CreatedAt = parseTime(createdAt)
	if expiresAt.Valid {
		t := parseTime(expiresAt.String)
		key.ExpiresAt = &t
	}
	key.WorkspaceID = nullableString(workspaceID)
	key.Role = nullableString(role)
	key.Enabled = enabled == 1
	_ = json.Unmarshal([]byte(metaStr), &key.Metadata)
	return key, nil
}

// ListAPIKeys returns all API keys.
func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.query(ctx,
		`SELECT id, name, key_prefix, created_at, expires_at, COALESCE(rate_limit, 0), COALESCE(budget_usd, 0), COALESCE(spend_usd, 0),
			COALESCE(monthly_cost_budget, 0), COALESCE(monthly_token_budget, 0), COALESCE(enabled, 0), workspace_id, role
		FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var k APIKey
		var createdAt string
		var expiresAt, workspaceID, role sql.NullString
		var enabled int
		if err := rows.Scan(&k.ID, &k.Name, &k.KeyPrefix, &createdAt, &expiresAt, &k.RateLimit, &k.BudgetUSD, &k.SpendUSD, &k.MonthlyCostBudget, &k.MonthlyTokenBudget, &enabled, &workspaceID, &role); err != nil {
			return nil, err
		}
		k.CreatedAt = parseTime(createdAt)
		if expiresAt.Valid {
			t := parseTime(expiresAt.String)
			k.ExpiresAt = &t
		}
		k.WorkspaceID = nullableString(workspaceID)
		k.Role = nullableString(role)
		k.Enabled = enabled == 1
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// DeleteAPIKey disables an API key.
func (s *Store) DeleteAPIKey(ctx context.Context, id string) error {
	_, err := s.exec(ctx, "UPDATE api_keys SET enabled = 0 WHERE id = ?", id)
	return err
}

// InsertPrompt stores a prompt template.
func (s *Store) InsertPrompt(ctx context.Context, p Prompt) error {
	vars, _ := json.Marshal(p.Variables)
	meta, _ := json.Marshal(p.Metadata)
	_, err := s.exec(ctx,
		`INSERT INTO prompts (id, name, version, template, variables, created_at, updated_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Version, p.Template, string(vars),
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt), string(meta),
	)
	return err
}

// ListPrompts returns all prompts.
func (s *Store) ListPrompts(ctx context.Context) ([]Prompt, error) {
	rows, err := s.query(ctx,
		"SELECT id, name, COALESCE(version, 1), template, COALESCE(variables, ''), created_at, updated_at FROM prompts ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prompts []Prompt
	for rows.Next() {
		var p Prompt
		var createdAt, updatedAt, varsStr string
		if err := rows.Scan(&p.ID, &p.Name, &p.Version, &p.Template, &varsStr, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTime(createdAt)
		p.UpdatedAt = parseTime(updatedAt)
		_ = json.Unmarshal([]byte(varsStr), &p.Variables)
		prompts = append(prompts, p)
	}
	return prompts, rows.Err()
}

func (s *Store) ListPromptVersions(ctx context.Context, name string) ([]Prompt, error) {
	rows, err := s.query(ctx,
		"SELECT id, name, COALESCE(version, 1), template, COALESCE(variables, ''), created_at, updated_at FROM prompts WHERE name = ? ORDER BY version DESC",
		name,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prompts []Prompt
	for rows.Next() {
		var p Prompt
		var createdAt, updatedAt, varsStr string
		if err := rows.Scan(&p.ID, &p.Name, &p.Version, &p.Template, &varsStr, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTime(createdAt)
		p.UpdatedAt = parseTime(updatedAt)
		_ = json.Unmarshal([]byte(varsStr), &p.Variables)
		prompts = append(prompts, p)
	}
	return prompts, rows.Err()
}

// maxPromptVersionAttempts bounds CreatePromptVersion retries when a
// concurrent create for the same name wins the race for a version number.
const maxPromptVersionAttempts = 5

// CreatePromptVersion stores p as the next version of p.Name and returns it
// with Version set. The version is computed inside the INSERT, so two
// concurrent creates cannot both read the same MAX(version) and store a
// duplicate. On PostgreSQL a per-name advisory lock serializes creates for
// one name; on SQLite the single writer connection does. If the unique
// (name, version) index rejects the row anyway, the insert is retried.
func (s *Store) CreatePromptVersion(ctx context.Context, p Prompt) (Prompt, error) {
	vars, _ := json.Marshal(p.Variables)
	meta, _ := json.Marshal(p.Metadata)
	query := `INSERT INTO prompts (id, name, version, template, variables, created_at, updated_at, metadata)
		SELECT CAST(? AS TEXT), CAST(? AS TEXT), COALESCE(MAX(version), 0) + 1, CAST(? AS TEXT), CAST(? AS TEXT),
			CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT)
		FROM prompts WHERE name = ?
		RETURNING version`
	if s.pg {
		query = rewriteSQL(query)
	}
	args := []any{p.ID, p.Name, p.Template, string(vars), formatTime(p.CreatedAt), formatTime(p.UpdatedAt), string(meta), p.Name}
	var lastErr error
	for attempt := 0; attempt < maxPromptVersionAttempts; attempt++ {
		version, err := s.insertPromptVersion(ctx, query, p.Name, args)
		if err == nil {
			p.Version = version
			return p, nil
		}
		if !isUniqueViolation(err) {
			return p, err
		}
		lastErr = err
	}
	return p, fmt.Errorf("create prompt version: %w", lastErr)
}

func (s *Store) insertPromptVersion(ctx context.Context, query, name string, args []any) (int, error) {
	tx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if s.pg {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "omniswitch.prompt:"+name); err != nil {
			return 0, err
		}
	}
	var version int
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&version); err != nil {
		return 0, err
	}
	return version, tx.Commit()
}

// isUniqueViolation reports whether err is a unique-constraint failure on
// either backend.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// NextPromptVersion returns MAX(version)+1 for name. It is not safe for
// concurrent creates; use CreatePromptVersion to store a new version.
func (s *Store) NextPromptVersion(ctx context.Context, name string) (int, error) {
	var latest int
	err := s.queryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM prompts WHERE name = ?", name).Scan(&latest)
	if err != nil {
		return 0, err
	}
	return latest + 1, nil
}

// GetPrompt returns a specific prompt by ID.
func (s *Store) GetPrompt(ctx context.Context, id string) (Prompt, error) {
	var p Prompt
	var createdAt, updatedAt, varsStr string
	err := s.queryRow(ctx,
		"SELECT id, name, COALESCE(version, 1), template, COALESCE(variables, ''), created_at, updated_at FROM prompts WHERE id = ?", id,
	).Scan(&p.ID, &p.Name, &p.Version, &p.Template, &varsStr, &createdAt, &updatedAt)
	if err != nil {
		return p, err
	}
	p.CreatedAt = parseTime(createdAt)
	p.UpdatedAt = parseTime(updatedAt)
	_ = json.Unmarshal([]byte(varsStr), &p.Variables)
	return p, nil
}

// InsertGuardrailEvent stores a guardrail event.
func (s *Store) InsertGuardrailEvent(ctx context.Context, e GuardrailEvent) error {
	_, err := s.exec(ctx,
		`INSERT INTO guardrail_events (id, request_id, timestamp, guardrail_type, action, matched, details)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.RequestID, formatTime(e.Timestamp),
		e.Type, e.Action, boolToInt(e.Matched), e.Details,
	)
	return err
}

func (s *Store) listGuardrailEventsForRequests(ctx context.Context, requestIDs []string) ([]GuardrailEvent, error) {
	if len(requestIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(requestIDs))
	args := make([]any, len(requestIDs))
	for i, id := range requestIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.query(ctx,
		fmt.Sprintf(`SELECT id, COALESCE(request_id, ''), timestamp, COALESCE(guardrail_type, ''), COALESCE(action, ''),
			COALESCE(matched, 0), COALESCE(details, '')
		FROM guardrail_events WHERE request_id IN (%s) ORDER BY timestamp ASC`, strings.Join(placeholders, ",")),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []GuardrailEvent
	for rows.Next() {
		var event GuardrailEvent
		var timestamp string
		var matched int
		if err := rows.Scan(&event.ID, &event.RequestID, &timestamp, &event.Type, &event.Action, &matched, &event.Details); err != nil {
			return nil, err
		}
		event.Timestamp = parseTime(timestamp)
		event.Matched = matched == 1
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) GetAPIKeyByID(ctx context.Context, id string) (APIKey, error) {
	var key APIKey
	var createdAt, metaStr string
	var expiresAt, workspaceID, role sql.NullString
	var enabled int

	err := s.queryRow(ctx,
		"SELECT "+apiKeyColumns+" FROM api_keys WHERE id = ?",
		id,
	).Scan(&key.ID, &key.Name, &key.KeyHash, &key.KeyPrefix, &createdAt, &expiresAt, &key.RateLimit, &key.BudgetUSD, &key.SpendUSD, &key.MonthlyCostBudget, &key.MonthlyTokenBudget, &enabled, &workspaceID, &role, &metaStr)
	if err != nil {
		return key, err
	}

	key.CreatedAt = parseTime(createdAt)
	if expiresAt.Valid {
		t := parseTime(expiresAt.String)
		key.ExpiresAt = &t
	}
	key.WorkspaceID = nullableString(workspaceID)
	key.Role = nullableString(role)
	key.Enabled = enabled == 1
	_ = json.Unmarshal([]byte(metaStr), &key.Metadata)
	return key, nil
}

func (s *Store) IncrementAPIKeySpend(ctx context.Context, id string, amount float64) error {
	if id == "" || amount <= 0 {
		return nil
	}
	_, err := s.exec(ctx, "UPDATE api_keys SET spend_usd = spend_usd + ? WHERE id = ?", amount, id)
	return err
}

func (s *Store) GetBudgetUsage(ctx context.Context, apiKeyID string, since time.Time) (BudgetUsage, error) {
	var usage BudgetUsage
	err := s.queryRow(ctx, `
		SELECT COALESCE(SUM(cost), 0), COALESCE(SUM(total_tokens), 0)
		FROM request_logs
		WHERE api_key_id = ? AND timestamp >= ? AND decision = 'ALLOW'`,
		apiKeyID,
		formatTime(since),
	).Scan(&usage.Cost, &usage.Tokens)
	return usage, err
}

func (s *Store) InsertSemanticCache(ctx context.Context, entry CacheEntry) error {
	vectorJSON, err := json.Marshal(entry.Vector)
	if err != nil {
		return fmt.Errorf("marshal cache vector: %w", err)
	}
	responseJSON, err := json.Marshal(entry.Response)
	if err != nil {
		return fmt.Errorf("marshal cache response: %w", err)
	}

	var expiresAt *string
	if entry.ExpiresAt != nil {
		value := formatTime(*entry.ExpiresAt)
		expiresAt = &value
	}

	// Entries with an exact-match key are upserted on that key. Inserting a
	// fresh row for a key whose previous entry had expired used to violate
	// the unique idx_cache_key index, so that prompt could never be cached
	// again. The conflict target repeats the partial-index predicate, which
	// both SQLite and PostgreSQL need to match a partial unique index.
	conflict := `ON CONFLICT(cache_key) WHERE cache_key IS NOT NULL AND cache_key != '' DO UPDATE SET
			created_at = excluded.created_at, expires_at = excluded.expires_at,
			model = excluded.model, prompt_text = excluded.prompt_text,
			vector_json = excluded.vector_json, response_json = excluded.response_json`
	if entry.Key == "" {
		conflict = `ON CONFLICT(id) DO UPDATE SET
			cache_key = excluded.cache_key, created_at = excluded.created_at, expires_at = excluded.expires_at,
			model = excluded.model, prompt_text = excluded.prompt_text,
			vector_json = excluded.vector_json, response_json = excluded.response_json`
	}
	_, err = s.exec(ctx,
		`INSERT INTO semantic_cache (id, cache_key, created_at, expires_at, model, prompt_text, vector_json, response_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`+conflict,
		entry.ID,
		entry.Key,
		formatTime(entry.CreatedAt),
		expiresAt,
		entry.Model,
		entry.Prompt,
		string(vectorJSON),
		string(responseJSON),
	)
	return err
}

// purgeBatchSize bounds how many rows one DELETE removes, so a large purge
// does not hold the SQLite write lock (or one long PostgreSQL statement)
// for the whole run.
const purgeBatchSize = 1000

// PurgeExpiredCache deletes cache entries that expired before now and
// returns how many rows it removed. Entries without an expiry are kept.
func (s *Store) PurgeExpiredCache(ctx context.Context, now time.Time) (int64, error) {
	return s.deleteInBatches(ctx, "semantic_cache",
		"expires_at IS NOT NULL AND expires_at != '' AND expires_at < ?", formatTime(now))
}

// RetentionResult reports how many rows PurgeRecordsBefore removed.
type RetentionResult struct {
	RequestLogs     int64 `json:"request_logs"`
	GuardrailEvents int64 `json:"guardrail_events"`
	ShadowRequests  int64 `json:"shadow_requests"`
}

// PurgeRecordsBefore deletes request logs, guardrail events and shadow
// requests with a timestamp before cutoff, in batches. Monthly budgets are
// computed from request_logs, so callers should never pass a cutoff inside
// the current month.
func (s *Store) PurgeRecordsBefore(ctx context.Context, cutoff time.Time) (RetentionResult, error) {
	var result RetentionResult
	bound := formatTime(cutoff)
	var err error
	if result.RequestLogs, err = s.deleteInBatches(ctx, "request_logs", "timestamp < ?", bound); err != nil {
		return result, err
	}
	if result.GuardrailEvents, err = s.deleteInBatches(ctx, "guardrail_events", "timestamp < ?", bound); err != nil {
		return result, err
	}
	if result.ShadowRequests, err = s.deleteInBatches(ctx, "shadow_requests", "timestamp < ?", bound); err != nil {
		return result, err
	}
	return result, nil
}

// deleteInBatches deletes the rows of table that match where, at most
// purgeBatchSize per statement. table and where are package constants,
// never caller input.
func (s *Store) deleteInBatches(ctx context.Context, table, where string, args ...any) (int64, error) {
	query := fmt.Sprintf("DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE %s LIMIT ?)", table, table, where)
	args = append(args, purgeBatchSize)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, err := s.exec(ctx, query, args...)
		if err != nil {
			return total, fmt.Errorf("purge %s: %w", table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("purge %s: %w", table, err)
		}
		total += n
		if n < purgeBatchSize {
			return total, nil
		}
	}
}

func (s *Store) GetExactCache(ctx context.Context, key string) (CacheEntry, bool, error) {
	var entry CacheEntry
	var createdAt, expiresAt, vectorJSON, responseJSON string
	err := s.queryRow(ctx,
		`SELECT id, COALESCE(cache_key, ''), created_at, COALESCE(expires_at, ''), model, prompt_text, vector_json, response_json
		FROM semantic_cache
		WHERE cache_key = ? AND (expires_at IS NULL OR expires_at = '' OR expires_at > ?)
		LIMIT 1`,
		key,
		formatTime(time.Now().UTC()),
	).Scan(&entry.ID, &entry.Key, &createdAt, &expiresAt, &entry.Model, &entry.Prompt, &vectorJSON, &responseJSON)
	if err == sql.ErrNoRows {
		return CacheEntry{}, false, nil
	}
	if err != nil {
		return CacheEntry{}, false, err
	}
	entry.CreatedAt = parseTime(createdAt)
	if expiresAt != "" {
		parsed := parseTime(expiresAt)
		entry.ExpiresAt = &parsed
	}
	if err := json.Unmarshal([]byte(vectorJSON), &entry.Vector); err != nil {
		return CacheEntry{}, false, err
	}
	if err := json.Unmarshal([]byte(responseJSON), &entry.Response); err != nil {
		return CacheEntry{}, false, err
	}
	return entry, true, nil
}

func (s *Store) FindSemanticCache(ctx context.Context, model string, vector map[string]float64, threshold float64) (CacheEntry, bool, error) {
	rows, err := s.query(ctx,
		`SELECT id, COALESCE(cache_key, ''), created_at, COALESCE(expires_at, ''), model, prompt_text, vector_json, response_json
		FROM semantic_cache
		WHERE model = ? AND (expires_at IS NULL OR expires_at = '' OR expires_at > ?)
		ORDER BY created_at DESC LIMIT 250`,
		model,
		formatTime(time.Now().UTC()),
	)
	if err != nil {
		return CacheEntry{}, false, err
	}
	defer rows.Close()

	var best CacheEntry
	var bestSimilarity float64
	for rows.Next() {
		var entry CacheEntry
		var createdAt, expiresAt, vectorJSON, responseJSON string
		if err := rows.Scan(&entry.ID, &entry.Key, &createdAt, &expiresAt, &entry.Model, &entry.Prompt, &vectorJSON, &responseJSON); err != nil {
			return CacheEntry{}, false, err
		}
		entry.CreatedAt = parseTime(createdAt)
		if expiresAt != "" {
			parsed := parseTime(expiresAt)
			entry.ExpiresAt = &parsed
		}
		if err := json.Unmarshal([]byte(vectorJSON), &entry.Vector); err != nil {
			continue
		}
		similarity := cache.Similarity(vector, entry.Vector)
		if similarity <= bestSimilarity {
			continue
		}
		if err := json.Unmarshal([]byte(responseJSON), &entry.Response); err != nil {
			continue
		}
		entry.Similarity = similarity
		best = entry
		bestSimilarity = similarity
	}
	if err := rows.Err(); err != nil {
		return CacheEntry{}, false, err
	}
	if bestSimilarity < threshold {
		return CacheEntry{}, false, nil
	}
	return best, true, nil
}

const shadowColumns = `id, request_id, COALESCE(trace_id, ''), timestamp, COALESCE(primary_provider, ''),
	COALESCE(shadow_provider, ''), COALESCE(model, ''), COALESCE(latency_ms, 0), COALESCE(cost, 0),
	COALESCE(status, ''), COALESCE(error_message, '')`

func (s *Store) InsertShadowLog(ctx context.Context, entry ShadowLog) error {
	_, err := s.exec(ctx,
		`INSERT INTO shadow_requests (id, request_id, trace_id, timestamp, primary_provider, shadow_provider, model, latency_ms, cost, status, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID,
		entry.RequestID,
		entry.TraceID,
		formatTime(entry.Timestamp),
		entry.PrimaryProvider,
		entry.ShadowProvider,
		entry.Model,
		entry.LatencyMs,
		entry.Cost,
		entry.Status,
		entry.ErrorMessage,
	)
	return err
}

func (s *Store) ListShadowLogs(ctx context.Context, requestID string) ([]ShadowLog, error) {
	rows, err := s.query(ctx,
		`SELECT `+shadowColumns+`
		FROM shadow_requests WHERE request_id = ? ORDER BY timestamp DESC`,
		requestID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []ShadowLog
	for rows.Next() {
		var entry ShadowLog
		var timestamp string
		if err := rows.Scan(&entry.ID, &entry.RequestID, &entry.TraceID, &timestamp, &entry.PrimaryProvider, &entry.ShadowProvider, &entry.Model, &entry.LatencyMs, &entry.Cost, &entry.Status, &entry.ErrorMessage); err != nil {
			return nil, err
		}
		entry.Timestamp = parseTime(timestamp)
		logs = append(logs, entry)
	}
	return logs, rows.Err()
}

func (s *Store) listShadowLogsForTrace(ctx context.Context, traceID string) ([]ShadowLog, error) {
	rows, err := s.query(ctx,
		`SELECT `+shadowColumns+`
		FROM shadow_requests WHERE trace_id = ? ORDER BY timestamp ASC`,
		traceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var logs []ShadowLog
	for rows.Next() {
		var entry ShadowLog
		var timestamp string
		if err := rows.Scan(&entry.ID, &entry.RequestID, &entry.TraceID, &timestamp, &entry.PrimaryProvider, &entry.ShadowProvider, &entry.Model, &entry.LatencyMs, &entry.Cost, &entry.Status, &entry.ErrorMessage); err != nil {
			return nil, err
		}
		entry.Timestamp = parseTime(timestamp)
		logs = append(logs, entry)
	}
	return logs, rows.Err()
}

func (s *Store) InsertFeedback(ctx context.Context, entry Feedback) error {
	metadata, _ := json.Marshal(entry.Metadata)
	_, err := s.exec(ctx,
		`INSERT INTO feedback (id, request_id, trace_id, timestamp, score, rating, comment, user_id, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID,
		entry.RequestID,
		entry.TraceID,
		formatTime(entry.Timestamp),
		entry.Score,
		entry.Rating,
		entry.Comment,
		entry.UserID,
		string(metadata),
	)
	return err
}

func (s *Store) ListFeedback(ctx context.Context, limit int, requestID, traceID string) ([]Feedback, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	where := "1=1"
	args := []any{}
	if requestID != "" {
		where += " AND request_id = ?"
		args = append(args, requestID)
	}
	if traceID != "" {
		where += " AND trace_id = ?"
		args = append(args, traceID)
	}
	args = append(args, limit)

	rows, err := s.query(ctx,
		fmt.Sprintf(`SELECT id, COALESCE(request_id, ''), COALESCE(trace_id, ''), timestamp, COALESCE(score, 0), COALESCE(rating, ''),
			COALESCE(comment, ''), COALESCE(user_id, ''), COALESCE(metadata, '')
		FROM feedback WHERE %s ORDER BY timestamp DESC LIMIT ?`, where),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []Feedback
	for rows.Next() {
		var entry Feedback
		var timestamp string
		var metadata string
		if err := rows.Scan(&entry.ID, &entry.RequestID, &entry.TraceID, &timestamp, &entry.Score, &entry.Rating, &entry.Comment, &entry.UserID, &metadata); err != nil {
			return nil, err
		}
		entry.Timestamp = parseTime(timestamp)
		_ = json.Unmarshal([]byte(metadata), &entry.Metadata)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) InsertOrganization(ctx context.Context, org Organization) error {
	if org.CreatedAt.IsZero() {
		org.CreatedAt = time.Now().UTC()
	}
	metadata, _ := json.Marshal(org.Metadata)
	_, err := s.exec(ctx,
		`INSERT INTO organizations (id, name, created_at, metadata)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, metadata = excluded.metadata`,
		org.ID,
		org.Name,
		formatTime(org.CreatedAt),
		string(metadata),
	)
	return err
}

func (s *Store) ListOrganizations(ctx context.Context) ([]Organization, error) {
	rows, err := s.query(ctx,
		"SELECT id, name, created_at, COALESCE(metadata, '') FROM organizations ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var organizations []Organization
	for rows.Next() {
		var org Organization
		var createdAt, metadata string
		if err := rows.Scan(&org.ID, &org.Name, &createdAt, &metadata); err != nil {
			return nil, err
		}
		org.CreatedAt = parseTime(createdAt)
		_ = json.Unmarshal([]byte(metadata), &org.Metadata)
		organizations = append(organizations, org)
	}
	return organizations, rows.Err()
}

func (s *Store) InsertWorkspace(ctx context.Context, workspace Workspace) error {
	if workspace.CreatedAt.IsZero() {
		workspace.CreatedAt = time.Now().UTC()
	}
	metadata, _ := json.Marshal(workspace.Metadata)
	_, err := s.exec(ctx,
		`INSERT INTO workspaces (id, organization_id, name, created_at, metadata)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			organization_id = excluded.organization_id,
			name = excluded.name,
			metadata = excluded.metadata`,
		workspace.ID,
		workspace.OrganizationID,
		workspace.Name,
		formatTime(workspace.CreatedAt),
		string(metadata),
	)
	return err
}

func (s *Store) ListWorkspaces(ctx context.Context, organizationID string) ([]Workspace, error) {
	where := "1=1"
	args := []any{}
	if organizationID != "" {
		where = "organization_id = ?"
		args = append(args, organizationID)
	}
	rows, err := s.query(ctx,
		fmt.Sprintf("SELECT id, organization_id, name, created_at, COALESCE(metadata, '') FROM workspaces WHERE %s ORDER BY created_at DESC", where),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workspaces []Workspace
	for rows.Next() {
		var workspace Workspace
		var createdAt, metadata string
		if err := rows.Scan(&workspace.ID, &workspace.OrganizationID, &workspace.Name, &createdAt, &metadata); err != nil {
			return nil, err
		}
		workspace.CreatedAt = parseTime(createdAt)
		_ = json.Unmarshal([]byte(metadata), &workspace.Metadata)
		workspaces = append(workspaces, workspace)
	}
	return workspaces, rows.Err()
}

// GetWorkspaceByID returns the authoritative organization mapping for a
// workspace. Authentication uses this to derive tenant scope rather than
// trusting a request header supplied by the caller.
func (s *Store) GetWorkspaceByID(ctx context.Context, id string) (Workspace, error) {
	var workspace Workspace
	var createdAt, metadata string
	err := s.queryRow(ctx,
		"SELECT id, organization_id, name, created_at, COALESCE(metadata, '') FROM workspaces WHERE id = ?", id,
	).Scan(&workspace.ID, &workspace.OrganizationID, &workspace.Name, &createdAt, &metadata)
	if err != nil {
		return workspace, err
	}
	workspace.CreatedAt = parseTime(createdAt)
	_ = json.Unmarshal([]byte(metadata), &workspace.Metadata)
	return workspace, nil
}

// InsertUser upserts user by email. Use UpsertUser to learn the stored ID.
func (s *Store) InsertUser(ctx context.Context, user User) error {
	_, err := s.UpsertUser(ctx, user)
	return err
}

// UpsertUser inserts user, or updates the name and metadata of the existing
// user with the same email, and returns the stored row. On an existing email
// the stored ID and created_at win over the ones in user, so callers must
// use the returned ID (for example for workspace memberships).
func (s *Store) UpsertUser(ctx context.Context, user User) (User, error) {
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}
	metadata, _ := json.Marshal(user.Metadata)
	var id, createdAt string
	err := s.writeQueryRow(ctx,
		`INSERT INTO users (id, email, name, created_at, metadata)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET name = excluded.name, metadata = excluded.metadata
		RETURNING id, created_at`,
		user.ID,
		user.Email,
		user.Name,
		formatTime(user.CreatedAt),
		string(metadata),
	).Scan(&id, &createdAt)
	if err != nil {
		return user, err
	}
	user.ID = id
	user.CreatedAt = parseTime(createdAt)
	return user, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.query(ctx,
		"SELECT id, email, COALESCE(name, ''), created_at, COALESCE(metadata, '') FROM users ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var user User
		var createdAt, metadata string
		if err := rows.Scan(&user.ID, &user.Email, &user.Name, &createdAt, &metadata); err != nil {
			return nil, err
		}
		user.CreatedAt = parseTime(createdAt)
		_ = json.Unmarshal([]byte(metadata), &user.Metadata)
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) UpsertWorkspaceMember(ctx context.Context, member WorkspaceMember) error {
	if member.CreatedAt.IsZero() {
		member.CreatedAt = time.Now().UTC()
	}
	_, err := s.exec(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(workspace_id, user_id) DO UPDATE SET role = excluded.role`,
		member.WorkspaceID,
		member.UserID,
		member.Role,
		formatTime(member.CreatedAt),
	)
	return err
}

func (s *Store) ListWorkspaceMembers(ctx context.Context, workspaceID string) ([]WorkspaceMember, error) {
	where := "1=1"
	args := []any{}
	if workspaceID != "" {
		where = "workspace_id = ?"
		args = append(args, workspaceID)
	}
	rows, err := s.query(ctx,
		fmt.Sprintf("SELECT workspace_id, user_id, role, created_at FROM workspace_members WHERE %s ORDER BY created_at DESC", where),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []WorkspaceMember
	for rows.Next() {
		var member WorkspaceMember
		var createdAt string
		if err := rows.Scan(&member.WorkspaceID, &member.UserID, &member.Role, &createdAt); err != nil {
			return nil, err
		}
		member.CreatedAt = parseTime(createdAt)
		members = append(members, member)
	}
	return members, rows.Err()
}

func (s *Store) InsertVirtualKey(ctx context.Context, key VirtualKeyRecord) error {
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now().UTC()
	}
	if key.UpdatedAt.IsZero() {
		key.UpdatedAt = key.CreatedAt
	}
	_, err := s.exec(ctx,
		`INSERT INTO virtual_keys (id, name, provider_type, provider_name, base_url, encrypted_key, created_at, updated_at, enabled, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			provider_type = excluded.provider_type,
			provider_name = excluded.provider_name,
			base_url = excluded.base_url,
			encrypted_key = excluded.encrypted_key,
			updated_at = excluded.updated_at,
			enabled = excluded.enabled,
			metadata = excluded.metadata`,
		key.ID,
		key.Name,
		key.ProviderType,
		key.ProviderName,
		key.BaseURL,
		key.EncryptedKey,
		formatTime(key.CreatedAt),
		formatTime(key.UpdatedAt),
		boolToInt(key.Enabled),
		key.MetadataJSON,
	)
	return err
}

func (s *Store) GetVirtualKey(ctx context.Context, name string) (VirtualKeyRecord, error) {
	var record VirtualKeyRecord
	var createdAt, updatedAt string
	var enabled int
	err := s.queryRow(ctx,
		`SELECT id, name, provider_type, COALESCE(provider_name, ''), COALESCE(base_url, ''), encrypted_key,
			created_at, updated_at, enabled, COALESCE(metadata, '{}')
		FROM virtual_keys WHERE name = ?`,
		name,
	).Scan(&record.ID, &record.Name, &record.ProviderType, &record.ProviderName, &record.BaseURL,
		&record.EncryptedKey, &createdAt, &updatedAt, &enabled, &record.MetadataJSON)
	if err != nil {
		return record, err
	}
	record.CreatedAt = parseTime(createdAt)
	record.UpdatedAt = parseTime(updatedAt)
	record.Enabled = enabled == 1
	return record, nil
}

func (s *Store) ListVirtualKeys(ctx context.Context) ([]VirtualKeyRecord, error) {
	rows, err := s.query(ctx,
		`SELECT id, name, provider_type, COALESCE(provider_name, ''), COALESCE(base_url, ''), encrypted_key,
			created_at, updated_at, enabled, COALESCE(metadata, '{}')
		FROM virtual_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []VirtualKeyRecord
	for rows.Next() {
		var record VirtualKeyRecord
		var createdAt, updatedAt string
		var enabled int
		if err := rows.Scan(&record.ID, &record.Name, &record.ProviderType, &record.ProviderName, &record.BaseURL,
			&record.EncryptedKey, &createdAt, &updatedAt, &enabled, &record.MetadataJSON); err != nil {
			return nil, err
		}
		record.CreatedAt = parseTime(createdAt)
		record.UpdatedAt = parseTime(updatedAt)
		record.Enabled = enabled == 1
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) RotateVirtualKey(ctx context.Context, name, encryptedKey string) error {
	result, err := s.exec(ctx,
		"UPDATE virtual_keys SET encrypted_key = ?, updated_at = ? WHERE name = ?",
		encryptedKey,
		formatTime(time.Now().UTC()),
		name,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err == nil && affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DisableVirtualKey(ctx context.Context, name string) error {
	result, err := s.exec(ctx,
		"UPDATE virtual_keys SET enabled = 0, updated_at = ? WHERE name = ?",
		formatTime(time.Now().UTC()),
		name,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err == nil && affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	err := s.db.Close()
	if s.wdb != nil && s.wdb != s.db {
		if werr := s.wdb.Close(); err == nil {
			err = werr
		}
	}
	return err
}

func (s *Store) ensureColumn(ctx context.Context, tx *sql.Tx, table, column, definition string) error {
	if s.pg {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s", table, column, definition)); err != nil {
			return fmt.Errorf("add column %s.%s: %w", table, column, err)
		}
		return nil
	}
	exists, err := sqliteColumnExists(ctx, tx, table, column)
	if err != nil {
		return err
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

// timeLayout is the one layout every timestamp column is written in. It is
// fixed width (nanoseconds are never trimmed) and always UTC, so the TEXT
// values sort and compare in time order. time.RFC3339Nano trims trailing
// zeros, and because '.' sorts before 'Z', "00:00:00.5Z" compared lower
// than "00:00:00Z".
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// formatTime renders t for storage or for a bound in a timestamp comparison.
func formatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// parseTime reads a stored timestamp. time.RFC3339Nano also accepts the
// RFC3339 and RFC3339Nano values written by older releases.
func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func nullableString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// ErrNotFound is returned when a resource is not found.
var ErrNotFound = errors.New("not found")

// Dataset represents an evaluation dataset stored in the database.
type Dataset struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Version     int               `json:"version"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Metadata    map[string]string `json:"metadata"`
	Examples    []DatasetExample  `json:"examples"`
}

// DatasetExample represents a single example in a dataset.
type DatasetExample struct {
	ID        string                 `json:"id"`
	Input     model.ToolRequest      `json:"input"`
	Expected  *provider.ChatResponse `json:"expected,omitempty"`
	Metadata  map[string]string      `json:"metadata,omitempty"`
	Labels    []string               `json:"labels,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
}

// Experiment represents an evaluation experiment.
type Experiment struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	DatasetID   string            `json:"dataset_id"`
	Config      ExperimentConfig  `json:"config"`
	Status      string            `json:"status"`
	Results     *ExperimentResult `json:"results,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	CreatedBy   string            `json:"created_by"`
}

// ExperimentConfig defines the configuration for an experiment.
type ExperimentConfig struct {
	Models          []string          `json:"models"`
	Prompts         []string          `json:"prompts"`
	Metrics         []string          `json:"metrics"`
	SampleSize      int               `json:"sample_size"`
	Temperature     float64           `json:"temperature"`
	MaxTokens       int               `json:"max_tokens"`
	EvaluatorModel  string            `json:"evaluator_model"`
	CustomEvaluator string            `json:"custom_evaluator"`
	Config          map[string]string `json:"config"`
}

// ExperimentResult contains the results of an experiment.
type ExperimentResult struct {
	ModelResults   map[string]ModelResult `json:"model_results"`
	Comparison     *ComparisonResult      `json:"comparison,omitempty"`
	CompletedAt    time.Time              `json:"completed_at"`
	TotalEvaluated int                    `json:"total_evaluated"`
	Errors         int                    `json:"errors"`
}

// ModelResult contains results for a single model.
type ModelResult struct {
	Model          string             `json:"model"`
	Metrics        map[string]float64 `json:"metrics"`
	LatencyP50     float64            `json:"latency_p50_ms"`
	LatencyP95     float64            `json:"latency_p95_ms"`
	LatencyP99     float64            `json:"latency_p99_ms"`
	ErrorRate      float64            `json:"error_rate"`
	CostPerRequest float64            `json:"cost_per_request"`
	TotalRequests  int                `json:"total_requests"`
	Successful     int                `json:"successful"`
	Failed         int                `json:"failed"`
	PerExample     []ExampleResult    `json:"per_example,omitempty"`
}

// ExampleResult is the result for a single example.
type ExampleResult struct {
	ExampleID string                 `json:"example_id"`
	Input     model.ToolRequest      `json:"input"`
	Output    provider.ChatResponse  `json:"output"`
	Expected  *provider.ChatResponse `json:"expected,omitempty"`
	Score     float64                `json:"score"`
	LatencyMs float64                `json:"latency_ms"`
	Error     string                 `json:"error,omitempty"`
	Cost      float64                `json:"cost"`
	TokensIn  int                    `json:"tokens_in"`
	TokensOut int                    `json:"tokens_out"`
}

// ComparisonResult compares multiple models.
type ComparisonResult struct {
	BestModel          string                    `json:"best_model"`
	MetricWinners      map[string]string         `json:"metric_winners"`
	Significance       map[string]float64        `json:"significance"`
	PairwiseComparison map[string]PairwiseResult `json:"pairwise_comparison"`
}

// PairwiseResult represents a pairwise comparison.
type PairwiseResult struct {
	ModelA      string  `json:"model_a"`
	ModelB      string  `json:"model_b"`
	Metric      string  `json:"metric"`
	Winner      string  `json:"winner"`
	Diff        float64 `json:"diff"`
	PValue      float64 `json:"p_value"`
	Significant bool    `json:"significant"`
}

// CreateDataset creates a new dataset.
func (s *Store) CreateDataset(ctx context.Context, dataset *Dataset) error {
	examplesJSON, err := json.Marshal(dataset.Examples)
	if err != nil {
		return err
	}
	metadataJSON, err := json.Marshal(dataset.Metadata)
	if err != nil {
		return err
	}

	_, err = s.exec(ctx,
		`INSERT INTO datasets (id, name, description, version, created_at, updated_at, metadata, examples)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		dataset.ID, dataset.Name, dataset.Description, dataset.Version,
		formatTime(dataset.CreatedAt), formatTime(dataset.UpdatedAt),
		string(metadataJSON), string(examplesJSON))
	return err
}

const datasetColumns = `id, name, COALESCE(description, ''), COALESCE(version, 1), created_at, updated_at,
		COALESCE(metadata, ''), COALESCE(examples, '')`

// scanDataset scans one datasetColumns row. Timestamps are stored as TEXT,
// so they are scanned into strings and parsed rather than into time.Time.
func scanDataset(row interface{ Scan(...any) error }) (*Dataset, error) {
	var dataset Dataset
	var createdAt, updatedAt, metadataJSON, examplesJSON string
	if err := row.Scan(&dataset.ID, &dataset.Name, &dataset.Description, &dataset.Version,
		&createdAt, &updatedAt, &metadataJSON, &examplesJSON); err != nil {
		return nil, err
	}
	dataset.CreatedAt = parseTime(createdAt)
	dataset.UpdatedAt = parseTime(updatedAt)
	if err := unmarshalOptionalJSON(metadataJSON, &dataset.Metadata); err != nil {
		return nil, fmt.Errorf("dataset %s metadata: %w", dataset.ID, err)
	}
	if err := unmarshalOptionalJSON(examplesJSON, &dataset.Examples); err != nil {
		return nil, fmt.Errorf("dataset %s examples: %w", dataset.ID, err)
	}
	return &dataset, nil
}

// unmarshalOptionalJSON decodes raw into v, treating an empty column or a
// JSON null as "no value".
func unmarshalOptionalJSON(raw string, v any) error {
	if raw == "" || raw == "null" {
		return nil
	}
	return json.Unmarshal([]byte(raw), v)
}

// GetDataset retrieves a dataset by ID.
func (s *Store) GetDataset(ctx context.Context, id string) (*Dataset, error) {
	dataset, err := scanDataset(s.queryRow(ctx,
		`SELECT `+datasetColumns+` FROM datasets WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return dataset, err
}

// ListDatasets lists datasets with pagination.
func (s *Store) ListDatasets(ctx context.Context, limit, offset int) ([]*Dataset, int, error) {
	limit, offset = ClampPage(limit, offset)
	var total int
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM datasets`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.query(ctx,
		`SELECT `+datasetColumns+` FROM datasets ORDER BY created_at DESC, id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var datasets []*Dataset
	for rows.Next() {
		dataset, err := scanDataset(rows)
		if err != nil {
			return nil, 0, err
		}
		datasets = append(datasets, dataset)
	}
	return datasets, total, rows.Err()
}

// UpdateDataset updates a dataset.
func (s *Store) UpdateDataset(ctx context.Context, dataset *Dataset) error {
	metadataJSON, err := json.Marshal(dataset.Metadata)
	if err != nil {
		return err
	}
	examplesJSON, err := json.Marshal(dataset.Examples)
	if err != nil {
		return err
	}

	result, err := s.exec(ctx,
		`UPDATE datasets SET name = ?, description = ?, version = ?, updated_at = ?, metadata = ?, examples = ?
		WHERE id = ?`,
		dataset.Name, dataset.Description, dataset.Version, formatTime(dataset.UpdatedAt),
		string(metadataJSON), string(examplesJSON), dataset.ID)
	return notFoundIfNoRows(result, err)
}

// DeleteDataset deletes a dataset.
func (s *Store) DeleteDataset(ctx context.Context, id string) error {
	result, err := s.exec(ctx, `DELETE FROM datasets WHERE id = ?`, id)
	return notFoundIfNoRows(result, err)
}

func notFoundIfNoRows(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// AddExamples appends examples to a dataset and bumps its version. The
// read-modify-write of the examples blob runs in one write transaction so
// concurrent calls cannot drop each other's examples: on SQLite the single
// immediate-mode writer connection serializes it, and on PostgreSQL the
// row is locked with SELECT ... FOR UPDATE.
func (s *Store) AddExamples(ctx context.Context, datasetID string, examples []DatasetExample) error {
	tx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	selectQuery := `SELECT ` + datasetColumns + ` FROM datasets WHERE id = ?`
	updateQuery := `UPDATE datasets SET examples = ?, version = ?, updated_at = ? WHERE id = ?`
	if s.pg {
		selectQuery = rewriteSQL(selectQuery + ` FOR UPDATE`)
		updateQuery = rewriteSQL(updateQuery)
	}
	dataset, err := scanDataset(tx.QueryRowContext(ctx, selectQuery, datasetID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	dataset.Examples = append(dataset.Examples, examples...)
	dataset.Version++
	dataset.UpdatedAt = time.Now().UTC()
	examplesJSON, err := json.Marshal(dataset.Examples)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, updateQuery,
		string(examplesJSON), dataset.Version, formatTime(dataset.UpdatedAt), datasetID); err != nil {
		return err
	}
	return tx.Commit()
}

// Experiment methods

func marshalExperimentResults(results *ExperimentResult) (string, error) {
	if results == nil {
		return "{}", nil
	}
	b, err := json.Marshal(results)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CreateExperiment creates a new experiment.
func (s *Store) CreateExperiment(ctx context.Context, exp *Experiment) error {
	configJSON, err := json.Marshal(exp.Config)
	if err != nil {
		return err
	}
	resultsJSON, err := marshalExperimentResults(exp.Results)
	if err != nil {
		return err
	}

	_, err = s.exec(ctx,
		`INSERT INTO experiments (id, name, description, dataset_id, config, status, results, created_at, updated_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		exp.ID, exp.Name, exp.Description, exp.DatasetID, string(configJSON),
		exp.Status, resultsJSON, formatTime(exp.CreatedAt),
		formatTime(exp.UpdatedAt), exp.CreatedBy)
	return err
}

const experimentColumns = `id, name, COALESCE(description, ''), COALESCE(dataset_id, ''), COALESCE(config, ''),
		COALESCE(status, ''), COALESCE(results, ''), created_at, updated_at, COALESCE(created_by, '')`

// scanExperiment scans one experimentColumns row. results is a JSON TEXT
// column; "{}" (what CreateExperiment stores for "no results yet") leaves
// Results nil.
func scanExperiment(row interface{ Scan(...any) error }) (*Experiment, error) {
	var exp Experiment
	var configJSON, resultsJSON, createdAt, updatedAt string
	if err := row.Scan(&exp.ID, &exp.Name, &exp.Description, &exp.DatasetID, &configJSON,
		&exp.Status, &resultsJSON, &createdAt, &updatedAt, &exp.CreatedBy); err != nil {
		return nil, err
	}
	exp.CreatedAt = parseTime(createdAt)
	exp.UpdatedAt = parseTime(updatedAt)
	if err := unmarshalOptionalJSON(configJSON, &exp.Config); err != nil {
		return nil, fmt.Errorf("experiment %s config: %w", exp.ID, err)
	}
	if resultsJSON != "{}" {
		if err := unmarshalOptionalJSON(resultsJSON, &exp.Results); err != nil {
			return nil, fmt.Errorf("experiment %s results: %w", exp.ID, err)
		}
	}
	return &exp, nil
}

// GetExperiment retrieves an experiment by ID.
func (s *Store) GetExperiment(ctx context.Context, id string) (*Experiment, error) {
	exp, err := scanExperiment(s.queryRow(ctx,
		`SELECT `+experimentColumns+` FROM experiments WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return exp, err
}

// ListExperiments lists experiments with pagination.
func (s *Store) ListExperiments(ctx context.Context, limit, offset int, status string) ([]*Experiment, int, error) {
	limit, offset = ClampPage(limit, offset)
	where := ""
	args := []any{}
	if status != "" {
		where = ` WHERE status = ?`
		args = append(args, status)
	}

	var total int
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM experiments`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.query(ctx,
		`SELECT `+experimentColumns+` FROM experiments`+where+` ORDER BY created_at DESC, id LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var experiments []*Experiment
	for rows.Next() {
		exp, err := scanExperiment(rows)
		if err != nil {
			return nil, 0, err
		}
		experiments = append(experiments, exp)
	}
	return experiments, total, rows.Err()
}

// UpdateExperiment updates an experiment.
func (s *Store) UpdateExperiment(ctx context.Context, exp *Experiment) error {
	configJSON, err := json.Marshal(exp.Config)
	if err != nil {
		return err
	}
	resultsJSON, err := marshalExperimentResults(exp.Results)
	if err != nil {
		return err
	}

	result, err := s.exec(ctx,
		`UPDATE experiments SET name = ?, description = ?, dataset_id = ?, config = ?, status = ?, results = ?, updated_at = ?
		WHERE id = ?`,
		exp.Name, exp.Description, exp.DatasetID, string(configJSON), exp.Status, resultsJSON,
		formatTime(exp.UpdatedAt), exp.ID)
	return notFoundIfNoRows(result, err)
}
