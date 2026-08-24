package store

import (
	"context"
	"testing"
	"time"
)

func TestRewriteSQL(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"simple", "SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = $1 AND b = $2"},
		{"no placeholders", "SELECT 1", "SELECT 1"},
		{
			"string literal preserved",
			"INSERT INTO t (a, b) VALUES (?, 'what?') RETURNING ?",
			"INSERT INTO t (a, b) VALUES ($1, 'what?') RETURNING $2",
		},
		{"escaped quote", `SELECT * FROM t WHERE c = 'it''s ?' AND d = ?`, `SELECT * FROM t WHERE c = 'it''s ?' AND d = $1`},
		{"on conflict", "UPDATE t SET x = ? WHERE y = ?", "UPDATE t SET x = $1 WHERE y = $2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rewriteSQL(tc.in); got != tc.want {
				t.Fatalf("rewriteSQL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSQLitePathStillWorks exercises the full store against SQLite to prove
// the exec/query wrappers did not regress the default driver.
func TestSQLitePathStillWorks(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	log := RequestLog{ID: "req-1", Timestamp: time.Now().UTC(), Provider: "openai", Model: "gpt-4o-mini", Status: "success"}
	if err := st.InsertLog(ctx, log); err != nil {
		t.Fatalf("insert log: %v", err)
	}
	logs, total, err := st.ListLogs(ctx, 10, 0, "", "")
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if total != 1 || len(logs) != 1 || logs[0].ID != "req-1" {
		t.Fatalf("unexpected logs: total=%d n=%d", total, len(logs))
	}
}
