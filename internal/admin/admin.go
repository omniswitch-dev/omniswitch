package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// Handler serves admin API endpoints.
type Handler struct {
	store *store.Store
}

// New creates a new admin handler.
func New(st *store.Store) *Handler {
	return &Handler{store: st}
}

// CreateKeyRequest is the request body for creating an API key.
type CreateKeyRequest struct {
	Name               string  `json:"name"`
	WorkspaceID        string  `json:"workspace_id,omitempty"`
	Role               string  `json:"role,omitempty"`
	RateLimit          int     `json:"rate_limit,omitempty"`
	BudgetUSD          float64 `json:"budget_usd,omitempty"`
	MonthlyCostBudget  float64 `json:"monthly_cost_budget,omitempty"`
	MonthlyTokenBudget int     `json:"monthly_token_budget,omitempty"`
	ExpiresIn          string  `json:"expires_in,omitempty"`
}

// CreateKeyResponse includes the full API key (shown only once).
type CreateKeyResponse struct {
	Key    string       `json:"key"`
	APIKey store.APIKey `json:"api_key"`
}

// CreateAPIKey handles POST /api/keys. When the request carries an
// authenticated identity, the new key's role may not exceed the caller's,
// and a workspace-scoped caller can only create keys in its own workspace.
func (h *Handler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.RateLimit <= 0 {
		req.RateLimit = 60
	}
	var expiresAt *time.Time
	if req.ExpiresIn != "" {
		dur, err := time.ParseDuration(req.ExpiresIn)
		if err != nil || dur <= 0 {
			writeError(w, http.StatusBadRequest, "expires_in must be a positive Go duration such as 720h")
			return
		}
		t := time.Now().Add(dur)
		expiresAt = &t
	}
	if caller, ok := gateway.IdentityFromContext(r.Context()); ok {
		if gateway.RoleRank(req.Role) > gateway.RoleRank(caller.Role) {
			writeError(w, http.StatusForbidden, "cannot create a key with a role above your own")
			return
		}
		if caller.WorkspaceID != "" {
			if req.WorkspaceID != "" && req.WorkspaceID != caller.WorkspaceID {
				writeError(w, http.StatusForbidden, "cannot create a key in another workspace")
				return
			}
			req.WorkspaceID = caller.WorkspaceID
		}
	}

	rawKey, err := generateKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hash := sha256.Sum256([]byte(rawKey))

	key := store.APIKey{
		ID:                 newID("key"),
		Name:               req.Name,
		KeyHash:            hex.EncodeToString(hash[:]),
		KeyPrefix:          rawKey[:12] + "...",
		WorkspaceID:        req.WorkspaceID,
		Role:               req.Role,
		CreatedAt:          time.Now().UTC(),
		RateLimit:          req.RateLimit,
		BudgetUSD:          req.BudgetUSD,
		MonthlyCostBudget:  req.MonthlyCostBudget,
		MonthlyTokenBudget: req.MonthlyTokenBudget,
		Enabled:            true,
		ExpiresAt:          expiresAt,
	}

	if err := h.store.InsertAPIKey(r.Context(), key); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, CreateKeyResponse{Key: rawKey, APIKey: key})
}

// ListAPIKeys handles GET /api/keys.
func (h *Handler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.store.ListAPIKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if caller, ok := gateway.IdentityFromContext(r.Context()); ok && caller.WorkspaceID != "" {
		scoped := keys[:0]
		for _, key := range keys {
			if key.WorkspaceID == caller.WorkspaceID {
				scoped = append(scoped, key)
			}
		}
		keys = scoped
	}
	if keys == nil {
		keys = []store.APIKey{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

// DeleteAPIKey handles DELETE /api/keys/{id}.
func (h *Handler) DeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if caller, ok := gateway.IdentityFromContext(r.Context()); ok {
		target, err := h.store.GetAPIKeyByID(r.Context(), id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "api key not found")
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "look up api key")
			return
		}
		if caller.WorkspaceID != "" && target.WorkspaceID != caller.WorkspaceID {
			// Same answer as a missing key, so ids in other workspaces are
			// not confirmed.
			writeError(w, http.StatusNotFound, "api key not found")
			return
		}
		if gateway.RoleRank(target.Role) > gateway.RoleRank(caller.Role) {
			writeError(w, http.StatusForbidden, "cannot delete a key with a role above your own")
			return
		}
	}
	if err := h.store.DeleteAPIKey(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func generateKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate API key: %w", err)
	}
	return "sk-sentinel-" + hex.EncodeToString(b[:]), nil
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
