package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"sentinel/internal/provider"
)

// Embeddings serves OpenAI-compatible /v1/embeddings for native OpenAI and
// OpenAI-compatible custom providers.
func (h *Handler) Embeddings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxRequestBytes)
	var request provider.EmbeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if strings.TrimSpace(request.Model) == "" || request.Input == nil {
		writeError(w, http.StatusBadRequest, "model and input are required")
		return
	}
	requestID := newRequestID()
	traceID := requestHeaderOrNew(r, "x-sentinel-trace-id", "trace")
	sessionID := r.Header.Get("x-sentinel-session-id")
	keyID := r.Header.Get("x-sentinel-key-id")
	if denied, reason := h.budgetExceeded(r.Context(), keyID); denied {
		w.Header().Set("x-sentinel-trace-id", traceID)
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": map[string]string{"message": reason, "type": "budget_exceeded", "code": "budget_exceeded"}})
		return
	}
	input, _ := json.Marshal(request.Input)
	logRequest := provider.ChatRequest{Model: request.Model, Messages: []provider.Message{{Role: "user", Content: string(input)}}}
	if h.guardrails != nil {
		results := h.guardrails.EvaluateInput(logRequest.Messages)
		h.recordGuardrailResults(r.Context(), requestID, results)
		for _, result := range results {
			if result.Action == "deny" {
				h.logRequest(r.Context(), logContext{ID: requestID, TraceID: traceID, SessionID: sessionID, Request: logRequest, APIKeyID: keyID, Status: "denied", ErrorMessage: result.Message})
				w.Header().Set("x-sentinel-trace-id", traceID)
				writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"message": result.Message, "type": result.Type, "code": "guardrail_triggered"}})
				return
			}
		}
	}
	response, meta, err := h.router.Embeddings(r.Context(), request, r.Header.Get("x-sentinel-provider"))
	if err != nil {
		h.logRequest(r.Context(), logContext{ID: requestID, TraceID: traceID, SessionID: sessionID, Request: logRequest, Meta: &meta, APIKeyID: keyID, Status: "error", ErrorMessage: err.Error()})
		w.Header().Set("x-sentinel-trace-id", traceID)
		writeError(w, http.StatusBadGateway, "embedding provider error: "+err.Error())
		return
	}
	logResponse := provider.ChatResponse{ID: requestID, Object: "embedding", Created: time.Now().Unix(), Model: response.Model, Usage: response.Usage}
	h.logRequest(r.Context(), logContext{ID: requestID, TraceID: traceID, SessionID: sessionID, Request: logRequest, Response: &logResponse, Meta: &meta, APIKeyID: keyID, Status: "success"})
	w.Header().Set("x-sentinel-trace-id", traceID)
	w.Header().Set("x-sentinel-session-id", sessionID)
	writeJSON(w, http.StatusOK, response)
}
