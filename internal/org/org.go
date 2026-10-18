package org

import (
	"crypto/rand"
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

// Handler serves organization and workspace management APIs. Every handler
// is limited to the caller's tenant (gateway.ScopeFromContext); see
// tenant.go for the rules.
type Handler struct {
	store *store.Store
}

func New(st *store.Store) *Handler {
	return &Handler{store: st}
}

type CreateOrganizationRequest struct {
	Name     string            `json:"name"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type CreateWorkspaceRequest struct {
	OrganizationID string            `json:"organization_id"`
	Name           string            `json:"name"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type CreateUserRequest struct {
	Email    string            `json:"email"`
	Name     string            `json:"name,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	// WorkspaceID adds the user to this workspace with Role. A scoped
	// caller always creates a user together with a membership in a
	// workspace it can see; a workspace-scoped caller defaults to its own
	// workspace.
	WorkspaceID string `json:"workspace_id,omitempty"`
	Role        string `json:"role,omitempty"`
}

type WorkspaceMemberRequest struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Role        string `json:"role"`
}

func (h *Handler) CreateOrganization(w http.ResponseWriter, r *http.Request) {
	if !gateway.ScopeFromContext(r.Context()).Global() {
		writeError(w, http.StatusForbidden, "only global callers can create organizations")
		return
	}
	var req CreateOrganizationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	org := store.Organization{
		ID:        newID("org"),
		Name:      strings.TrimSpace(req.Name),
		CreatedAt: time.Now().UTC(),
		Metadata:  req.Metadata,
	}
	if err := h.store.InsertOrganization(r.Context(), org); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, org)
}

func (h *Handler) ListOrganizations(w http.ResponseWriter, r *http.Request) {
	organizations, err := h.store.ListOrganizationsForTenant(r.Context(), tenantFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if organizations == nil {
		organizations = []store.Organization{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"organizations": organizations})
}

func (h *Handler) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	scope := gateway.ScopeFromContext(r.Context())
	if scope.WorkspaceID != "" {
		writeError(w, http.StatusForbidden, "workspace-scoped callers cannot create workspaces")
		return
	}
	var req CreateWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !scope.Global() {
		// An organization-scoped caller creates workspaces only in its
		// own organization; another organization looks like a missing
		// one.
		requested := strings.TrimSpace(req.OrganizationID)
		if requested != "" && requested != scope.OrganizationID {
			writeError(w, http.StatusNotFound, "organization not found")
			return
		}
		req.OrganizationID = scope.OrganizationID
	}
	if strings.TrimSpace(req.OrganizationID) == "" || strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "organization_id and name are required")
		return
	}

	workspace := store.Workspace{
		ID:             newID("ws"),
		OrganizationID: strings.TrimSpace(req.OrganizationID),
		Name:           strings.TrimSpace(req.Name),
		CreatedAt:      time.Now().UTC(),
		Metadata:       req.Metadata,
	}
	if err := h.store.InsertWorkspace(r.Context(), workspace); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, workspace)
}

func (h *Handler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	workspaces, err := h.store.ListWorkspacesForTenant(r.Context(), tenantFilter(r), r.URL.Query().Get("organization_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if workspaces == nil {
		workspaces = []store.Workspace{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": workspaces})
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Email) == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}

	user := store.User{
		ID:        newID("user"),
		Email:     strings.TrimSpace(strings.ToLower(req.Email)),
		Name:      strings.TrimSpace(req.Name),
		CreatedAt: time.Now().UTC(),
		Metadata:  req.Metadata,
	}
	scope := gateway.ScopeFromContext(r.Context())
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	if !scope.Global() {
		h.createScopedUser(w, r, scope, user, workspaceID, req.Role)
		return
	}
	// An existing email keeps its stored ID; respond with that ID, not the
	// fresh one generated above, which was never stored.
	stored, err := h.store.UpsertUser(r.Context(), user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if workspaceID != "" {
		if err := h.store.UpsertWorkspaceMember(r.Context(), store.WorkspaceMember{
			WorkspaceID: workspaceID, UserID: stored.ID, Role: memberRole(req.Role), CreatedAt: user.CreatedAt,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, stored)
}

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsersForTenant(r.Context(), tenantFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if users == nil {
		users = []store.User{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *Handler) UpsertWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	var req WorkspaceMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.WorkspaceID) == "" || strings.TrimSpace(req.UserID) == "" {
		writeError(w, http.StatusBadRequest, "workspace_id and user_id are required")
		return
	}
	member := store.WorkspaceMember{
		WorkspaceID: strings.TrimSpace(req.WorkspaceID),
		UserID:      strings.TrimSpace(req.UserID),
		Role:        memberRole(req.Role),
		CreatedAt:   time.Now().UTC(),
	}
	if scope := gateway.ScopeFromContext(r.Context()); !scope.Global() {
		if !h.authorizeMembership(w, r, scope, member) {
			return
		}
		if _, err := h.store.GetUserByID(r.Context(), member.UserID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "user not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := h.store.UpsertWorkspaceMember(r.Context(), member); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, member)
}

func (h *Handler) ListWorkspaceMembers(w http.ResponseWriter, r *http.Request) {
	members, err := h.store.ListWorkspaceMembersForTenant(r.Context(), tenantFilter(r), r.URL.Query().Get("workspace_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if members == nil {
		members = []store.WorkspaceMember{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
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
