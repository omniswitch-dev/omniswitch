package main

import (
	"math/rand"
	"net/http"
	"time"
)

// resolveFault decides whether this request should be faulted, and with
// what fault type. The X-Bench-Fault request header is a deterministic
// override (used by scripted conformance/reliability probes that need an
// exact fault on an exact request); absent that, the process-wide
// scheduled fault window set via POST /admin/fault applies probabilistically
// at its configured rate (used by scenario S5's load-test fault windows).
func (s *server) resolveFault(r *http.Request) (string, bool) {
	if v := r.Header.Get("X-Bench-Fault"); v != "" && v != "none" {
		return v, true
	}
	faultType, rate, active := s.fault.active()
	if !active {
		return "", false
	}
	if rand.Float64() < rate {
		return faultType, true
	}
	return "", false
}

// applyFault writes an error response (or, for "reset", hijacks and closes
// the connection) for fault types that do not require special streaming
// handling. It returns true if the request has been fully handled and the
// caller must return without writing anything else.
//
// "stall" and "slow-first-byte" are handled by the caller via
// stallOrSlowDelay, because both need to happen before any response is
// written but do not, by themselves, produce an error status. "midstream-
// disconnect" only makes sense inside a streaming response and is handled
// directly in handleChatCompletions'/handleAnthropicMessages' SSE loop.
func (s *server) applyFault(w http.ResponseWriter, faultType string) bool {
	switch faultType {
	case "429":
		s.stats.recordFault("429")
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": map[string]string{
				"message": "mock-upstream: injected 429 rate limit",
				"type":    "rate_limit_error",
			},
		})
		return true
	case "500":
		s.stats.recordFault("500")
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{
				"message": "mock-upstream: injected 500 internal error",
				"type":    "internal_error",
			},
		})
		return true
	case "503":
		s.stats.recordFault("503")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]string{
				"message": "mock-upstream: injected 503 unavailable",
				"type":    "unavailable_error",
			},
		})
		return true
	case "reset":
		s.stats.recordFault("reset")
		hijackAndReset(w)
		return true
	default:
		return false
	}
}

// hijackAndReset closes the underlying TCP connection without writing an
// HTTP response, simulating an upstream connection reset (used by
// scenario S5's "connection reset" fault window).
func hijackAndReset(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	_ = conn.Close()
}

// stallOrSlowDelay returns an additional delay to apply before writing any
// response bytes for the "stall" (very long delay, meant to exceed the
// gateway's configured per-attempt timeout) and "slow-first-byte" (shorter
// but still large delay) fault types. It returns 0 for every other fault
// type.
func stallOrSlowDelay(faultType string) time.Duration {
	switch faultType {
	case "stall":
		return 60 * time.Second
	case "slow-first-byte":
		return 5 * time.Second
	default:
		return 0
	}
}
