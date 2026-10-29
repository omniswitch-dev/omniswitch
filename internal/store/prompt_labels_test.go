package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestPromptLabelsSurviveRestartAndRollback(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for index, id := range []string{"v1", "v2"} {
		if err := st.InsertPrompt(ctx, Prompt{ID: id, Name: "support", Version: index + 1, Template: id, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"v1", "v2", "v1"} {
		if err := st.SetPromptLabel(ctx, PromptLabel{Name: "support", Label: "production", PromptID: id, UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetPromptLabel(ctx, PromptLabel{Name: "support", Label: "production", PromptID: "missing", UpdatedAt: time.Now()}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing version = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	prompt, err := st.GetPromptByLabel(ctx, "support", "production")
	if err != nil || prompt.ID != "v1" {
		t.Fatalf("prompt/error = %+v/%v", prompt, err)
	}
	labels, err := st.ListPromptLabels(ctx, "support")
	if err != nil || len(labels) != 1 || labels[0].PromptID != "v1" {
		t.Fatalf("labels/error = %+v/%v", labels, err)
	}
}
