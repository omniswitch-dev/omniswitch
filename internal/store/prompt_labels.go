package store

import (
	"context"
	"database/sql"
	"time"
)

// PromptLabel is a mutable deployment pointer to an immutable prompt version.
type PromptLabel struct {
	WorkspaceID string    `json:"workspace_id,omitempty"`
	Name        string    `json:"name"`
	Label       string    `json:"label"`
	PromptID    string    `json:"prompt_id"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SetPromptLabel promotes or rolls back a label atomically, and only within
// the named prompt. A nonexistent or mismatched version cannot move the label.
func (s *Store) SetPromptLabel(ctx context.Context, label PromptLabel) error {
	result, err := s.exec(ctx, `INSERT INTO prompt_labels (workspace_id, name, label, prompt_id, updated_at)
		SELECT workspace_id, name, ?, id, ? FROM prompts WHERE id = ? AND name = ? AND workspace_id = ?
		ON CONFLICT(workspace_id, name, label) DO UPDATE SET prompt_id = excluded.prompt_id, updated_at = excluded.updated_at`,
		label.Label, formatTime(label.UpdatedAt), label.PromptID, label.Name, label.WorkspaceID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return sql.ErrNoRows
	}
	return err
}

func (s *Store) ListPromptLabels(ctx context.Context, name string) ([]PromptLabel, error) {
	return s.ListWorkspacePromptLabels(ctx, "", name)
}

func (s *Store) ListWorkspacePromptLabels(ctx context.Context, workspaceID, name string) ([]PromptLabel, error) {
	rows, err := s.query(ctx, `SELECT workspace_id, name, label, prompt_id, updated_at FROM prompt_labels WHERE workspace_id = ? AND name = ? ORDER BY label`, workspaceID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := make([]PromptLabel, 0)
	for rows.Next() {
		var label PromptLabel
		var updatedAt string
		if err := rows.Scan(&label.WorkspaceID, &label.Name, &label.Label, &label.PromptID, &updatedAt); err != nil {
			return nil, err
		}
		label.UpdatedAt = parseTime(updatedAt)
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

func (s *Store) GetPromptByLabel(ctx context.Context, name, label string) (Prompt, error) {
	return s.GetWorkspacePromptByLabel(ctx, "", name, label)
}

func (s *Store) GetWorkspacePromptByLabel(ctx context.Context, workspaceID, name, label string) (Prompt, error) {
	var id string
	if err := s.queryRow(ctx, `SELECT prompt_id FROM prompt_labels WHERE workspace_id = ? AND name = ? AND label = ?`, workspaceID, name, label).Scan(&id); err != nil {
		return Prompt{}, err
	}
	return s.GetWorkspacePrompt(ctx, workspaceID, id)
}
