package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// postgresTestDSNEnv names an optional PostgreSQL DSN. When it is set, the
// backend matrix tests also run against PostgreSQL, each in a fresh schema.
// Without it they run against SQLite only, so `go test` needs no network.
const postgresTestDSNEnv = "OMNISWITCH_TEST_POSTGRES_DSN"

var pgSchemaSeq atomic.Int64

type backend struct {
	name string
	open func(t *testing.T) *Store
}

func testBackends(t *testing.T) []backend {
	t.Helper()
	backends := []backend{{name: "sqlite", open: newTestStore}}
	if os.Getenv(postgresTestDSNEnv) != "" {
		backends = append(backends, backend{name: "postgres", open: newPostgresTestStore})
	}
	return backends
}

// forEachBackend runs fn once per available backend with a fresh store.
func forEachBackend(t *testing.T, fn func(t *testing.T, st *Store)) {
	t.Helper()
	for _, b := range testBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			fn(t, b.open(t))
		})
	}
}

// newPostgresTestDSN creates an empty schema and returns a DSN whose
// search_path points at it. The schema is dropped when the test ends.
func newPostgresTestDSN(t *testing.T) string {
	t.Helper()
	base := os.Getenv(postgresTestDSNEnv)
	if base == "" {
		t.Skip(postgresTestDSNEnv + " is not set")
	}
	schema := fmt.Sprintf("omniswitch_test_%d_%d", time.Now().UnixNano(), pgSchemaSeq.Add(1))
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		admin.Close()
	})
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "search_path=" + schema
}

func newPostgresTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenPostgres(newPostgresTestDSN(t))
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mustExec(t *testing.T, st *Store, query string, args ...any) {
	t.Helper()
	if _, err := st.exec(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
