package vault

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Handler struct {
	vault *Vault
}

func NewHandler(v *Vault) *Handler {
	return &Handler{vault: v}
}

type CreateVirtualKeyRequest struct {
	Name         string            `json:"name"`
	ProviderType string            `json:"provider_type"`
	ProviderName string            `json:"provider_name,omitempty"`
	BaseURL      string            `json:"base_url,omitempty"`
	ProviderKey  string            `json:"provider_key"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type RotateVirtualKeyRequest struct {
	Name        string `json:"name"`
	ProviderKey string `json:"provider_key"`
}

func (h *Handler) CreateVirtualKey(w http.ResponseWriter, r *http.Request) {
	var req CreateVirtualKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.ProviderType = strings.ToLower(strings.TrimSpace(req.ProviderType))
	req.ProviderName = strings.TrimSpace(req.ProviderName)
	if req.ProviderName == "" {
		req.ProviderName = req.Name
	}
	if req.Name == "" || req.ProviderType == "" {
		writeError(w, http.StatusBadRequest, "name and provider_type are required")
		return
	}
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if req.ProviderType == "custom" && req.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "base_url is required for custom virtual keys")
		return
	}
	if req.ProviderKey == "" && req.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "provider_key is required unless base_url is set for a keyless local provider")
		return
	}

	now := time.Now().UTC()
	vk := VirtualKey{
		ID:           newID("vk"),
		Name:         req.Name,
		ProviderType: req.ProviderType,
		ProviderName: req.ProviderName,
		BaseURL:      req.BaseURL,
		ProviderKey:  req.ProviderKey,
		CreatedAt:    now,
		UpdatedAt:    now,
		Enabled:      true,
		Metadata:     req.Metadata,
	}
	if err := h.vault.Store(r.Context(), vk); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	vk.ProviderKey = ""
	writeJSON(w, http.StatusCreated, vk)
}

func (h *Handler) ListVirtualKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.vault.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []VirtualKey{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"virtual_keys": keys})
}

func (h *Handler) RotateVirtualKey(w http.ResponseWriter, r *http.Request) {
	var req RotateVirtualKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || req.ProviderKey == "" {
		writeError(w, http.StatusBadRequest, "name and provider_key are required")
		return
	}
	if err := h.vault.Rotate(r.Context(), req.Name, req.ProviderKey); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rotated"})
}

func (h *Handler) RevokeVirtualKey(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := h.vault.Revoke(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
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
