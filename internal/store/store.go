package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/cache"
	"github.com/omniswitch-dev/omniswitch/internal/provider"

	_ "modernc.org/sqlite"
)

// Store provides persistent storage for the gateway.
type Store struct {
	db *sql.DB
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

// New creates a new Store backed by a SQLite database at the given directory.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	dbPath := filepath.Join(dir, "sentinel.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable WAL mode for concurrent reads.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) migrate() error {
	migrations := []string{
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
			latency_ms REAL DEFAULT 0,
			cost REAL DEFAULT 0,
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
			budget_usd REAL DEFAULT 0,
			spend_usd REAL DEFAULT 0,
			monthly_cost_budget REAL DEFAULT 0,
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
			latency_ms REAL DEFAULT 0,
			cost REAL DEFAULT 0,
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
		`CREATE INDEX IF NOT EXISTS idx_logs_timestamp ON request_logs(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_trace ON request_logs(trace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_provider ON request_logs(provider)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_status ON request_logs(status)`,
		`CREATE INDEX IF NOT EXISTS idx_guardrail_request ON guardrail_events(request_id)`,
		`CREATE INDEX IF NOT EXISTS idx_cache_model ON semantic_cache(model)`,
		`CREATE INDEX IF NOT EXISTS idx_shadow_request ON shadow_requests(request_id)`,
		`CREATE INDEX IF NOT EXISTS idx_feedback_request ON feedback(request_id)`,
		`CREATE INDEX IF NOT EXISTS idx_feedback_trace ON feedback(trace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_workspaces_org ON workspaces(organization_id)`,
		`CREATE INDEX IF NOT EXISTS idx_workspace_members_user ON workspace_members(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_virtual_keys_name ON virtual_keys(name)`,
	}

	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	if err := s.ensureColumn("request_logs", "trace_id", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("request_logs", "session_id", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("api_keys", "monthly_cost_budget", "REAL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("api_keys", "monthly_token_budget", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("api_keys", "budget_usd", "REAL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("api_keys", "spend_usd", "REAL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("api_keys", "workspace_id", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("api_keys", "role", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("semantic_cache", "cache_key", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("semantic_cache", "expires_at", "TEXT"); err != nil {
		return err
	}
	if _, err := s.db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_cache_key ON semantic_cache(cache_key) WHERE cache_key IS NOT NULL AND cache_key != ''"); err != nil {
		return fmt.Errorf("create cache key index: %w", err)
	}
	return nil
}

// InsertLog stores a request log entry.
func (s *Store) InsertLog(ctx context.Context, log RequestLog) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO request_logs (id, timestamp, trace_id, session_id, provider, model, api_key_id, status,
			input_tokens, output_tokens, total_tokens, latency_ms, cost,
			request_body, response_body, decision, decision_reason, error_message, cached)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.ID, log.Timestamp.Format(time.RFC3339Nano), log.TraceID, log.SessionID, log.Provider, log.Model,
		log.APIKeyID, log.Status, log.InputTokens, log.OutputTokens, log.TotalTokens,
		log.LatencyMs, log.Cost, log.RequestBody, log.ResponseBody,
		log.Decision, log.DecisionReason, log.ErrorMessage, boolToInt(log.Cached),
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

func (s *Store) listLogs(ctx context.Context, limit, offset int, provider, status, apiKeyID string) ([]RequestLog, int, error) {
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
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf(
		"SELECT id, timestamp, trace_id, session_id, provider, model, api_key_id, status, input_tokens, output_tokens, total_tokens, latency_ms, cost, COALESCE(request_body, ''), COALESCE(response_body, ''), decision, decision_reason, error_message, cached FROM request_logs WHERE %s ORDER BY timestamp DESC LIMIT ? OFFSET ?",
		where,
	)
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
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
			&l.LatencyMs, &l.Cost, &l.RequestBody, &l.ResponseBody, &l.Decision, &l.DecisionReason, &l.ErrorMessage, &cached); err != nil {
			return nil, 0, err
		}
		l.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		l.Cached = cached == 1
		logs = append(logs, l)
	}
	return logs, total, rows.Err()
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
	sinceStr := since.Format(time.RFC3339Nano)
	where := "timestamp >= ?"
	args := []any{sinceStr}
	if apiKeyID != "" {
		where += " AND api_key_id = ?"
		args = append(args, apiKeyID)
	}
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
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
	sinceStr := since.Format(time.RFC3339Nano)
	where := "timestamp >= ?"
	args := []any{sinceStr}
	if apiKeyID != "" {
		where += " AND api_key_id = ?"
		args = append(args, apiKeyID)
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT provider,
			COUNT(*),
			COALESCE(AVG(latency_ms), 0),
			COALESCE(SUM(cost), 0),
			COALESCE(CAST(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) AS REAL) / NULLIF(COUNT(*), 0), 0)
		FROM request_logs WHERE %s
		GROUP BY provider`, where), args...)
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

// InsertAPIKey stores an API key.
func (s *Store) InsertAPIKey(ctx context.Context, key APIKey) error {
	meta, _ := json.Marshal(key.Metadata)
	var expiresAt *string
	if key.ExpiresAt != nil {
		s := key.ExpiresAt.Format(time.RFC3339)
		expiresAt = &s
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, name, key_hash, key_prefix, created_at, expires_at, rate_limit, enabled, workspace_id, role, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			key_hash = excluded.key_hash,
			key_prefix = excluded.key_prefix,
			expires_at = excluded.expires_at,
			rate_limit = excluded.rate_limit,
			enabled = excluded.enabled,
			workspace_id = excluded.workspace_id,
			role = excluded.role,
			metadata = excluded.metadata`,
		key.ID, key.Name, key.KeyHash, key.KeyPrefix,
		key.CreatedAt.Format(time.RFC3339), expiresAt,
		key.RateLimit, boolToInt(key.Enabled), key.WorkspaceID, key.Role, string(meta),
	)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		"UPDATE api_keys SET budget_usd = ?, spend_usd = ?, monthly_cost_budget = ?, monthly_token_budget = ? WHERE id = ?",
		firstPositive(key.BudgetUSD, key.MonthlyCostBudget), key.SpendUSD, key.MonthlyCostBudget, key.MonthlyTokenBudget, key.ID,
	)
	return err
}

// GetAPIKeyByHash looks up an API key by its SHA-256 hash.
func (s *Store) GetAPIKeyByHash(ctx context.Context, hash string) (APIKey, error) {
	var key APIKey
	var createdAt, metaStr string
	var expiresAt, workspaceID, role sql.NullString
	var enabled int

	err := s.db.QueryRowContext(ctx,
		"SELECT id, name, key_hash, key_prefix, created_at, expires_at, rate_limit, budget_usd, spend_usd, monthly_cost_budget, monthly_token_budget, enabled, workspace_id, role, metadata FROM api_keys WHERE key_hash = ?",
		hash,
	).Scan(&key.ID, &key.Name, &key.KeyHash, &key.KeyPrefix, &createdAt, &expiresAt, &key.RateLimit, &key.BudgetUSD, &key.SpendUSD, &key.MonthlyCostBudget, &key.MonthlyTokenBudget, &enabled, &workspaceID, &role, &metaStr)
	if err != nil {
		return key, err
	}

	key.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if expiresAt.Valid {
		t, _ := time.Parse(time.RFC3339, expiresAt.String)
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
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, name, key_prefix, created_at, expires_at, rate_limit, budget_usd, spend_usd, monthly_cost_budget, monthly_token_budget, enabled, workspace_id, role FROM api_keys ORDER BY created_at DESC")
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
		k.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if expiresAt.Valid {
			t, _ := time.Parse(time.RFC3339, expiresAt.String)
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
	_, err := s.db.ExecContext(ctx, "UPDATE api_keys SET enabled = 0 WHERE id = ?", id)
	return err
}

// InsertPrompt stores a prompt template.
func (s *Store) InsertPrompt(ctx context.Context, p Prompt) error {
	vars, _ := json.Marshal(p.Variables)
	meta, _ := json.Marshal(p.Metadata)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO prompts (id, name, version, template, variables, created_at, updated_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Version, p.Template, string(vars),
		p.CreatedAt.Format(time.RFC3339), p.UpdatedAt.Format(time.RFC3339), string(meta),
	)
	return err
}

// ListPrompts returns all prompts.
func (s *Store) ListPrompts(ctx context.Context) ([]Prompt, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, name, version, template, variables, created_at, updated_at FROM prompts ORDER BY updated_at DESC")
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
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		_ = json.Unmarshal([]byte(varsStr), &p.Variables)
		prompts = append(prompts, p)
	}
	return prompts, rows.Err()
}

func (s *Store) ListPromptVersions(ctx context.Context, name string) ([]Prompt, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, name, version, template, variables, created_at, updated_at FROM prompts WHERE name = ? ORDER BY version DESC",
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
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		_ = json.Unmarshal([]byte(varsStr), &p.Variables)
		prompts = append(prompts, p)
	}
	return prompts, rows.Err()
}

func (s *Store) NextPromptVersion(ctx context.Context, name string) (int, error) {
	var latest int
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM prompts WHERE name = ?", name).Scan(&latest)
	if err != nil {
		return 0, err
	}
	return latest + 1, nil
}

// GetPrompt returns a specific prompt by ID.
func (s *Store) GetPrompt(ctx context.Context, id string) (Prompt, error) {
	var p Prompt
	var createdAt, updatedAt, varsStr string
	err := s.db.QueryRowContext(ctx,
		"SELECT id, name, version, template, variables, created_at, updated_at FROM prompts WHERE id = ?", id,
	).Scan(&p.ID, &p.Name, &p.Version, &p.Template, &varsStr, &createdAt, &updatedAt)
	if err != nil {
		return p, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	_ = json.Unmarshal([]byte(varsStr), &p.Variables)
	return p, nil
}

// InsertGuardrailEvent stores a guardrail event.
func (s *Store) InsertGuardrailEvent(ctx context.Context, e GuardrailEvent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO guardrail_events (id, request_id, timestamp, guardrail_type, action, matched, details)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.RequestID, e.Timestamp.Format(time.RFC3339Nano),
		e.Type, e.Action, boolToInt(e.Matched), e.Details,
	)
	return err
}

func (s *Store) GetAPIKeyByID(ctx context.Context, id string) (APIKey, error) {
	var key APIKey
	var createdAt, metaStr string
	var expiresAt, workspaceID, role sql.NullString
	var enabled int

	err := s.db.QueryRowContext(ctx,
		"SELECT id, name, key_hash, key_prefix, created_at, expires_at, rate_limit, budget_usd, spend_usd, monthly_cost_budget, monthly_token_budget, enabled, workspace_id, role, metadata FROM api_keys WHERE id = ?",
		id,
	).Scan(&key.ID, &key.Name, &key.KeyHash, &key.KeyPrefix, &createdAt, &expiresAt, &key.RateLimit, &key.BudgetUSD, &key.SpendUSD, &key.MonthlyCostBudget, &key.MonthlyTokenBudget, &enabled, &workspaceID, &role, &metaStr)
	if err != nil {
		return key, err
	}

	key.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if expiresAt.Valid {
		t, _ := time.Parse(time.RFC3339, expiresAt.String)
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
	_, err := s.db.ExecContext(ctx, "UPDATE api_keys SET spend_usd = spend_usd + ? WHERE id = ?", amount, id)
	return err
}

func (s *Store) GetBudgetUsage(ctx context.Context, apiKeyID string, since time.Time) (BudgetUsage, error) {
	var usage BudgetUsage
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(cost), 0), COALESCE(SUM(total_tokens), 0)
		FROM request_logs
		WHERE api_key_id = ? AND timestamp >= ? AND decision = 'ALLOW'`,
		apiKeyID,
		since.Format(time.RFC3339Nano),
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
		value := entry.ExpiresAt.Format(time.RFC3339Nano)
		expiresAt = &value
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO semantic_cache (id, cache_key, created_at, expires_at, model, prompt_text, vector_json, response_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID,
		entry.Key,
		entry.CreatedAt.Format(time.RFC3339Nano),
		expiresAt,
		entry.Model,
		entry.Prompt,
		string(vectorJSON),
		string(responseJSON),
	)
	return err
}

func (s *Store) GetExactCache(ctx context.Context, key string) (CacheEntry, bool, error) {
	var entry CacheEntry
	var createdAt, expiresAt, vectorJSON, responseJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, cache_key, created_at, COALESCE(expires_at, ''), model, prompt_text, vector_json, response_json
		FROM semantic_cache
		WHERE cache_key = ? AND (expires_at IS NULL OR expires_at = '' OR expires_at > ?)
		LIMIT 1`,
		key,
		time.Now().UTC().Format(time.RFC3339Nano),
	).Scan(&entry.ID, &entry.Key, &createdAt, &expiresAt, &entry.Model, &entry.Prompt, &vectorJSON, &responseJSON)
	if err == sql.ErrNoRows {
		return CacheEntry{}, false, nil
	}
	if err != nil {
		return CacheEntry{}, false, err
	}
	entry.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if expiresAt != "" {
		parsed, _ := time.Parse(time.RFC3339Nano, expiresAt)
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
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, cache_key, created_at, COALESCE(expires_at, ''), model, prompt_text, vector_json, response_json
		FROM semantic_cache
		WHERE model = ? AND (expires_at IS NULL OR expires_at = '' OR expires_at > ?)
		ORDER BY created_at DESC LIMIT 250`,
		model,
		time.Now().UTC().Format(time.RFC3339Nano),
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
		entry.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		if expiresAt != "" {
			parsed, _ := time.Parse(time.RFC3339Nano, expiresAt)
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

func (s *Store) InsertShadowLog(ctx context.Context, entry ShadowLog) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO shadow_requests (id, request_id, trace_id, timestamp, primary_provider, shadow_provider, model, latency_ms, cost, status, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID,
		entry.RequestID,
		entry.TraceID,
		entry.Timestamp.Format(time.RFC3339Nano),
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
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, request_id, trace_id, timestamp, primary_provider, shadow_provider, model, latency_ms, cost, status, error_message
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
		entry.Timestamp, _ = time.Parse(time.RFC3339Nano, timestamp)
		logs = append(logs, entry)
	}
	return logs, rows.Err()
}

func (s *Store) InsertFeedback(ctx context.Context, entry Feedback) error {
	metadata, _ := json.Marshal(entry.Metadata)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO feedback (id, request_id, trace_id, timestamp, score, rating, comment, user_id, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID,
		entry.RequestID,
		entry.TraceID,
		entry.Timestamp.Format(time.RFC3339Nano),
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

	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, request_id, trace_id, timestamp, score, rating, comment, user_id, metadata
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
		entry.Timestamp, _ = time.Parse(time.RFC3339Nano, timestamp)
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO organizations (id, name, created_at, metadata)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, metadata = excluded.metadata`,
		org.ID,
		org.Name,
		org.CreatedAt.Format(time.RFC3339Nano),
		string(metadata),
	)
	return err
}

func (s *Store) ListOrganizations(ctx context.Context) ([]Organization, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, name, created_at, metadata FROM organizations ORDER BY created_at DESC")
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
		org.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspaces (id, organization_id, name, created_at, metadata)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			organization_id = excluded.organization_id,
			name = excluded.name,
			metadata = excluded.metadata`,
		workspace.ID,
		workspace.OrganizationID,
		workspace.Name,
		workspace.CreatedAt.Format(time.RFC3339Nano),
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
	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf("SELECT id, organization_id, name, created_at, metadata FROM workspaces WHERE %s ORDER BY created_at DESC", where),
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
		workspace.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
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
	err := s.db.QueryRowContext(ctx,
		"SELECT id, organization_id, name, created_at, metadata FROM workspaces WHERE id = ?", id,
	).Scan(&workspace.ID, &workspace.OrganizationID, &workspace.Name, &createdAt, &metadata)
	if err != nil {
		return workspace, err
	}
	workspace.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	_ = json.Unmarshal([]byte(metadata), &workspace.Metadata)
	return workspace, nil
}

func (s *Store) InsertUser(ctx context.Context, user User) error {
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}
	metadata, _ := json.Marshal(user.Metadata)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, email, name, created_at, metadata)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET name = excluded.name, metadata = excluded.metadata`,
		user.ID,
		user.Email,
		user.Name,
		user.CreatedAt.Format(time.RFC3339Nano),
		string(metadata),
	)
	return err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, email, COALESCE(name, ''), created_at, metadata FROM users ORDER BY created_at DESC")
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
		user.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		_ = json.Unmarshal([]byte(metadata), &user.Metadata)
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) UpsertWorkspaceMember(ctx context.Context, member WorkspaceMember) error {
	if member.CreatedAt.IsZero() {
		member.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(workspace_id, user_id) DO UPDATE SET role = excluded.role`,
		member.WorkspaceID,
		member.UserID,
		member.Role,
		member.CreatedAt.Format(time.RFC3339Nano),
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
	rows, err := s.db.QueryContext(ctx,
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
		member.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
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
	_, err := s.db.ExecContext(ctx,
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
		key.CreatedAt.Format(time.RFC3339Nano),
		key.UpdatedAt.Format(time.RFC3339Nano),
		boolToInt(key.Enabled),
		key.MetadataJSON,
	)
	return err
}

func (s *Store) GetVirtualKey(ctx context.Context, name string) (VirtualKeyRecord, error) {
	var record VirtualKeyRecord
	var createdAt, updatedAt string
	var enabled int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, provider_type, COALESCE(provider_name, ''), COALESCE(base_url, ''), encrypted_key,
			created_at, updated_at, enabled, COALESCE(metadata, '{}')
		FROM virtual_keys WHERE name = ?`,
		name,
	).Scan(&record.ID, &record.Name, &record.ProviderType, &record.ProviderName, &record.BaseURL,
		&record.EncryptedKey, &createdAt, &updatedAt, &enabled, &record.MetadataJSON)
	if err != nil {
		return record, err
	}
	record.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	record.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	record.Enabled = enabled == 1
	return record, nil
}

func (s *Store) ListVirtualKeys(ctx context.Context) ([]VirtualKeyRecord, error) {
	rows, err := s.db.QueryContext(ctx,
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
		record.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		record.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		record.Enabled = enabled == 1
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) RotateVirtualKey(ctx context.Context, name, encryptedKey string) error {
	result, err := s.db.ExecContext(ctx,
		"UPDATE virtual_keys SET encrypted_key = ?, updated_at = ? WHERE name = ?",
		encryptedKey,
		time.Now().UTC().Format(time.RFC3339Nano),
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
	result, err := s.db.ExecContext(ctx,
		"UPDATE virtual_keys SET enabled = 0, updated_at = ? WHERE name = ?",
		time.Now().UTC().Format(time.RFC3339Nano),
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
	return s.db.Close()
}

func (s *Store) ensureColumn(table, column, definition string) error {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
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
