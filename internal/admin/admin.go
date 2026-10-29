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
	"strings"
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
	Name               string   `json:"name"`
	WorkspaceID        string   `json:"workspace_id,omitempty"`
	Role               string   `json:"role,omitempty"`
	RateLimit          int      `json:"rate_limit,omitempty"`
	BudgetUSD          float64  `json:"budget_usd,omitempty"`
	MonthlyCostBudget  float64  `json:"monthly_cost_budget,omitempty"`
	MonthlyTokenBudget int      `json:"monthly_token_budget,omitempty"`
	ExpiresIn          string   `json:"expires_in,omitempty"`
	AllowedModels      []string `json:"allowed_models,omitempty"`
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
	if len(req.AllowedModels) > 256 {
		writeError(w, http.StatusBadRequest, "allowed_models must contain at most 256 model IDs")
		return
	}
	for _, model := range req.AllowedModels {
		if strings.TrimSpace(model) == "" || strings.TrimSpace(model) != model {
			writeError(w, http.StatusBadRequest, "allowed_models must contain non-empty model IDs without surrounding whitespace")
			return
		}
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
		if len(caller.AllowedModels) > 0 {
			if len(req.AllowedModels) == 0 {
				req.AllowedModels = append([]string(nil), caller.AllowedModels...)
			}
			for _, model := range req.AllowedModels {
				if !caller.AllowsModel(model) {
					writeError(w, http.StatusForbidden, "cannot create a key with broader model access than your own")
					return
				}
			}
		}
		if gateway.RoleRank(req.Role) > gateway.RoleRank(caller.Role) {
			writeError(w, http.StatusForbidden, "cannot create a key with a role above your own")
			return
		}
		switch scope := gateway.ScopeFromContext(r.Context()); {
		case scope.WorkspaceID != "":
			if req.WorkspaceID != "" && req.WorkspaceID != scope.WorkspaceID {
				writeError(w, http.StatusForbidden, "cannot create a key in another workspace")
				return
			}
			req.WorkspaceID = scope.WorkspaceID
		case !scope.Global():
			// An organization-scoped caller creates keys only in the
			// workspaces of its organization; a key without a workspace
			// would be global.
			if req.WorkspaceID == "" {
				writeError(w, http.StatusBadRequest, "workspace_id is required: keys belong to a workspace of your organization")
				return
			}
			visible, err := h.keyInScope(r.Context(), scope, req.WorkspaceID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "look up workspace")
				return
			}
			if !visible {
				writeError(w, http.StatusNotFound, "workspace not found")
				return
			}
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
		AllowedModels:      req.AllowedModels,
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
	scope := gateway.ScopeFromContext(r.Context())
	keys, err := h.store.ListAPIKeysForTenant(r.Context(), store.TenantFilter{WorkspaceID: scope.WorkspaceID, OrganizationID: scope.OrganizationID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
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
		visible, err := h.keyInScope(r.Context(), gateway.ScopeFromContext(r.Context()), target.WorkspaceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "look up api key")
			return
		}
		if !visible {
			// Same answer as a missing key, so ids in other tenants are
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
