package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// newTenantDashboard serves the dashboard over a store holding one log per
// tenant: r1 in workspace w1 and r2 in workspace w2 (both in organization
// o1), r3 in workspace w3 of organization o2, and r0 written before the
// upgrade with no tenant. Each log has its own trace.
func newTenantDashboard(t *testing.T, metricsPublic bool) *http.ServeMux {
	t.Helper()
	st := newDashboardTestStore(t)
	now := time.Now().UTC()
	for i, row := range []store.RequestLog{
		{ID: "r0", TraceID: "t0"},
		{ID: "r1", TraceID: "t1", WorkspaceID: "w1", OrganizationID: "o1"},
		{ID: "r2", TraceID: "t2", WorkspaceID: "w2", OrganizationID: "o1"},
		{ID: "r3", TraceID: "t3", WorkspaceID: "w3", OrganizationID: "o2"},
	} {
		row.Timestamp = now.Add(time.Duration(i) * time.Millisecond)
		row.Provider, row.Model, row.Status, row.Decision, row.Cost = "p"+row.ID, "m", "success", "ALLOW", 1
		if err := st.InsertLog(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	h := New(st)
	cfg := filepath.Join(t.TempDir(), "omniswitch.yaml")
	if err := os.WriteFile(cfg, []byte("routes: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.SetConfigPath(cfg)
	h.SetMetricsPublic(metricsPublic)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	h.RegisterPrometheus(mux)
	return mux
}

type tenantCaller struct {
	name     string
	identity *gateway.Identity
	// sees lists the log ids the caller may see, in any order.
	sees []string
}

var tenantCallers = []tenantCaller{
	{name: "auth off", sees: []string{"r0", "r1", "r2", "r3"}},
	{name: "global admin", identity: &gateway.Identity{APIKeyID: "k0", Role: "admin"}, sees: []string{"r0", "r1", "r2", "r3"}},
	{name: "organization o1", identity: &gateway.Identity{Subject: "u", OrganizationID: "o1", Role: "admin"}, sees: []string{"r1", "r2"}},
	{name: "workspace w1", identity: &gateway.Identity{APIKeyID: "k1", WorkspaceID: "w1", OrganizationID: "o1", Role: "admin"}, sees: []string{"r1"}},
	{name: "workspace w3", identity: &gateway.Identity{APIKeyID: "k3", WorkspaceID: "w3", OrganizationID: "o2", Role: "owner"}, sees: []string{"r3"}},
}

func serveAs(mux http.Handler, c tenantCaller, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if c.identity != nil {
		req = req.WithContext(gateway.ContextWithIdentity(req.Context(), *c.identity))
		// The auth middleware sets the role header from the identity.
		req.Header.Set("x-omniswitch-role", c.identity.Role)
		req.Header.Set("x-omniswitch-key-id", c.identity.APIKeyID)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestDashboardReadsAreTenantScoped(t *testing.T) {
	mux := newTenantDashboard(t, false)
	for _, c := range tenantCallers {
		t.Run(c.name, func(t *testing.T) {
			rec := serveAs(mux, c, http.MethodGet, "/api/logs")
			var logs struct {
				Logs  []store.RequestLog `json:"logs"`
				Total int                `json:"total"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &logs); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("GET /api/logs = %d %s", rec.Code, rec.Body.String())
			}
			got := map[string]bool{}
			for _, l := range logs.Logs {
				got[l.ID] = true
			}
			if logs.Total != len(c.sees) || len(got) != len(c.sees) {
				t.Fatalf("logs = %v (total %d), want %v", got, logs.Total, c.sees)
			}
			for _, id := range c.sees {
				if !got[id] {
					t.Fatalf("logs = %v, want %v", got, c.sees)
				}
			}

			rec = serveAs(mux, c, http.MethodGet, "/api/metrics")
			var metrics store.Metrics
			if err := json.Unmarshal(rec.Body.Bytes(), &metrics); err != nil || metrics.TotalRequests != len(c.sees) {
				t.Fatalf("GET /api/metrics = %d %s, want %d requests", rec.Code, rec.Body.String(), len(c.sees))
			}

			rec = serveAs(mux, c, http.MethodGet, "/api/metrics/providers")
			var providers struct {
				Providers []store.ProviderMetrics `json:"providers"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &providers); err != nil || len(providers.Providers) != len(c.sees) {
				t.Fatalf("GET /api/metrics/providers = %d %s, want %d providers", rec.Code, rec.Body.String(), len(c.sees))
			}

			rec = serveAs(mux, c, http.MethodGet, "/api/analytics/cost?groupBy=provider")
			var cost struct {
				Rows []store.CostAnalyticsRow `json:"rows"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &cost); err != nil || len(cost.Rows) != len(c.sees) {
				t.Fatalf("GET /api/analytics/cost = %d %s, want %d rows", rec.Code, rec.Body.String(), len(c.sees))
			}

			visible := map[string]bool{}
			for _, id := range c.sees {
				visible[id] = true
			}
			for _, id := range []string{"r0", "r1", "r2", "r3"} {
				trace := "t" + strings.TrimPrefix(id, "r")
				want := http.StatusNotFound
				if visible[id] {
					want = http.StatusOK
				}
				if rec := serveAs(mux, c, http.MethodGet, "/api/traces/"+trace); rec.Code != want {
					t.Errorf("GET /api/traces/%s = %d %s, want %d", trace, rec.Code, rec.Body.String(), want)
				}
			}
		})
	}
}

func TestGatewayWideEndpointsNeedGlobalCaller(t *testing.T) {
	for _, public := range []bool{false, true} {
		mux := newTenantDashboard(t, public)
		for _, c := range tenantCallers {
			global := c.identity == nil || (c.identity.WorkspaceID == "" && c.identity.OrganizationID == "")
			for _, path := range []string{"/api/configs", "/api/config/raw", "/metrics"} {
				want := http.StatusOK
				if !global && (path != "/metrics" || !public) {
					want = http.StatusForbidden
				}
				rec := serveAs(mux, c, http.MethodGet, path)
				if rec.Code != want {
					t.Errorf("public=%v %s GET %s = %d %s, want %d", public, c.name, path, rec.Code, rec.Body.String(), want)
				}
				if want == http.StatusForbidden && !strings.Contains(rec.Body.String(), "workspace or organization") {
					t.Errorf("%s GET %s body = %s, want an explanation", c.name, path, rec.Body.String())
				}
			}
			if !global {
				// Writes are refused before the body is read.
				if rec := serveAs(mux, c, http.MethodPost, "/api/configs"); rec.Code != http.StatusForbidden {
					t.Errorf("%s POST /api/configs = %d, want 403", c.name, rec.Code)
				}
				if rec := serveAs(mux, c, http.MethodPost, "/api/config/raw"); rec.Code != http.StatusForbidden {
					t.Errorf("%s POST /api/config/raw = %d, want 403", c.name, rec.Code)
				}
			}
			if rec := serveAs(mux, c, http.MethodGet, "/api/health"); rec.Code != http.StatusOK {
				t.Errorf("%s GET /api/health = %d, want 200", c.name, rec.Code)
			}
		}
	}
}

// Callers below admin stay limited to their own API key inside their
// tenant.
func TestDashboardKeepsKeyScopeInsideTenant(t *testing.T) {
	mux := newTenantDashboard(t, false)
	viewer := tenantCaller{name: "viewer", identity: &gateway.Identity{APIKeyID: "other", WorkspaceID: "w1", OrganizationID: "o1", Role: "viewer"}}
	rec := serveAs(mux, viewer, http.MethodGet, "/api/logs")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("viewer GET /api/logs = %d %s, want no logs", rec.Code, rec.Body.String())
	}
}
