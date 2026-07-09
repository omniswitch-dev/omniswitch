package org

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sentinel/internal/store"
)

// Handler serves organization and workspace management APIs.
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
}

type WorkspaceMemberRequest struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Role        string `json:"role"`
}

func (h *Handler) CreateOrganization(w http.ResponseWriter, r *http.Request) {
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
	organizations, err := h.store.ListOrganizations(r.Context())
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
	var req CreateWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
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
	workspaces, err := h.store.ListWorkspaces(r.Context(), r.URL.Query().Get("organization_id"))
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
	if err := h.store.InsertUser(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers(r.Context())
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
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = "viewer"
	}

	member := store.WorkspaceMember{
		WorkspaceID: strings.TrimSpace(req.WorkspaceID),
		UserID:      strings.TrimSpace(req.UserID),
		Role:        role,
		CreatedAt:   time.Now().UTC(),
	}
	if err := h.store.UpsertWorkspaceMember(r.Context(), member); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, member)
}

func (h *Handler) ListWorkspaceMembers(w http.ResponseWriter, r *http.Request) {
	members, err := h.store.ListWorkspaceMembers(r.Context(), r.URL.Query().Get("workspace_id"))
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
