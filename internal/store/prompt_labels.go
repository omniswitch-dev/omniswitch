package store

import (
	"context"
	"database/sql"
	"time"
)

// PromptLabel is a mutable deployment pointer to an immutable prompt version.
type PromptLabel struct {
	Name      string    `json:"name"`
	Label     string    `json:"label"`
	PromptID  string    `json:"prompt_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SetPromptLabel promotes or rolls back a label atomically, and only within
// the named prompt. A nonexistent or mismatched version cannot move the label.
func (s *Store) SetPromptLabel(ctx context.Context, label PromptLabel) error {
	result, err := s.exec(ctx, `INSERT INTO prompt_labels (name, label, prompt_id, updated_at)
		SELECT name, ?, id, ? FROM prompts WHERE id = ? AND name = ?
		ON CONFLICT(name, label) DO UPDATE SET prompt_id = excluded.prompt_id, updated_at = excluded.updated_at`,
		label.Label, label.UpdatedAt.Format(time.RFC3339Nano), label.PromptID, label.Name)
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
	rows, err := s.query(ctx, `SELECT name, label, prompt_id, updated_at FROM prompt_labels WHERE name = ? ORDER BY label`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := make([]PromptLabel, 0)
	for rows.Next() {
		var label PromptLabel
		var updatedAt string
		if err := rows.Scan(&label.Name, &label.Label, &label.PromptID, &updatedAt); err != nil {
			return nil, err
		}
		label.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

func (s *Store) GetPromptByLabel(ctx context.Context, name, label string) (Prompt, error) {
	var id string
	if err := s.queryRow(ctx, `SELECT prompt_id FROM prompt_labels WHERE name = ? AND label = ?`, name, label).Scan(&id); err != nil {
		return Prompt{}, err
	}
	return s.GetPrompt(ctx, id)
}
