package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestDatasetAndExperimentRoundTrip covers STORE-07: the tables were never
// created, '?' was not rewritten for PostgreSQL, TEXT timestamps were
// scanned into time.Time and results into a struct pointer.
func TestDatasetAndExperimentRoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		created := time.Date(2026, 3, 4, 5, 6, 7, 800, time.UTC)
		ds := &Dataset{ID: "ds1", Name: "d", Description: "desc", Version: 1, CreatedAt: created, UpdatedAt: created,
			Metadata: map[string]string{"k": "v"}, Examples: []DatasetExample{{ID: "e1"}}}
		if err := st.CreateDataset(ctx, ds); err != nil {
			t.Fatalf("CreateDataset: %v", err)
		}
		got, err := st.GetDataset(ctx, "ds1")
		if err != nil {
			t.Fatalf("GetDataset: %v", err)
		}
		if !got.CreatedAt.Equal(created) || got.Metadata["k"] != "v" || len(got.Examples) != 1 || got.Description != "desc" {
			t.Fatalf("GetDataset = %+v", got)
		}
		got.Name = "d2"
		got.UpdatedAt = created.Add(time.Hour)
		if err := st.UpdateDataset(ctx, got); err != nil {
			t.Fatalf("UpdateDataset: %v", err)
		}
		if err := st.UpdateDataset(ctx, &Dataset{ID: "missing"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("UpdateDataset(missing) = %v, want ErrNotFound", err)
		}
		list, total, err := st.ListDatasets(ctx, 10, -1)
		if err != nil || total != 1 || len(list) != 1 || list[0].Name != "d2" || !list[0].UpdatedAt.Equal(created.Add(time.Hour)) {
			t.Fatalf("ListDatasets = %+v, %d, %v", list, total, err)
		}

		exp := &Experiment{ID: "x1", Name: "x", DatasetID: "ds1", Status: "pending", CreatedAt: created, UpdatedAt: created,
			Config: ExperimentConfig{Models: []string{"m"}}}
		if err := st.CreateExperiment(ctx, exp); err != nil {
			t.Fatalf("CreateExperiment: %v", err)
		}
		gotExp, err := st.GetExperiment(ctx, "x1")
		if err != nil {
			t.Fatalf("GetExperiment: %v", err)
		}
		if gotExp.Results != nil || !gotExp.CreatedAt.Equal(created) || len(gotExp.Config.Models) != 1 {
			t.Fatalf("GetExperiment = %+v", gotExp)
		}
		gotExp.Status = "completed"
		gotExp.Results = &ExperimentResult{TotalEvaluated: 3, ModelResults: map[string]ModelResult{"m": {Model: "m", Successful: 3}}}
		if err := st.UpdateExperiment(ctx, gotExp); err != nil {
			t.Fatalf("UpdateExperiment: %v", err)
		}
		for _, status := range []string{"", "completed"} {
			exps, total, err := st.ListExperiments(ctx, 10, -3, status)
			if err != nil || total != 1 || len(exps) != 1 {
				t.Fatalf("ListExperiments(%q) = %+v, %d, %v", status, exps, total, err)
			}
			if exps[0].Results == nil || exps[0].Results.TotalEvaluated != 3 || exps[0].Results.ModelResults["m"].Successful != 3 {
				t.Fatalf("ListExperiments(%q) results = %+v", status, exps[0].Results)
			}
		}
		if _, total, err := st.ListExperiments(ctx, 10, 0, "pending"); err != nil || total != 0 {
			t.Fatalf("ListExperiments(pending) total = %d, %v", total, err)
		}
		if _, err := st.GetExperiment(ctx, "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetExperiment(missing) = %v, want ErrNotFound", err)
		}

		if err := st.DeleteDataset(ctx, "ds1"); err != nil {
			t.Fatalf("DeleteDataset: %v", err)
		}
		if err := st.DeleteDataset(ctx, "ds1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DeleteDataset(again) = %v, want ErrNotFound", err)
		}
	})
}

// TestAddExamplesConcurrentNoLostUpdates covers the AddExamples
// read-modify-write: concurrent calls used to overwrite each other.
func TestAddExamplesConcurrentNoLostUpdates(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		now := time.Now().UTC()
		if err := st.CreateDataset(ctx, &Dataset{ID: "ds", Name: "d", Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		const workers = 16
		var wg sync.WaitGroup
		errs := make(chan error, workers)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs <- st.AddExamples(ctx, "ds", []DatasetExample{{ID: fmt.Sprintf("e%d", i)}})
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("AddExamples: %v", err)
			}
		}
		got, err := st.GetDataset(ctx, "ds")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Examples) != workers || got.Version != 1+workers {
			t.Fatalf("examples = %d, version = %d; want %d, %d", len(got.Examples), got.Version, workers, 1+workers)
		}
		if err := st.AddExamples(ctx, "missing", nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("AddExamples(missing) = %v, want ErrNotFound", err)
		}
	})
}
