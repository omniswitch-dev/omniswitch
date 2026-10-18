package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
		w.Header().Set("Allow", http.MethodPost)
		writeA2AError(w, nil, -32600, "Invalid Request")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.requestBodyLimit())
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeA2AError(w, nil, -32700, "Parse error")
		return
	}
	if !json.Valid(raw) {
		writeA2AError(w, nil, -32700, "Parse error")
		return
	}
	var request a2aRPCRequest
	if err := json.Unmarshal(raw, &request); err != nil || request.JSONRPC != "2.0" || len(request.ID) == 0 {
		writeA2AError(w, request.ID, -32600, "Invalid Request")
		return
	}
	switch request.Method {
	case "SendMessage", "message/send":
		h.a2aSendMessage(w, r, request, false)
	case "SendMessageStream", "message/stream":
		h.a2aSendMessage(w, r, request, true)
	case "GetTask", "tasks/get":
		h.a2aGetTask(w, r, request)
	case "CancelTask", "tasks/cancel":
		h.a2aCancelTask(w, r, request)
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

// a2aCaller identifies the authenticated caller that owns tasks. It prefers
// the identity stored by the authentication middleware and falls back to the
// identity headers that middleware sets.
func a2aCaller(r *http.Request) (keyID, workspaceID string) {
	if identity, ok := IdentityFromContext(r.Context()); ok {
		return identity.APIKeyID, identity.WorkspaceID
	}
	return r.Header.Get("x-omniswitch-key-id"), r.Header.Get("x-omniswitch-workspace-id")
}

func (h *Handler) a2aSendMessage(w http.ResponseWriter, r *http.Request, request a2aRPCRequest, stream bool) {
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

	// CancelTask cancels this context, which stops the inference.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	keyID, workspaceID := a2aCaller(r)
	task := newA2ATask(input.Message.ContextID, keyID, workspaceID, cancel)
	a2aTasks.put(task)

	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		writeA2AStreamResult(w, request.ID, statusUpdate(task.snapshot(), false, nil))
	}

	chatRequest := provider.ChatRequest{Model: model, Messages: []provider.Message{{Role: "user", Content: strings.Join(parts, "\n")}}}
	chat, headers, status, _, err := h.executeCompatibleChat(r.WithContext(ctx), chatRequest)
	if !stream {
		copyHeaders(w.Header(), headers)
	}

	var view a2aTaskView
	if err != nil {
		view = task.fail(fmt.Sprintf("inference failed (HTTP %d)", status))
	} else {
		content := ""
		messageID := "msg_" + task.id
		if len(chat.Choices) > 0 {
			content = chat.Choices[0].Message.Content
			messageID = "msg_" + chat.ID
		}
		message := map[string]any{
			"role":      "ROLE_AGENT",
			"parts":     []map[string]string{{"text": content}},
			"messageId": messageID,
		}
		if task.contextID != "" {
			message["contextId"] = task.contextID
		}
		view = task.complete(message, a2aArtifact{
			ArtifactID:  "artifact_" + strings.TrimPrefix(task.id, "task_"),
			Name:        "response",
			Description: "Agent response text",
			Parts:       []map[string]any{{"kind": "text", "text": content}},
		})
	}

	if stream {
		// Response headers can no longer be sent once the stream started, so
		// gateway metadata (provider, cost, ...) travels in the final frame.
		if view.Status.State == a2aStateCompleted && len(view.Artifacts) > 0 {
			writeA2AStreamResult(w, request.ID, map[string]any{
				"kind": "artifact-update", "taskId": view.ID, "contextId": view.ContextID,
				"artifact": view.Artifacts[len(view.Artifacts)-1], "append": false, "lastChunk": true,
			})
		}
		writeA2AStreamResult(w, request.ID, statusUpdate(view, true, gatewayMetadata(headers)))
		if view.Status.State == a2aStateFailed {
			writeA2AStreamFrame(w, map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{
				"code": -32603, "message": fmt.Sprintf("inference failed (HTTP %d)", status),
			}})
		}
		return
	}
	if view.Status.State == a2aStateFailed {
		writeA2AError(w, request.ID, -32603, fmt.Sprintf("inference failed (HTTP %d)", status))
		return
	}
	writeA2AResponse(w, request.ID, view)
}

func statusUpdate(view a2aTaskView, final bool, metadata map[string]string) map[string]any {
	update := map[string]any{
		"kind": "status-update", "taskId": view.ID, "contextId": view.ContextID,
		"status": view.Status, "final": final,
	}
	if len(metadata) > 0 {
		update["metadata"] = metadata
	}
	return update
}

// gatewayMetadata returns the gateway's own response headers
// (x-omniswitch-*) as a flat map.
func gatewayMetadata(headers http.Header) map[string]string {
	metadata := map[string]string{}
	for key, values := range headers {
		if strings.HasPrefix(strings.ToLower(key), "x-omniswitch-") && len(values) > 0 {
			metadata[strings.ToLower(key)] = values[0]
		}
	}
	return metadata
}

// writeA2AStreamResult emits one SSE frame holding a JSON-RPC response, as
// the A2A JSON-RPC binding requires for streaming methods.
func writeA2AStreamResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeA2AStreamFrame(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// writeA2AStreamFrame emits one SSE data frame. Write errors are ignored: the
// client may have disconnected mid-stream.
func writeA2AStreamFrame(w http.ResponseWriter, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func a2aTaskID(w http.ResponseWriter, request a2aRPCRequest) (string, bool) {
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil || strings.TrimSpace(params.ID) == "" {
		writeA2AError(w, request.ID, -32602, "params.id is required")
		return "", false
	}
	return strings.TrimSpace(params.ID), true
}

func (h *Handler) a2aGetTask(w http.ResponseWriter, r *http.Request, request a2aRPCRequest) {
	id, ok := a2aTaskID(w, request)
	if !ok {
		return
	}
	keyID, workspaceID := a2aCaller(r)
	task, ok := a2aTasks.get(id, keyID, workspaceID)
	if !ok {
		writeA2AError(w, request.ID, -32001, a2aTaskNotFoundError(id))
		return
	}
	writeA2AResponse(w, request.ID, task.snapshot())
}

func (h *Handler) a2aCancelTask(w http.ResponseWriter, r *http.Request, request a2aRPCRequest) {
	id, ok := a2aTaskID(w, request)
	if !ok {
		return
	}
	keyID, workspaceID := a2aCaller(r)
	task, ok := a2aTasks.get(id, keyID, workspaceID)
	if !ok {
		writeA2AError(w, request.ID, -32001, a2aTaskNotFoundError(id))
		return
	}
	view, canceled := task.requestCancel()
	if !canceled {
		writeA2AError(w, request.ID, -32002, fmt.Sprintf("task %q is %s and cannot be canceled", id, view.Status.State))
		return
	}
	writeA2AResponse(w, request.ID, view)
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
		"capabilities":       map[string]bool{"streaming": true, "pushNotifications": false, "extendedAgentCard": true},
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
