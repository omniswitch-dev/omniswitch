package prompt

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"sentinel/internal/store"
)

// Handler serves prompt management API endpoints.
type Handler struct {
	store *store.Store
}

// New creates a new prompt handler.
func New(st *store.Store) *Handler {
	return &Handler{store: st}
}

// CreatePromptRequest is the request body for creating a prompt.
type CreatePromptRequest struct {
	Name     string `json:"name"`
	Template string `json:"template"`
}

// RenderRequest is the request body for rendering a prompt template.
type RenderRequest struct {
	PromptID  string            `json:"prompt_id"`
	Variables map[string]string `json:"variables"`
}

// CreatePrompt handles POST /api/prompts.
func (h *Handler) CreatePrompt(w http.ResponseWriter, r *http.Request) {
	var req CreatePromptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" || req.Template == "" {
		writeError(w, http.StatusBadRequest, "name and template are required")
		return
	}

	p := store.Prompt{
		ID:        newID("prompt"),
		Name:      req.Name,
		Version:   1,
		Template:  req.Template,
		Variables: extractVariables(req.Template),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := h.store.InsertPrompt(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// ListPrompts handles GET /api/prompts.
func (h *Handler) ListPrompts(w http.ResponseWriter, r *http.Request) {
	prompts, err := h.store.ListPrompts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if prompts == nil {
		prompts = []store.Prompt{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompts": prompts})
}

// RenderPrompt handles POST /api/prompts/render.
func (h *Handler) RenderPrompt(w http.ResponseWriter, r *http.Request) {
	var req RenderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	p, err := h.store.GetPrompt(r.Context(), req.PromptID)
	if err != nil {
		writeError(w, http.StatusNotFound, "prompt not found")
		return
	}

	rendered := p.Template
	for key, value := range req.Variables {
		rendered = strings.ReplaceAll(rendered, "{{"+key+"}}", value)
	}

	writeJSON(w, http.StatusOK, map[string]string{"rendered": rendered})
}

var varPattern = regexp.MustCompile(`\{\{(\w+)\}\}`)

func extractVariables(template string) []string {
	matches := varPattern.FindAllStringSubmatch(template, -1)
	seen := map[string]bool{}
	var vars []string
	for _, m := range matches {
		if !seen[m[1]] {
			vars = append(vars, m[1])
			seen[m[1]] = true
		}
	}
	return vars
}

func newID(prefix string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg}})
}
