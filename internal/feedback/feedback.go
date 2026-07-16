package feedback

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

type Handler struct {
	store *store.Store
}

func New(st *store.Store) *Handler {
	return &Handler{store: st}
}

type CreateRequest struct {
	RequestID string            `json:"request_id,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
	Score     int               `json:"score"`
	Rating    string            `json:"rating,omitempty"`
	Comment   string            `json:"comment,omitempty"`
	UserID    string            `json:"user_id,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

func (h *Handler) CreateFeedback(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.RequestID == "" && req.TraceID == "" {
		writeError(w, http.StatusBadRequest, "request_id or trace_id is required")
		return
	}
	if req.Score < -1 || req.Score > 1 {
		writeError(w, http.StatusBadRequest, "score must be -1, 0, or 1")
		return
	}

	entry := store.Feedback{
		ID:        newID("fb"),
		RequestID: req.RequestID,
		TraceID:   req.TraceID,
		Timestamp: time.Now().UTC(),
		Score:     req.Score,
		Rating:    req.Rating,
		Comment:   req.Comment,
		UserID:    req.UserID,
		Metadata:  req.Metadata,
	}
	if err := h.store.InsertFeedback(r.Context(), entry); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"feedback": entry})
}

func (h *Handler) ListFeedback(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := h.store.ListFeedback(r.Context(), limit, r.URL.Query().Get("request_id"), r.URL.Query().Get("trace_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []store.Feedback{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": entries})
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
