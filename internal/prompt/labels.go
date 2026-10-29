package prompt

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

var labelPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Labels manages named environment pointers such as production and staging.
// Point a label at a previous prompt_id to roll back without editing history.
func (h *Handler) Labels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		workspaceID, ok := h.resolveWorkspace(w, r, r.URL.Query().Get("workspace_id"))
		if !ok {
			return
		}
		labels, err := h.store.ListWorkspacePromptLabels(r.Context(), workspaceID, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list prompt labels")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"labels": labels})
	case http.MethodPost:
		var req struct {
			Name        string `json:"name"`
			Label       string `json:"label"`
			PromptID    string `json:"prompt_id"`
			WorkspaceID string `json:"workspace_id,omitempty"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if strings.TrimSpace(req.Name) == "" || req.PromptID == "" || !labelPattern.MatchString(req.Label) {
			writeError(w, http.StatusBadRequest, "name, prompt_id, and a 1-64 character alphanumeric/._- label are required")
			return
		}
		workspaceID, ok := h.resolveWorkspace(w, r, req.WorkspaceID)
		if !ok {
			return
		}
		label := store.PromptLabel{WorkspaceID: workspaceID, Name: req.Name, Label: req.Label, PromptID: req.PromptID, UpdatedAt: time.Now().UTC()}
		if err := h.store.SetPromptLabel(r.Context(), label); err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "prompt version not found for this name")
			} else {
				writeError(w, http.StatusInternalServerError, "failed to set prompt label")
			}
			return
		}
		writeJSON(w, http.StatusOK, label)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "only GET and POST are supported")
	}
}
