package store

import (
	"context"
	"testing"
	"time"
)

// TestTimestampsCompareInTimeOrder covers STORE-11. With RFC3339Nano the
// fraction is trimmed, so "00:00:00.5Z" sorted before "00:00:00Z" and a
// month-start bound skipped requests from the first second of the month.
func TestTimestampsCompareInTimeOrder(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		monthStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		entries := []struct {
			id string
			at time.Time
		}{
			{"exact", monthStart},
			{"half", monthStart.Add(500 * time.Millisecond)},
			{"one", monthStart.Add(time.Second)},
			{"before", monthStart.Add(-time.Nanosecond)},
		}
		for _, e := range entries {
			if err := st.InsertLog(ctx, RequestLog{ID: e.id, Timestamp: e.at, APIKeyID: "k", Decision: "ALLOW", Cost: 1}); err != nil {
				t.Fatal(err)
			}
		}
		usage, err := st.GetBudgetUsage(ctx, "k", monthStart)
		if err != nil {
			t.Fatal(err)
		}
		if usage.Cost != 3 {
			t.Fatalf("GetBudgetUsage(since month start) cost = %v, want 3", usage.Cost)
		}
		usage, err = st.GetBudgetUsage(ctx, "k", monthStart.Add(250*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		if usage.Cost != 2 {
			t.Fatalf("GetBudgetUsage(since +250ms) cost = %v, want 2", usage.Cost)
		}
		logs, _, err := st.ListLogs(ctx, 10, 0, "", "")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"one", "half", "exact", "before"}
		for i, id := range want {
			if logs[i].ID != id {
				t.Fatalf("ListLogs order = %v, want %v", logIDs(logs), want)
			}
		}
		if !logs[1].Timestamp.Equal(monthStart.Add(500 * time.Millisecond)) {
			t.Fatalf("timestamp round trip = %v", logs[1].Timestamp)
		}
	})
}

// TestTimestampsAreStoredInUTC checks that a non-UTC time is normalised.
func TestTimestampsAreStoredInUTC(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	loc := time.FixedZone("UTC+2", 2*60*60)
	at := time.Date(2026, 5, 1, 2, 0, 0, 0, loc)
	if err := st.InsertLog(ctx, RequestLog{ID: "tz", Timestamp: at}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := st.db.QueryRow("SELECT timestamp FROM request_logs WHERE id = 'tz'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "2026-05-01T00:00:00.000000000Z" {
		t.Fatalf("stored timestamp = %q, want fixed-width UTC", raw)
	}
}

// TestLegacyTimestampsStillParse checks rows written by older releases.
func TestLegacyTimestampsStillParse(t *testing.T) {
	for _, raw := range []string{"2026-05-01T00:00:00Z", "2026-05-01T00:00:00.5Z", "2026-05-01T02:00:00+02:00"} {
		got := parseTime(raw)
		if got.IsZero() || got.UTC().Day() != 1 || got.UTC().Hour() != 0 {
			t.Fatalf("parseTime(%q) = %v", raw, got)
		}
	}
	if got := parseTime(""); !got.IsZero() {
		t.Fatalf("parseTime(\"\") = %v, want zero", got)
	}
}

func logIDs(logs []RequestLog) []string {
	ids := make([]string, len(logs))
	for i, l := range logs {
		ids[i] = l.ID
	}
	return ids
}

// TestPaginationIsClamped covers STORE-14: a negative offset failed only on
// PostgreSQL ("OFFSET must not be negative").
func TestPaginationIsClamped(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		for i := 0; i < 3; i++ {
			if err := st.InsertLog(ctx, RequestLog{ID: string(rune('a' + i)), Timestamp: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct{ limit, offset, want int }{{10, -5, 3}, {-1, -1, 3}, {0, 0, 3}, {2, 0, 2}, {1 << 30, 1, 2}} {
			logs, total, err := st.ListLogs(ctx, tc.limit, tc.offset, "", "")
			if err != nil {
				t.Fatalf("ListLogs(%d, %d) error = %v", tc.limit, tc.offset, err)
			}
			if total != 3 || len(logs) != tc.want {
				t.Fatalf("ListLogs(%d, %d) = %d rows (total %d), want %d", tc.limit, tc.offset, len(logs), total, tc.want)
			}
			if _, _, err := st.ListLogsForAPIKey(ctx, "k", tc.limit, tc.offset, "", ""); err != nil {
				t.Fatalf("ListLogsForAPIKey(%d, %d) error = %v", tc.limit, tc.offset, err)
			}
		}
	})
}

func TestClampPage(t *testing.T) {
	cases := []struct{ limit, offset, wantLimit, wantOffset int }{
		{0, 0, DefaultPageSize, 0},
		{-3, -7, DefaultPageSize, 0},
		{25, 10, 25, 10},
		{MaxPageSize + 1, 0, MaxPageSize, 0},
	}
	for _, tc := range cases {
		l, o := ClampPage(tc.limit, tc.offset)
		if l != tc.wantLimit || o != tc.wantOffset {
			t.Fatalf("ClampPage(%d, %d) = %d, %d; want %d, %d", tc.limit, tc.offset, l, o, tc.wantLimit, tc.wantOffset)
		}
	}
}
