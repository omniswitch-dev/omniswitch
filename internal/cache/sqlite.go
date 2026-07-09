package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sentinel/internal/provider"

	_ "modernc.org/sqlite"
)

type SQLiteCache struct {
	db  *sql.DB
	ttl time.Duration
}

func NewSQLiteCache(dir string, ttl time.Duration) (*SQLiteCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache directory: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "sentinel_cache.db"))
	if err != nil {
		return nil, fmt.Errorf("open cache database: %w", err)
	}
	cache := &SQLiteCache{db: db, ttl: ttl}
	if err := cache.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return cache, nil
}

func (c *SQLiteCache) Get(key string) (*provider.ChatResponse, bool, error) {
	return c.GetContext(context.Background(), key)
}

func (c *SQLiteCache) Set(key string, val *provider.ChatResponse) error {
	return c.SetContext(context.Background(), key, val)
}

func (c *SQLiteCache) GetContext(ctx context.Context, key string) (*provider.ChatResponse, bool, error) {
	var payload string
	err := c.db.QueryRowContext(ctx,
		"SELECT response_json FROM cache_entries WHERE cache_key = ? AND expires_at > ?",
		key,
		time.Now().UTC().Format(time.RFC3339Nano),
	).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var response provider.ChatResponse
	if err := json.Unmarshal([]byte(payload), &response); err != nil {
		return nil, false, err
	}
	return &response, true, nil
}

func (c *SQLiteCache) SetContext(ctx context.Context, key string, val *provider.ChatResponse) error {
	if val == nil {
		return nil
	}
	payload, err := json.Marshal(val)
	if err != nil {
		return err
	}
	ttl := c.ttl
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	_, err = c.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO cache_entries (cache_key, response_json, created_at, expires_at)
		VALUES (?, ?, ?, ?)`,
		key,
		string(payload),
		time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Add(ttl).Format(time.RFC3339Nano),
	)
	return err
}

func (c *SQLiteCache) Close() error {
	return c.db.Close()
}

func (c *SQLiteCache) migrate() error {
	_, err := c.db.Exec(`CREATE TABLE IF NOT EXISTS cache_entries (
		cache_key TEXT PRIMARY KEY,
		response_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	)`)
	return err
}
