package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentinel/internal/store"
)

func TestDashboardAPIRoutes(t *testing.T) {
	st := newDashboardTestStore(t)
	if err := st.InsertLog(context.Background(), store.RequestLog{
		ID:          "req_1",
		Timestamp:   time.Now().UTC(),
		Provider:    "openai",
		Model:       "gpt-4o-mini",
		Status:      "success",
		Decision:    "ALLOW",
		LatencyMs:   12,
		TotalTokens: 4,
	}); err != nil {
		t.Fatalf("InsertLog() error = %v", err)
	}

	mux := http.NewServeMux()
	New(st).RegisterRoutes(mux)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{name: "logs", path: "/api/logs?limit=1", wantStatus: http.StatusOK, wantBody: `"total":1`},
		{name: "metrics", path: "/api/metrics?window=1h", wantStatus: http.StatusOK, wantBody: `"total_requests":1`},
		{name: "provider metrics", path: "/api/metrics/providers", wantStatus: http.StatusOK, wantBody: `"provider":"openai"`},
		{name: "health", path: "/api/health", wantStatus: http.StatusOK, wantBody: `"status":"healthy"`},
		{name: "static dashboard", path: "/", wantStatus: http.StatusOK, wantBody: "<title>Sentinel"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body = %q, want substring %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func newDashboardTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
