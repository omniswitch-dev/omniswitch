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

	"github.com/omniswitch-dev/omniswitch/internal/store"
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

	now := time.Now().UTC()
	// CreatePromptVersion picks the version inside the INSERT, so two
	// concurrent creates for one name cannot get the same version.
	p, err := h.store.CreatePromptVersion(r.Context(), store.Prompt{
		ID:        newID("prompt"),
		Name:      req.Name,
		Template:  req.Template,
		Variables: extractVariables(req.Template),
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
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

// ListPromptVersions handles GET /api/prompts/versions?name=<prompt-name>.
func (h *Handler) ListPromptVersions(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	prompts, err := h.store.ListPromptVersions(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if prompts == nil {
		prompts = []store.Prompt{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompts": prompts})
}

// Render limits bound the work one render request can cause.
const (
	// MaxRenderedBytes caps the size of a rendered prompt.
	MaxRenderedBytes = 1 << 20
	// MaxRenderVariables caps how many variables one request may supply.
	MaxRenderVariables = 256
	// MaxRenderVariableBytes caps the size of one variable value.
	MaxRenderVariableBytes = 256 << 10
)

// errRenderTooLarge reports a render that would exceed MaxRenderedBytes.
var errRenderTooLarge = fmt.Errorf("rendered prompt exceeds %d bytes", MaxRenderedBytes)

// RenderPrompt handles POST /api/prompts/render.
func (h *Handler) RenderPrompt(w http.ResponseWriter, r *http.Request) {
	var req RenderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Variables) > MaxRenderVariables {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d variables are allowed", MaxRenderVariables))
		return
	}
	for key, value := range req.Variables {
		if len(value) > MaxRenderVariableBytes {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("variable %q exceeds %d bytes", key, MaxRenderVariableBytes))
			return
		}
	}

	p, err := h.store.GetPrompt(r.Context(), req.PromptID)
	if err != nil {
		writeError(w, http.StatusNotFound, "prompt not found")
		return
	}

	rendered, err := renderTemplate(p.Template, req.Variables, MaxRenderedBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"rendered": rendered})
}

// renderTemplate substitutes {{name}} placeholders in a single pass over the
// template. Substituted values are copied verbatim and never scanned again,
// so a value containing another placeholder cannot trigger recursive
// expansion. Unknown placeholders are left as-is. Rendering stops with an
// error once the output would exceed limit bytes.
func renderTemplate(template string, variables map[string]string, limit int) (string, error) {
	var out strings.Builder
	last := 0
	for _, match := range varPattern.FindAllStringSubmatchIndex(template, -1) {
		start, end := match[0], match[1]
		value, ok := variables[template[match[2]:match[3]]]
		if !ok {
			continue
		}
		if out.Len()+(start-last)+len(value) > limit {
			return "", errRenderTooLarge
		}
		out.WriteString(template[last:start])
		out.WriteString(value)
		last = end
	}
	if out.Len()+len(template)-last > limit {
		return "", errRenderTooLarge
	}
	out.WriteString(template[last:])
	return out.String(), nil
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
