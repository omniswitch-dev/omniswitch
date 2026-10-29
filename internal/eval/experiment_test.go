package eval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func newExperimentTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestCreateExperimentRunsAfterRequestEnds covers the background run: it
// used r.Context(), which is canceled when the handler returns, so the run
// failed at its first store call and the experiment stayed "pending".
func TestCreateExperimentRunsAfterRequestEnds(t *testing.T) {
	st := newExperimentTestStore(t)
	h := NewHandler(st)

	dsRec := httptest.NewRecorder()
	h.CreateDataset(dsRec, httptest.NewRequest(http.MethodPost, "/api/evals/datasets", strings.NewReader(`{"name":"ds"}`)))
	if dsRec.Code != http.StatusCreated {
		t.Fatalf("CreateDataset status = %d, body = %s", dsRec.Code, dsRec.Body.String())
	}
	var ds store.Dataset
	if err := json.Unmarshal(dsRec.Body.Bytes(), &ds); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/evals/experiments",
		strings.NewReader(`{"name":"exp","dataset_id":"`+ds.ID+`","config":{"models":["m1"]}}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.CreateExperiment(rec, req)
	cancel() // what net/http does once the handler returns
	if rec.Code != http.StatusCreated {
		t.Fatalf("CreateExperiment status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created store.Experiment
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != "pending" {
		t.Fatalf("response status = %q, want pending", created.Status)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		exp, err := st.GetExperiment(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("GetExperiment: %v", err)
		}
		if exp.Status == "completed" {
			if exp.Results == nil || len(exp.Results.ModelResults) != 1 {
				t.Fatalf("completed experiment results = %+v", exp.Results)
			}
			return
		}
		if exp.Status == "failed" || time.Now().After(deadline) {
			t.Fatalf("experiment status = %q, want completed", exp.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAddExamplesUnknownDatasetIsNotFound(t *testing.T) {
	h := NewHandler(newExperimentTestStore(t))
	rec := httptest.NewRecorder()
	h.AddExamples(rec, httptest.NewRequest(http.MethodPost, "/api/evals/datasets/examples",
		strings.NewReader(`{"dataset_id":"missing","examples":[{"id":"e1"}]}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}

func TestListHandlersClampNegativeOffset(t *testing.T) {
	h := NewHandler(newExperimentTestStore(t))
	for name, fn := range map[string]http.HandlerFunc{"datasets": h.ListDatasets, "experiments": h.ListExperiments} {
		rec := httptest.NewRecorder()
		fn(rec, httptest.NewRequest(http.MethodGet, "/x?limit=-1&offset=-5", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", name, rec.Code, rec.Body.String())
		}
		var body struct{ Limit, Offset int }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Limit != store.DefaultPageSize || body.Offset != 0 {
			t.Fatalf("%s limit/offset = %d/%d", name, body.Limit, body.Offset)
		}
	}
}
