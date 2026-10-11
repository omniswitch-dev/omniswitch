package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

type server struct {
	cfg   config
	fault *faultState
	stats *stats
	nowFn func() time.Time
}

func newServer(cfg config) *server {
	return &server{
		cfg:   cfg,
		fault: &faultState{},
		stats: newStats(),
		nowFn: time.Now,
	}
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.stats.snapshot())
}

// adminFaultRequest is the body accepted by POST /admin/fault. Scenario S5
// (bench/scenarios/S5) drives this endpoint to open scheduled fault
// windows, e.g. {"type":"429","rate":1.0,"duration_seconds":30}.
type adminFaultRequest struct {
	Type            string  `json:"type"`
	Rate            float64 `json:"rate"`
	DurationSeconds float64 `json:"duration_seconds"`
}

func (s *server) handleAdminFault(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req adminFaultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Type == "" || req.Type == "none" {
		s.fault.clear()
		writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
		return
	}
	if req.Rate <= 0 {
		req.Rate = 1.0
	}
	if req.DurationSeconds <= 0 {
		req.DurationSeconds = 30
	}
	until := s.nowFn().Add(time.Duration(req.DurationSeconds * float64(time.Second)))
	s.fault.set(req.Type, req.Rate, until)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "set",
		"type":   req.Type,
		"rate":   req.Rate,
		"until":  until,
	})
}

func (s *server) handleAdminReset(w http.ResponseWriter, r *http.Request) {
	s.fault.clear()
	s.stats.reset()
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// requestDelay resolves the effective base delay for a request: the
// X-Bench-Delay-Ms header overrides the server default, so a single mock
// instance can serve every S1 payload/rate combination without a restart.
func (s *server) requestDelay(r *http.Request) time.Duration {
	if v := r.Header.Get("X-Bench-Delay-Ms"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return s.cfg.BaseDelay
}

func (s *server) streamChunks(r *http.Request) int {
	if v := r.Header.Get("X-Bench-Stream-Chunks"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return s.cfg.StreamChunks
}

func (s *server) streamChunkInterval(r *http.Request) time.Duration {
	if v := r.Header.Get("X-Bench-Stream-Chunk-Interval-Ms"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return s.cfg.StreamChunkInterval
}

func (s *server) outputTokens(r *http.Request) int {
	switch r.Header.Get("X-Bench-Output-Size") {
	case "medium":
		return s.cfg.MediumOutputTokens
	default:
		return s.cfg.SmallOutputTokens
	}
}
