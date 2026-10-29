package store

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

// TestCreatePromptVersionConcurrent covers STORE-09: MAX(version)+1 followed
// by a separate INSERT gave 20 concurrent creates only 4 distinct versions.
func TestCreatePromptVersionConcurrent(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		const n = 20
		var wg sync.WaitGroup
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				now := time.Now().UTC()
				p, err := st.CreatePromptVersion(ctx, Prompt{ID: fmt.Sprintf("p%d", i), Name: "support", Template: "hi {{name}}", Variables: []string{"name"}, CreatedAt: now, UpdatedAt: now})
				if err == nil && p.Version < 1 {
					err = fmt.Errorf("version %d", p.Version)
				}
				if err != nil {
					errs <- err
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("CreatePromptVersion() error = %v", err)
		}
		prompts, err := st.ListPromptVersions(ctx, "support")
		if err != nil {
			t.Fatal(err)
		}
		versions := make([]int, 0, len(prompts))
		for _, p := range prompts {
			versions = append(versions, p.Version)
		}
		sort.Ints(versions)
		if len(versions) != n {
			t.Fatalf("stored %d prompts, want %d", len(versions), n)
		}
		for i, v := range versions {
			if v != i+1 {
				t.Fatalf("versions = %v, want 1..%d", versions, n)
			}
		}
		other, err := st.CreatePromptVersion(ctx, Prompt{ID: "o1", Name: "other", Template: "x"})
		if err != nil || other.Version != 1 {
			t.Fatalf("first version of another name = %d, %v; want 1", other.Version, err)
		}
		got, err := st.GetPrompt(ctx, "o1")
		if err != nil || got.Template != "x" || got.Name != "other" {
			t.Fatalf("GetPrompt() = %+v, %v", got, err)
		}
	})
}

// TestUniquePromptIndexIsOptional checks that existing duplicate versions do
// not stop the store from opening; the index is skipped with a warning.
func TestUniquePromptIndexIsOptional(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.wdb.Exec("DROP INDEX idx_prompts_workspace_name_version"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := st.InsertPrompt(context.Background(), Prompt{ID: id, Name: "dup", Version: 1, Template: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	st, err = New(dir)
	if err != nil {
		t.Fatalf("New() with duplicate prompt versions error = %v", err)
	}
	defer st.Close()
	p, err := st.CreatePromptVersion(context.Background(), Prompt{ID: "c", Name: "dup", Template: "t"})
	if err != nil || p.Version != 2 {
		t.Fatalf("CreatePromptVersion() = %d, %v; want 2", p.Version, err)
	}

	// A fresh database does get the index.
	fresh := newTestStore(t)
	if err := fresh.InsertPrompt(context.Background(), Prompt{ID: "x", Name: "n", Version: 1, Template: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := fresh.InsertPrompt(context.Background(), Prompt{ID: "y", Name: "n", Version: 1, Template: "t"}); !isUniqueViolation(err) {
		t.Fatalf("duplicate (name, version) insert error = %v, want unique violation", err)
	}
}

// TestPostgresUniquePromptIndexIsOptional runs the same check on PostgreSQL,
// where a failed statement would abort the migration transaction unless it
// is isolated in a savepoint.
func TestPostgresUniquePromptIndexIsOptional(t *testing.T) {
	dsn := newPostgresTestDSN(t)
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.wdb.Exec("DROP INDEX idx_prompts_workspace_name_version"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := st.InsertPrompt(context.Background(), Prompt{ID: id, Name: "dup", Version: 1, Template: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	st, err = OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres() with duplicate prompt versions error = %v", err)
	}
	defer st.Close()
	if _, err := st.ListPrompts(context.Background()); err != nil {
		t.Fatal(err)
	}
}
