package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

func cacheEntry(id, key string, created time.Time, expires *time.Time, respID string) CacheEntry {
	return CacheEntry{
		ID:        id,
		Key:       key,
		CreatedAt: created,
		ExpiresAt: expires,
		Model:     "scope",
		Prompt:    "hello",
		Vector:    map[string]float64{"hello": 1},
		Response:  provider.ChatResponse{ID: respID},
	}
}

// TestExactCacheRefreshesAfterExpiry covers STORE-03: re-caching a key whose
// entry had expired failed with a UNIQUE violation on cache_key, so the
// prompt could never be cached again.
func TestExactCacheRefreshesAfterExpiry(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		now := time.Now().UTC()
		past := now.Add(-time.Minute)
		future := now.Add(time.Hour)
		if err := st.InsertSemanticCache(ctx, cacheEntry("c1", "K", now.Add(-time.Hour), &past, "old")); err != nil {
			t.Fatalf("insert expired: %v", err)
		}
		if _, ok, err := st.GetExactCache(ctx, "K"); err != nil || ok {
			t.Fatalf("GetExactCache(expired) ok=%v err=%v, want miss", ok, err)
		}
		if err := st.InsertSemanticCache(ctx, cacheEntry("c2", "K", now, &future, "new")); err != nil {
			t.Fatalf("re-insert after expiry: %v", err)
		}
		entry, ok, err := st.GetExactCache(ctx, "K")
		if err != nil || !ok || entry.Response.ID != "new" {
			t.Fatalf("GetExactCache() = %+v ok=%v err=%v, want refreshed entry", entry.Response, ok, err)
		}
		var rows int
		if err := st.queryRow(ctx, "SELECT COUNT(*) FROM semantic_cache WHERE cache_key = ?", "K").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Fatalf("rows for key = %d, want 1", rows)
		}
		// Entries without an exact key (semantic only) still insert by id.
		for i := 0; i < 2; i++ {
			if err := st.InsertSemanticCache(ctx, cacheEntry(fmt.Sprintf("s%d", i), "", now, nil, "sem")); err != nil {
				t.Fatalf("insert keyless entry %d: %v", i, err)
			}
		}
	})
}

func TestPurgeExpiredCache(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		now := time.Now().UTC()
		past := now.Add(-time.Minute)
		future := now.Add(time.Hour)
		for i := 0; i < purgeBatchSize+5; i++ {
			if err := st.InsertSemanticCache(ctx, cacheEntry(fmt.Sprintf("e%d", i), fmt.Sprintf("ek%d", i), now, &past, "x")); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.InsertSemanticCache(ctx, cacheEntry("live", "lk", now, &future, "live")); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertSemanticCache(ctx, cacheEntry("forever", "fk", now, nil, "forever")); err != nil {
			t.Fatal(err)
		}
		n, err := st.PurgeExpiredCache(ctx, now)
		if err != nil {
			t.Fatalf("PurgeExpiredCache() error = %v", err)
		}
		if n != purgeBatchSize+5 {
			t.Fatalf("PurgeExpiredCache() removed %d, want %d", n, purgeBatchSize+5)
		}
		var left int
		if err := st.queryRow(ctx, "SELECT COUNT(*) FROM semantic_cache").Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 2 {
			t.Fatalf("remaining cache rows = %d, want 2", left)
		}
	})
}

func TestPurgeRecordsBefore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		cutoff := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		old, recent := cutoff.Add(-time.Nanosecond), cutoff
		for i, at := range []time.Time{old, old, recent} {
			id := fmt.Sprintf("r%d", i)
			if err := st.InsertLog(ctx, RequestLog{ID: id, Timestamp: at}); err != nil {
				t.Fatal(err)
			}
			if err := st.InsertGuardrailEvent(ctx, GuardrailEvent{ID: "g" + id, RequestID: id, Timestamp: at}); err != nil {
				t.Fatal(err)
			}
			if err := st.InsertShadowLog(ctx, ShadowLog{ID: "s" + id, RequestID: id, Timestamp: at}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := st.PurgeRecordsBefore(ctx, cutoff)
		if err != nil {
			t.Fatalf("PurgeRecordsBefore() error = %v", err)
		}
		if got != (RetentionResult{RequestLogs: 2, GuardrailEvents: 2, ShadowRequests: 2}) {
			t.Fatalf("PurgeRecordsBefore() = %+v, want 2 of each", got)
		}
		logs, total, err := st.ListLogs(ctx, 10, 0, "", "")
		if err != nil || total != 1 || logs[0].ID != "r2" {
			t.Fatalf("remaining logs = %v total=%d err=%v, want only r2", logIDs(logs), total, err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := st.PurgeRecordsBefore(canceled, cutoff); err == nil {
			t.Fatal("PurgeRecordsBefore(canceled ctx) error = nil")
		}
	})
}

// TestHotQueriesUseCompositeIndexes covers the index half of STORE-05: the
// budget query and the semantic-cache scan used single-column indexes and a
// temp B-tree sort over the full history.
func TestHotQueriesUseCompositeIndexes(t *testing.T) {
	st := newTestStore(t)
	cases := []struct {
		name, query, wantIndex string
		args                   []any
	}{
		{"budget", `SELECT COALESCE(SUM(cost), 0) FROM request_logs WHERE api_key_id = ? AND timestamp >= ? AND decision = 'ALLOW'`, "idx_logs_api_key_time", []any{"k", "2026"}},
		{"semantic cache", `SELECT id FROM semantic_cache WHERE model = ? AND (expires_at IS NULL OR expires_at = '' OR expires_at > ?) ORDER BY created_at DESC LIMIT 250`, "idx_cache_model_created", []any{"m", "2026"}},
		{"scoped logs", `SELECT id FROM request_logs WHERE api_key_id = ? ORDER BY timestamp DESC LIMIT 10`, "idx_logs_api_key_time", []any{"k"}},
	}
	for _, tc := range cases {
		rows, err := st.db.Query("EXPLAIN QUERY PLAN "+tc.query, tc.args...)
		if err != nil {
			t.Fatalf("%s: explain: %v", tc.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		joined := strings.Join(plan, "; ")
		if !strings.Contains(joined, tc.wantIndex) || strings.Contains(joined, "TEMP B-TREE") {
			t.Fatalf("%s plan = %q, want %s without a temp B-tree", tc.name, joined, tc.wantIndex)
		}
	}
}
