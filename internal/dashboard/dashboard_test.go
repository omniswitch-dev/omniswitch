package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestDashboardAPIRoutes(t *testing.T) {
	st := newDashboardTestStore(t)
	if err := st.InsertLog(context.Background(), store.RequestLog{
		ID:          "req_1",
		Timestamp:   time.Now().UTC(),
		TraceID:     "trace_1",
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
		{name: "trace detail", path: "/api/traces/trace_1", wantStatus: http.StatusOK, wantBody: `"trace_id":"trace_1"`},
		{name: "health", path: "/api/health", wantStatus: http.StatusOK, wantBody: `"status":"healthy"`},
		{name: "static dashboard", path: "/", wantStatus: http.StatusOK, wantBody: "<title>OmniSwitch"},
		{name: "playground route", path: "/playground", wantStatus: http.StatusOK, wantBody: `id="playground"`},
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

func TestDashboardConfigTogglePreservesBody(t *testing.T) {
	st := newDashboardTestStore(t)
	mux := http.NewServeMux()
	New(st).RegisterRoutes(mux)

	body := `apiVersion: omniswitch.dev/v1
kind: GatewayConfig
routes:
  gpt-4o-mini:
    provider: openai
`
	createReq := httptest.NewRequest(http.MethodPost, "/api/configs", strings.NewReader(`{
		"name":"production-routing",
		"description":"prod",
		"format":"yaml",
		"body":`+strconvQuote(body)+`
	}`))
	createRec := httptest.NewRecorder()
	mux.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}

	patchReq := httptest.NewRequest(http.MethodPatch, "/api/configs?name=production-routing", strings.NewReader(`{"enabled":false}`))
	patchRec := httptest.NewRecorder()
	mux.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}

	records, err := st.ListGatewayConfigs(context.Background(), true)
	if err != nil {
		t.Fatalf("ListGatewayConfigs() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records len = %d, want 1", len(records))
	}
	if records[0].Enabled {
		t.Fatalf("Enabled = true, want false")
	}
	if records[0].Body != body {
		t.Fatalf("body changed = %q, want %q", records[0].Body, body)
	}
}

func strconvQuote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(value) + `"`
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
