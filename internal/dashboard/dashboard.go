package dashboard

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

//go:embed static
var staticFiles embed.FS

// Handler serves the dashboard API and embedded web UI.
type Handler struct {
	store      *store.Store
	configPath string
}

// New creates a new dashboard handler.
func New(st *store.Store) *Handler {
	return &Handler{store: st}
}

func (h *Handler) SetConfigPath(path string) {
	h.configPath = strings.TrimSpace(path)
}

// RegisterRoutes adds dashboard routes to the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Dashboard API endpoints.
	mux.HandleFunc("/api/logs", h.getLogs)
	mux.HandleFunc("/api/metrics", h.getMetrics)
	mux.HandleFunc("/api/metrics/providers", h.getProviderMetrics)
	mux.HandleFunc("/api/analytics/cost", h.getCostAnalytics)
	mux.HandleFunc("/api/traces/", h.getTrace)
	mux.HandleFunc("/api/config/raw", h.rawConfig)
	mux.HandleFunc("/api/configs", h.configs)
	mux.HandleFunc("/api/health", h.health)

	// Serve embedded static files at root.
	staticFS, _ := fs.Sub(staticFiles, "static")
	fileServer := http.FileServer(http.FS(staticFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if isDashboardRoute(r.URL.Path) {
			serveDashboardIndex(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
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

func (h *Handler) getCostAnalytics(w http.ResponseWriter, r *http.Request) {
	since := windowStart(r.URL.Query().Get("window"))
	groupBy := r.URL.Query().Get("groupBy")
	var keyID string
	if scoped := scopedAPIKeyID(r); scoped != "" {
		keyID = scoped
	}
	rows, err := h.store.GetCostAnalytics(r.Context(), since, groupBy, keyID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if rows == nil {
		rows = []store.CostAnalyticsRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groupBy": firstNonEmpty(groupBy, "day"), "rows": rows})
}

func (h *Handler) getTrace(w http.ResponseWriter, r *http.Request) {
	traceID := strings.TrimPrefix(r.URL.Path, "/api/traces/")
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		writeError(w, http.StatusBadRequest, "trace id is required")
		return
	}
	detail, err := h.store.GetTrace(r.Context(), traceID, scopedAPIKeyID(r))
	if err != nil {
		writeError(w, http.StatusNotFound, "trace not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) configs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ref := strings.TrimSpace(r.URL.Query().Get("id"))
		if ref == "" {
			ref = strings.TrimSpace(r.URL.Query().Get("name"))
		}
		if ref != "" {
			record, err := h.store.GetGatewayConfig(r.Context(), ref)
			if err != nil {
				writeError(w, http.StatusNotFound, "config not found")
				return
			}
			writeJSON(w, http.StatusOK, record)
			return
		}
		includeDisabled, _ := strconv.ParseBool(r.URL.Query().Get("include_disabled"))
		records, err := h.store.ListGatewayConfigs(r.Context(), includeDisabled)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if records == nil {
			records = []store.GatewayConfigRecord{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"configs": records})
	case http.MethodPost:
		var req struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Format      string `json:"format"`
			Body        string `json:"body"`
			Enabled     *bool  `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" || strings.TrimSpace(req.Body) == "" {
			writeError(w, http.StatusBadRequest, "name and body are required")
			return
		}
		format := normalizedConfigFormat(req.Format)
		if _, err := gatewayconfig.Parse([]byte(req.Body), "."+format); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		id := strings.TrimSpace(req.ID)
		if id == "" {
			id = newID("cfg")
		}
		now := time.Now().UTC()
		record := store.GatewayConfigRecord{
			ID: id, Name: req.Name, Description: req.Description, Format: format,
			Body: req.Body, CreatedAt: now, UpdatedAt: now, Enabled: enabled,
		}
		if err := h.store.UpsertGatewayConfig(r.Context(), record); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, record)
	case http.MethodPatch:
		ref := strings.TrimSpace(r.URL.Query().Get("id"))
		if ref == "" {
			ref = strings.TrimSpace(r.URL.Query().Get("name"))
		}
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if ref == "" || req.Enabled == nil {
			writeError(w, http.StatusBadRequest, "id or name and enabled are required")
			return
		}
		if err := h.store.SetGatewayConfigEnabled(r.Context(), ref, *req.Enabled); err != nil {
			writeError(w, http.StatusNotFound, "config not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": *req.Enabled})
	case http.MethodDelete:
		ref := strings.TrimSpace(r.URL.Query().Get("id"))
		if ref == "" {
			ref = strings.TrimSpace(r.URL.Query().Get("name"))
		}
		if ref == "" {
			writeError(w, http.StatusBadRequest, "id or name is required")
			return
		}
		if err := h.store.DeleteGatewayConfig(r.Context(), ref); err != nil {
			writeError(w, http.StatusNotFound, "config not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) rawConfig(w http.ResponseWriter, r *http.Request) {
	if h.configPath == "" {
		writeError(w, http.StatusNotFound, "OMNISWITCH_CONFIG is not set")
		return
	}
	switch r.Method {
	case http.MethodGet:
		body, err := os.ReadFile(h.configPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		format := normalizedConfigFormat("")
		if strings.EqualFold(configExt(h.configPath), ".json") {
			format = "json"
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": h.configPath, "format": format, "body": string(body)})
	case http.MethodPost:
		var req struct {
			Body   string `json:"body"`
			Format string `json:"format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		format := normalizedConfigFormat(req.Format)
		if _, err := gatewayconfig.Parse([]byte(req.Body), "."+format); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := os.WriteFile(h.configPath, []byte(req.Body), 0o644); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"saved": true, "path": h.configPath})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func isDashboardRoute(path string) bool {
	switch path {
	case "/", "/overview", "/playground", "/logs", "/configs", "/guardrails", "/prompts", "/keys", "/providers":
		return true
	default:
		return strings.HasPrefix(path, "/traces/")
	}
}

func serveDashboardIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
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

func windowStart(window string) time.Time {
	now := time.Now().UTC()
	switch strings.TrimSpace(window) {
	case "1h":
		return now.Add(-1 * time.Hour)
	case "6h":
		return now.Add(-6 * time.Hour)
	case "7d":
		return now.Add(-7 * 24 * time.Hour)
	case "30d":
		return now.Add(-30 * 24 * time.Hour)
	default:
		return now.Add(-24 * time.Hour)
	}
}

func normalizedConfigFormat(format string) string {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), ".")) {
	case "json":
		return "json"
	default:
		return "yaml"
	}
}

func configExt(path string) string {
	return strings.ToLower(filepath.Ext(path))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func newID(prefix string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

func (h *Handler) prometheus(w http.ResponseWriter, r *http.Request) {
	metrics, err := h.store.GetMetrics(r.Context(), time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fprintf := func(format string, values ...any) { _, _ = fmt.Fprintf(w, format, values...) }
	fprintf("# HELP omniswitch_requests_total Gateway requests over the last 24 hours.\n")
	fprintf("# TYPE omniswitch_requests_total counter\n")
	fprintf("omniswitch_requests_total %d\n", metrics.TotalRequests)
	fprintf("omniswitch_requests_allowed_total %d\n", metrics.AllowedCount)
	fprintf("omniswitch_requests_denied_total %d\n", metrics.DeniedCount)
	fprintf("omniswitch_requests_errors_total %d\n", metrics.ErrorCount)
	fprintf("omniswitch_cache_hits_total %d\n", metrics.CacheHits)
	fprintf("omniswitch_tokens_total %d\n", metrics.TotalTokens)
	fprintf("omniswitch_cost_usd_total %.6f\n", metrics.TotalCost)
	fprintf("omniswitch_request_latency_ms_average %.3f\n", metrics.AvgLatencyMs)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
