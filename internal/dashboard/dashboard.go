package dashboard

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"sentinel/internal/store"
)

//go:embed static
var staticFiles embed.FS

// Handler serves the dashboard API and embedded web UI.
type Handler struct {
	store *store.Store
}

// New creates a new dashboard handler.
func New(st *store.Store) *Handler {
	return &Handler{store: st}
}

// RegisterRoutes adds dashboard routes to the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Dashboard API endpoints.
	mux.HandleFunc("/api/logs", h.getLogs)
	mux.HandleFunc("/api/metrics", h.getMetrics)
	mux.HandleFunc("/api/metrics/providers", h.getProviderMetrics)
	mux.HandleFunc("/api/health", h.health)

	// Serve embedded static files at root.
	staticFS, _ := fs.Sub(staticFiles, "static")
	fileServer := http.FileServer(http.FS(staticFS))
	mux.Handle("/", fileServer)
}

func (h *Handler) getLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	providerFilter := r.URL.Query().Get("provider")
	statusFilter := r.URL.Query().Get("status")

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	logs, total, err := h.store.ListLogs(r.Context(), limit, offset, providerFilter, statusFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if logs == nil {
		logs = []store.RequestLog{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "total": total, "limit": limit, "offset": offset})
}

func (h *Handler) getMetrics(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour)
	switch window {
	case "1h":
		since = now.Add(-1 * time.Hour)
	case "6h":
		since = now.Add(-6 * time.Hour)
	case "7d":
		since = now.Add(-7 * 24 * time.Hour)
	case "30d":
		since = now.Add(-30 * 24 * time.Hour)
	}

	metrics, err := h.store.GetMetrics(r.Context(), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (h *Handler) getProviderMetrics(w http.ResponseWriter, r *http.Request) {
	since := time.Now().UTC().Add(-24 * time.Hour)
	metrics, err := h.store.GetProviderMetrics(r.Context(), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if metrics == nil {
		metrics = []store.ProviderMetrics{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": metrics})
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "healthy",
		"time":   time.Now().UTC(),
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
