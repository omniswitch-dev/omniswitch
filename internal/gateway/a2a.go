package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

// A2AAgentCard publishes the A2A v1 Agent Card. Discovery is intentionally
// public; task execution at /a2a remains protected by the normal gateway
// authentication and authorization middleware.
func (h *Handler) A2AAgentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET is supported")
		return
	}
	w.Header().Set("Content-Type", "application/a2a+json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeA2AAgentCard(w, r, h.a2aAgentCard(r))
}

// A2A implements the A2A v1 JSON-RPC binding's direct-message path. A
// SendMessage request is converted to the same ChatCompletions pipeline used
// by OpenAI-compatible clients, preserving routing, budgets, guardrails,
// cache isolation, logs, and provider telemetry.
func (h *Handler) A2A(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeA2AError(w, nil, -32600, "Invalid Request")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxRequestBytes)
	var request a2aRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.JSONRPC != "2.0" || len(request.ID) == 0 {
		writeA2AError(w, request.ID, -32600, "Invalid Request")
		return
	}
	switch request.Method {
	case "SendMessage":
		h.a2aSendMessage(w, r, request)
	case "GetExtendedAgentCard":
		writeA2AResponse(w, request.ID, map[string]any{"agentCard": h.a2aAgentCard(r)})
	default:
		writeA2AError(w, request.ID, -32601, "Method not found")
	}
}

type a2aRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type a2aSendMessageRequest struct {
	Message struct {
		ContextID string `json:"contextId,omitempty"`
		Parts     []struct {
			Text string `json:"text,omitempty"`
		} `json:"parts"`
	} `json:"message"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func (h *Handler) a2aSendMessage(w http.ResponseWriter, r *http.Request, request a2aRPCRequest) {
	var input a2aSendMessageRequest
	if err := json.Unmarshal(request.Params, &input); err != nil {
		writeA2AError(w, request.ID, -32602, "Invalid params")
		return
	}
	parts := make([]string, 0, len(input.Message.Parts))
	for _, part := range input.Message.Parts {
		if text := strings.TrimSpace(part.Text); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		writeA2AError(w, request.ID, -32602, "message.parts must include text")
		return
	}
	model := strings.TrimSpace(r.Header.Get("x-omniswitch-model"))
	if configured, ok := input.Metadata["model"].(string); ok && strings.TrimSpace(configured) != "" {
		model = strings.TrimSpace(configured)
	}
	if model == "" {
		writeA2AError(w, request.ID, -32602, "model is required in metadata.model or x-omniswitch-model")
		return
	}
	chatRequest := provider.ChatRequest{Model: model, Messages: []provider.Message{{Role: "user", Content: strings.Join(parts, "\n")}}}
	chat, headers, status, _, err := h.executeCompatibleChat(r, chatRequest)
	copyHeaders(w.Header(), headers)
	if err != nil {
		writeA2AError(w, request.ID, -32603, fmt.Sprintf("inference failed (HTTP %d)", status))
		return
	}
	content := ""
	if len(chat.Choices) > 0 {
		content = chat.Choices[0].Message.Content
	}
	message := map[string]any{
		"role":      "ROLE_AGENT",
		"parts":     []map[string]string{{"text": content}},
		"messageId": "msg_" + chat.ID,
	}
	if input.Message.ContextID != "" {
		message["contextId"] = input.Message.ContextID
	}
	writeA2AResponse(w, request.ID, map[string]any{"message": message})
}

func (h *Handler) a2aAgentCard(r *http.Request) map[string]any {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return map[string]any{
		"name":        "OmniSwitch AI Gateway",
		"description": "Routes and governs AI inference through an A2A direct-message interface.",
		"version":     "1.0",
		"supportedInterfaces": []map[string]string{{
			"url": scheme + "://" + r.Host + "/a2a", "protocolBinding": "JSONRPC", "protocolVersion": "1.0",
		}},
		"capabilities":       map[string]bool{"streaming": false, "pushNotifications": false, "extendedAgentCard": true},
		"defaultInputModes":  []string{"text/plain"},
		"defaultOutputModes": []string{"text/plain"},
		"securitySchemes": map[string]any{
			"omniswitchBearer": map[string]any{"httpAuthSecurityScheme": map[string]string{"scheme": "Bearer"}},
		},
		"security": []map[string][]string{{"omniswitchBearer": []string{}}},
		"skills": []map[string]any{{
			"id": "omniswitch-inference", "name": "Governed AI inference", "description": "Sends text to a routed model selected by metadata.model.",
			"tags": []string{"inference", "routing", "guardrails"}, "inputModes": []string{"text/plain"}, "outputModes": []string{"text/plain"},
		}},
	}
}

func writeA2AAgentCard(w http.ResponseWriter, r *http.Request, card map[string]any) {
	payload, err := json.Marshal(card)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode agent card")
		return
	}
	sum := sha256.Sum256(payload)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/a2a+json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(payload, '\n'))
}

func writeA2AResponse(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeA2AError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}
