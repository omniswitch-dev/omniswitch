package eval

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
)

// TestDatasetsAndExperimentsRejectScopedCallers covers SEC-3: datasets and
// experiments have no workspace, so tenant-scoped callers get 403.
func TestDatasetsAndExperimentsRejectScopedCallers(t *testing.T) {
	h := NewHandler(nil) // the guard runs before any store access
	handlers := map[string]http.HandlerFunc{
		"CreateDataset": h.CreateDataset, "GetDataset": h.GetDataset, "ListDatasets": h.ListDatasets,
		"UpdateDataset": h.UpdateDataset, "DeleteDataset": h.DeleteDataset, "AddExamples": h.AddExamples,
		"CreateExperiment": h.CreateExperiment, "GetExperiment": h.GetExperiment, "ListExperiments": h.ListExperiments,
		"GetExperimentResults": h.GetExperimentResults,
	}
	for _, identity := range []gateway.Identity{
		{Role: "owner", WorkspaceID: "ws_a", OrganizationID: "org_a"},
		{Role: "owner", OrganizationID: "org_a"},
	} {
		for name, fn := range handlers {
			r := httptest.NewRequest(http.MethodPost, "/api/evals?id=x", strings.NewReader(`{}`))
			r = r.WithContext(gateway.ContextWithIdentity(r.Context(), identity))
			rec := httptest.NewRecorder()
			fn(rec, r)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s for %+v: status = %d, want 403; body = %s", name, identity, rec.Code, rec.Body.String())
			}
		}
	}
}
