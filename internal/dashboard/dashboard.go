package dashboard

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
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

// RegisterPrometheus registers a lightweight Prometheus text endpoint. It is
// kept separate from RegisterRoutes so operators can explicitly disable it.
func (h *Handler) RegisterPrometheus(mux *http.ServeMux) {
	mux.HandleFunc("/metrics", h.prometheus)
}

func (h *Handler) getLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	providerFilter := r.URL.Query().Get("provider")
	statusFilter := r.URL.Query().Get("status")

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var logs []store.RequestLog
	var total int
	var err error
	if keyID := scopedAPIKeyID(r); keyID != "" {
		logs, total, err = h.store.ListLogsForAPIKey(r.Context(), keyID, limit, offset, providerFilter, statusFilter)
	} else {
		logs, total, err = h.store.ListLogs(r.Context(), limit, offset, providerFilter, statusFilter)
	}
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

	var metrics store.Metrics
	var err error
	if keyID := scopedAPIKeyID(r); keyID != "" {
		metrics, err = h.store.GetMetricsForAPIKey(r.Context(), since, keyID)
	} else {
		metrics, err = h.store.GetMetrics(r.Context(), since)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (h *Handler) getProviderMetrics(w http.ResponseWriter, r *http.Request) {
	since := time.Now().UTC().Add(-24 * time.Hour)
	var metrics []store.ProviderMetrics
	var err error
	if keyID := scopedAPIKeyID(r); keyID != "" {
		metrics, err = h.store.GetProviderMetricsForAPIKey(r.Context(), since, keyID)
	} else {
		metrics, err = h.store.GetProviderMetrics(r.Context(), since)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if metrics == nil {
		metrics = []store.ProviderMetrics{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": metrics})
}

// scopedAPIKeyID returns the key scope for non-administrative callers. The
// authentication middleware owns these internal headers; callers cannot set
// them when authentication is enabled because it overwrites their values.
func scopedAPIKeyID(r *http.Request) string {
	role := r.Header.Get("x-omniswitch-role")
	if role == "admin" || role == "owner" {
		return ""
	}
	return r.Header.Get("x-omniswitch-key-id")
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "healthy",
		"time":   time.Now().UTC(),
	})
}

func (h *Handler) prometheus(w http.ResponseWriter, r *http.Request) {
	metrics, err := h.store.GetMetrics(r.Context(), time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fprintf := func(format string, values ...any) { _, _ = fmt.Fprintf(w, format, values...) }
	fprintf("# HELP sentinel_requests_total Gateway requests over the last 24 hours.\n")
	fprintf("# TYPE sentinel_requests_total counter\n")
	fprintf("sentinel_requests_total %d\n", metrics.TotalRequests)
	fprintf("sentinel_requests_allowed_total %d\n", metrics.AllowedCount)
	fprintf("sentinel_requests_denied_total %d\n", metrics.DeniedCount)
	fprintf("sentinel_requests_errors_total %d\n", metrics.ErrorCount)
	fprintf("sentinel_cache_hits_total %d\n", metrics.CacheHits)
	fprintf("sentinel_tokens_total %d\n", metrics.TotalTokens)
	fprintf("sentinel_cost_usd_total %.6f\n", metrics.TotalCost)
	fprintf("sentinel_request_latency_ms_average %.3f\n", metrics.AvgLatencyMs)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
