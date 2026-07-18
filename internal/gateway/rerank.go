package gateway

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

// Rerank serves a provider-neutral /v1/rerank endpoint for RAG retrieval
// stacks. It reuses the gateway's normal auth, budget, guardrail, routing, and
// logging posture.
func (h *Handler) Rerank(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxRequestBytes)
	var request provider.RerankRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if strings.TrimSpace(request.Model) == "" || strings.TrimSpace(request.Query) == "" || len(request.Documents) == 0 {
		writeError(w, http.StatusBadRequest, "model, query, and documents are required")
		return
	}

	requestID := newRequestID()
	traceID := requestHeaderOrNew(r, "x-omniswitch-trace-id", "trace")
	sessionID := r.Header.Get("x-omniswitch-session-id")
	keyID := r.Header.Get("x-omniswitch-key-id")
	if denied, reason := h.budgetExceeded(r.Context(), keyID); denied {
		w.Header().Set("x-omniswitch-trace-id", traceID)
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": map[string]string{"message": reason, "type": "budget_exceeded", "code": "budget_exceeded"}})
		return
	}

	logRequest := provider.ChatRequest{Model: request.Model, Messages: []provider.Message{{Role: "user", Content: rerankGuardrailText(request)}}}
	if h.guardrails != nil {
		results := h.guardrails.EvaluateInputContext(r.Context(), logRequest.Messages)
		h.recordGuardrailResults(r.Context(), requestID, results)
		for _, result := range results {
			if result.Action == "deny" {
				h.logRequest(r.Context(), logContext{ID: requestID, TraceID: traceID, SessionID: sessionID, Request: logRequest, APIKeyID: keyID, Status: "denied", ErrorMessage: result.Message})
				w.Header().Set("x-omniswitch-trace-id", traceID)
				writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"message": result.Message, "type": result.Type, "code": "guardrail_triggered"}})
				return
			}
		}
	}

	response, meta, err := h.router.Rerank(r.Context(), request, r.Header.Get("x-omniswitch-provider"))
	if err != nil {
		h.logRequest(r.Context(), logContext{ID: requestID, TraceID: traceID, SessionID: sessionID, Request: logRequest, Meta: &meta, APIKeyID: keyID, Status: "error", ErrorMessage: err.Error()})
		w.Header().Set("x-omniswitch-trace-id", traceID)
		writeError(w, http.StatusBadGateway, "rerank provider error: "+err.Error())
		return
	}
	if response.ID == "" {
		response.ID = requestID
	}
	if response.Object == "" {
		response.Object = "list"
	}
	if response.Model == "" {
		response.Model = request.Model
	}
	logResponse := provider.ChatResponse{ID: response.ID, Object: "rerank", Created: time.Now().Unix(), Model: response.Model, Usage: response.Usage}
	h.logRequest(r.Context(), logContext{ID: requestID, TraceID: traceID, SessionID: sessionID, Request: logRequest, Response: &logResponse, Meta: &meta, APIKeyID: keyID, Status: "success"})
	w.Header().Set("x-omniswitch-trace-id", traceID)
	w.Header().Set("x-omniswitch-session-id", sessionID)
	writeJSON(w, http.StatusOK, response)
}

func rerankGuardrailText(request provider.RerankRequest) string {
	parts := []string{"query: " + request.Query}
	for index, document := range request.Documents {
		text := strings.TrimSpace(rerankDocumentText(document))
		if text != "" {
			parts = append(parts, "document "+strconv.Itoa(index)+": "+text)
		}
	}
	return strings.Join(parts, "\n")
}

func rerankDocumentText(document any) string {
	switch value := document.(type) {
	case string:
		return value
	case map[string]any:
		if text, ok := value["text"].(string); ok {
			return text
		}
		if text, ok := value["content"].(string); ok {
			return text
		}
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return ""
	}
	return string(payload)
}
