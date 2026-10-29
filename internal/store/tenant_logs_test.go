package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// seedTenantLogs writes one log per tenant: workspace w1 and w2 in
// organization o1, workspace w3 in organization o2, and a legacy row with
// no tenant. Every log shares trace "shared" and has its own trace too.
func seedTenantLogs(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	rows := []RequestLog{
		{ID: "r1", TraceID: "shared", WorkspaceID: "w1", OrganizationID: "o1", APIKeyID: "k1", Cost: 1},
		{ID: "r2", TraceID: "shared", WorkspaceID: "w2", OrganizationID: "o1", APIKeyID: "k2", Cost: 2},
		{ID: "r3", TraceID: "shared", WorkspaceID: "w3", OrganizationID: "o2", APIKeyID: "k3", Cost: 4},
		{ID: "r4", TraceID: "shared", APIKeyID: "k4", Cost: 8},
	}
	for i, row := range rows {
		row.Timestamp = now.Add(time.Duration(i) * time.Millisecond)
		row.Provider, row.Model, row.Status, row.Decision = "p-"+row.ID, "m", "success", "ALLOW"
		if err := st.InsertLog(ctx, row); err != nil {
			t.Fatalf("InsertLog(%s) error = %v", row.ID, err)
		}
		if err := st.InsertShadowLog(ctx, ShadowLog{ID: "s-" + row.ID, RequestID: row.ID, TraceID: "shared", Timestamp: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertGuardrailEvent(ctx, GuardrailEvent{ID: "g-" + row.ID, RequestID: row.ID, Timestamp: now, Matched: true}); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertFeedback(ctx, Feedback{ID: "f-" + row.ID, RequestID: row.ID, TraceID: "shared", Timestamp: now,
			WorkspaceID: row.WorkspaceID, OrganizationID: row.OrganizationID}); err != nil {
			t.Fatal(err)
		}
	}
}

func tenantLogIDs(logs []RequestLog) []string {
	ids := make([]string, 0, len(logs))
	for _, l := range logs {
		ids = append(ids, l.ID)
	}
	sort.Strings(ids)
	return ids
}

func equalIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestTenantScopedLogReads(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		seedTenantLogs(t, st)
		ctx := context.Background()
		since := time.Now().Add(-time.Hour)
		cases := []struct {
			name     string
			scope    LogScope
			want     []string
			wantCost float64
		}{
			{"global", LogScope{}, []string{"r1", "r2", "r3", "r4"}, 15},
			{"workspace", LogScope{WorkspaceID: "w1", OrganizationID: "o1"}, []string{"r1"}, 1},
			{"organization", LogScope{OrganizationID: "o1"}, []string{"r1", "r2"}, 3},
			{"other organization", LogScope{OrganizationID: "o2"}, []string{"r3"}, 4},
			{"workspace and key", LogScope{WorkspaceID: "w1", APIKeyID: "k2"}, nil, 0},
			{"unknown workspace", LogScope{WorkspaceID: "nope"}, nil, 0},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				logs, total, err := st.ListLogsInScope(ctx, tc.scope, 10, 0, "", "")
				if err != nil {
					t.Fatal(err)
				}
				if got := tenantLogIDs(logs); !equalIDs(got, tc.want...) || total != len(tc.want) {
					t.Fatalf("ListLogsInScope() = %v (total %d), want %v", got, total, tc.want)
				}
				m, err := st.GetMetricsInScope(ctx, since, tc.scope)
				if err != nil {
					t.Fatal(err)
				}
				if m.TotalRequests != len(tc.want) || m.TotalCost != tc.wantCost {
					t.Fatalf("GetMetricsInScope() = %d requests, cost %v; want %d, %v", m.TotalRequests, m.TotalCost, len(tc.want), tc.wantCost)
				}
				pm, err := st.GetProviderMetricsInScope(ctx, since, tc.scope)
				if err != nil {
					t.Fatal(err)
				}
				if len(pm) != len(tc.want) {
					t.Fatalf("GetProviderMetricsInScope() = %v, want %d providers", pm, len(tc.want))
				}
				rows, err := st.GetCostAnalyticsInScope(ctx, since, "provider", tc.scope)
				if err != nil {
					t.Fatal(err)
				}
				var cost float64
				for _, row := range rows {
					cost += row.TotalCost
				}
				if len(rows) != len(tc.want) || cost != tc.wantCost {
					t.Fatalf("GetCostAnalyticsInScope() = %v, want %d rows costing %v", rows, len(tc.want), tc.wantCost)
				}

				detail, err := st.GetTraceInScope(ctx, "shared", tc.scope)
				if len(tc.want) == 0 {
					if !errors.Is(err, sql.ErrNoRows) {
						t.Fatalf("GetTraceInScope() error = %v, want sql.ErrNoRows", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := tenantLogIDs(detail.Logs); !equalIDs(got, tc.want...) {
					t.Fatalf("trace logs = %v, want %v", got, tc.want)
				}
				// Shadow requests and guardrail events follow the visible logs;
				// feedback follows the tenant.
				if len(detail.ShadowLogs) != len(tc.want) || len(detail.GuardrailEvents) != len(tc.want) || len(detail.Feedback) != len(tc.want) {
					t.Fatalf("trace has %d shadow, %d guardrail, %d feedback; want %d each",
						len(detail.ShadowLogs), len(detail.GuardrailEvents), len(detail.Feedback), len(tc.want))
				}
				for _, s := range detail.ShadowLogs {
					if !contains(tc.want, s.RequestID) {
						t.Fatalf("shadow log of %s leaked into %v", s.RequestID, tc.want)
					}
				}
				for _, f := range detail.Feedback {
					if !contains(tc.want, f.RequestID) {
						t.Fatalf("feedback on %s leaked into %v", f.RequestID, tc.want)
					}
				}
			})
		}
	})
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func TestTenantScopedFeedbackAndReferences(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		seedTenantLogs(t, st)
		ctx := context.Background()
		fb, err := st.ListFeedbackInScope(ctx, LogScope{WorkspaceID: "w2", OrganizationID: "o1"}, 10, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(fb) != 1 || fb[0].ID != "f-r2" || fb[0].WorkspaceID != "w2" || fb[0].OrganizationID != "o1" {
			t.Fatalf("workspace feedback = %+v, want f-r2 with its tenant", fb)
		}
		if fb, err = st.ListFeedbackInScope(ctx, LogScope{OrganizationID: "o1"}, 10, "", ""); err != nil || len(fb) != 2 {
			t.Fatalf("organization feedback = %v, %v; want 2", fb, err)
		}
		// The API key part of a scope does not apply to feedback.
		if fb, err = st.ListFeedbackInScope(ctx, LogScope{APIKeyID: "k1"}, 10, "", ""); err != nil || len(fb) != 4 {
			t.Fatalf("global feedback = %v, %v; want 4", fb, err)
		}
		if fb, err = st.ListFeedbackInScope(ctx, LogScope{WorkspaceID: "w1"}, 10, "r2", ""); err != nil || len(fb) != 0 {
			t.Fatalf("feedback of another workspace's request = %v, %v; want none", fb, err)
		}

		for _, tc := range []struct {
			id    string
			scope LogScope
			want  bool
		}{
			{"r1", LogScope{WorkspaceID: "w1"}, true},
			{"r2", LogScope{WorkspaceID: "w1"}, false},
			{"r2", LogScope{OrganizationID: "o1"}, true},
			{"r3", LogScope{OrganizationID: "o1"}, false},
			{"r4", LogScope{OrganizationID: "o1"}, false},
			{"r4", LogScope{}, true},
			{"missing", LogScope{}, false},
		} {
			got, err := st.RequestLogInScope(ctx, tc.id, tc.scope)
			if err != nil || got != tc.want {
				t.Errorf("RequestLogInScope(%s, %+v) = %v, %v; want %v", tc.id, tc.scope, got, err, tc.want)
			}
		}
		if ok, err := st.TraceInScope(ctx, "shared", LogScope{WorkspaceID: "w3"}); err != nil || !ok {
			t.Fatalf("TraceInScope(shared, w3) = %v, %v; want true", ok, err)
		}
		if ok, err := st.TraceInScope(ctx, "shared", LogScope{WorkspaceID: "w9"}); err != nil || ok {
			t.Fatalf("TraceInScope(shared, w9) = %v, %v; want false", ok, err)
		}
	})
}

// legacyTenantTables is what a release before the tenant columns left
// behind: request_logs and feedback without workspace_id / organization_id.
var legacyTenantTables = []string{
	`CREATE TABLE request_logs (id TEXT PRIMARY KEY, timestamp TEXT NOT NULL, trace_id TEXT, session_id TEXT, provider TEXT, model TEXT,
		api_key_id TEXT, status TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0,
		latency_ms DOUBLE PRECISION DEFAULT 0, cost DOUBLE PRECISION DEFAULT 0, request_body TEXT, response_body TEXT, decision TEXT,
		decision_reason TEXT, error_message TEXT, cached INTEGER DEFAULT 0, metadata TEXT)`,
	`CREATE TABLE feedback (id TEXT PRIMARY KEY, request_id TEXT, trace_id TEXT, timestamp TEXT NOT NULL, score INTEGER DEFAULT 0,
		rating TEXT, comment TEXT, user_id TEXT, metadata TEXT)`,
	`INSERT INTO request_logs (id, timestamp, trace_id, provider, api_key_id, status, cost) VALUES ('old', '2026-01-01T00:00:00Z', 'tr-old', 'p', 'k', 'success', 1)`,
	`INSERT INTO feedback (id, request_id, trace_id, timestamp, score) VALUES ('fb-old', 'old', 'tr-old', '2026-01-01T00:00:00Z', 1)`,
}

// checkTenantUpgrade verifies an upgraded store: old rows have no tenant,
// so only global callers see them, and new rows carry their tenant.
func checkTenantUpgrade(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	logs, total, err := st.ListLogsInScope(ctx, LogScope{}, 10, 0, "", "")
	if err != nil || total != 1 || logs[0].ID != "old" || logs[0].WorkspaceID != "" || logs[0].OrganizationID != "" {
		t.Fatalf("global ListLogsInScope() = %+v (total %d), %v; want the legacy row without a tenant", logs, total, err)
	}
	for _, scope := range []LogScope{{WorkspaceID: "w1"}, {OrganizationID: "o1"}} {
		if _, total, err := st.ListLogsInScope(ctx, scope, 10, 0, "", ""); err != nil || total != 0 {
			t.Fatalf("ListLogsInScope(%+v) total = %d, %v; legacy rows must stay global-only", scope, total, err)
		}
		if _, err := st.GetTraceInScope(ctx, "tr-old", scope); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("GetTraceInScope(%+v) error = %v, want sql.ErrNoRows", scope, err)
		}
		if fb, err := st.ListFeedbackInScope(ctx, scope, 10, "", ""); err != nil || len(fb) != 0 {
			t.Fatalf("ListFeedbackInScope(%+v) = %v, %v; want none", scope, fb, err)
		}
	}
	if fb, err := st.ListFeedbackInScope(ctx, LogScope{}, 10, "", ""); err != nil || len(fb) != 1 || fb[0].ID != "fb-old" {
		t.Fatalf("global ListFeedbackInScope() = %v, %v; want the legacy row", fb, err)
	}
	if err := st.InsertLog(ctx, RequestLog{ID: "new", Timestamp: time.Now(), WorkspaceID: "w1", OrganizationID: "o1"}); err != nil {
		t.Fatal(err)
	}
	if logs, _, err := st.ListLogsInScope(ctx, LogScope{OrganizationID: "o1"}, 10, 0, "", ""); err != nil || len(logs) != 1 || logs[0].ID != "new" {
		t.Fatalf("organization ListLogsInScope() = %+v, %v; want the new row", logs, err)
	}
}

func TestMigrateAddsTenantColumnsSQLite(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "sentinel.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range legacyTenantTables {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	db.Close()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() on legacy database error = %v", err)
	}
	defer st.Close()
	checkTenantUpgrade(t, st)
}

func TestMigrateAddsTenantColumnsPostgres(t *testing.T) {
	dsn := newPostgresTestDSN(t)
	db := openPostgresDB(t, dsn)
	for _, stmt := range legacyTenantTables {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres() on legacy schema error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkTenantUpgrade(t, st)
	for _, column := range []string{"workspace_id", "organization_id"} {
		for _, table := range []string{"request_logs", "feedback"} {
			if got := columnType(t, db, table, column); got != "text" {
				t.Fatalf("%s.%s type = %q, want text", table, column, got)
			}
		}
	}
	for _, name := range []string{"idx_logs_workspace_time", "idx_logs_org_time", "idx_feedback_workspace_time", "idx_feedback_org_time"} {
		if exists, valid := indexState(t, db, name); !exists || !valid {
			t.Fatalf("index %s exists=%v valid=%v, want a valid index", name, exists, valid)
		}
	}
}
