package store

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrentWritesDoNotFail is the STORE-01 regression test: before the
// fix, 1596 of 1600 SQLite writes failed with SQLITE_BUSY and spend was lost.
func TestConcurrentWritesDoNotFail(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		runConcurrentWrites(t, st, st)
	})
}

// TestConcurrentWritesAcrossStores opens two Store values on the same SQLite
// file, as two gateway processes would. Their writers can only coordinate
// through SQLite's own locking, so this covers busy_timeout on every pooled
// connection.
func TestConcurrentWritesAcrossStores(t *testing.T) {
	dir := t.TempDir()
	a, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer a.Close()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New() second store error = %v", err)
	}
	defer b.Close()
	runConcurrentWrites(t, a, b)
}

func runConcurrentWrites(t *testing.T, a, b *Store) {
	t.Helper()
	ctx := context.Background()
	if err := a.InsertAPIKey(ctx, APIKey{ID: "k1", Name: "k", KeyHash: "h1", KeyPrefix: "p", CreatedAt: time.Now(), Enabled: true}); err != nil {
		t.Fatalf("InsertAPIKey() error = %v", err)
	}
	const goroutines, iterations = 32, 25
	var wg sync.WaitGroup
	var fails atomic.Int64
	var first atomic.Value
	for g := 0; g < goroutines; g++ {
		st := a
		if g%2 == 1 {
			st = b
		}
		wg.Add(1)
		go func(g int, st *Store) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				err := st.InsertLog(ctx, RequestLog{ID: fmt.Sprintf("r%d-%d", g, i), Timestamp: time.Now().UTC(), APIKeyID: "k1", Cost: 0.01, Decision: "ALLOW", Status: "success"})
				if err == nil {
					err = st.IncrementAPIKeySpend(ctx, "k1", 0.01)
				}
				if err == nil {
					_, err = st.GetBudgetUsage(ctx, "k1", time.Now().Add(-time.Hour))
				}
				if err != nil {
					fails.Add(1)
					first.CompareAndSwap(nil, err.Error())
				}
			}
		}(g, st)
	}
	wg.Wait()
	if n := fails.Load(); n != 0 {
		t.Fatalf("%d of %d iterations failed; first error: %v", n, goroutines*iterations, first.Load())
	}
	key, err := a.GetAPIKeyByID(ctx, "k1")
	if err != nil {
		t.Fatalf("GetAPIKeyByID() error = %v", err)
	}
	want := float64(goroutines*iterations) * 0.01
	if math.Abs(key.SpendUSD-want) > 1e-6 {
		t.Fatalf("spend_usd = %.10f, want %.2f", key.SpendUSD, want)
	}
	_, total, err := a.ListLogs(ctx, 1, 0, "", "")
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	if total != goroutines*iterations {
		t.Fatalf("logged %d requests, want %d", total, goroutines*iterations)
	}
}

// TestSQLitePragmasApplyToEveryConnection checks that the read pool's
// connections, not only the first one, carry busy_timeout and WAL.
func TestSQLitePragmasApplyToEveryConnection(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	conns := make([]interface{ Close() error }, 0, 3)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i := 0; i < 3; i++ {
		conn, err := st.db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn() error = %v", err)
		}
		conns = append(conns, conn)
		var timeout int
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatalf("PRAGMA busy_timeout: %v", err)
		}
		if timeout != sqliteBusyTimeoutMs {
			t.Fatalf("connection %d busy_timeout = %d, want %d", i, timeout, sqliteBusyTimeoutMs)
		}
		var mode string
		if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatalf("PRAGMA journal_mode: %v", err)
		}
		if mode != "wal" {
			t.Fatalf("connection %d journal_mode = %q, want wal", i, mode)
		}
		var sync int
		if err := conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
			t.Fatalf("PRAGMA synchronous: %v", err)
		}
		if sync != 1 { // NORMAL
			t.Fatalf("connection %d synchronous = %d, want 1 (NORMAL)", i, sync)
		}
	}
}
