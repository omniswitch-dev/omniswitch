package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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
		h.a2aSendMessage(w, r, request, false)
	case "message/send":
		h.a2aSendMessage(w, r, request, false)
	case "SendMessageStream", "message/stream":
		h.a2aSendMessage(w, r, request, true)
	case "GetTask", "tasks/get":
		h.a2aGetTask(w, request)
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

	task := &a2aTask{ID: newA2ATaskID(), ContextID: input.Message.ContextID, CreatedAt: time.Now().UTC()}
	task.Status = a2aTaskStatus{State: a2aStateWorking, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
	a2aTasks.Store(task.ID, task)
	pruneA2ATasks()

	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		writeA2AStreamEvent(w, "status-update", map[string]any{
			"taskId": task.ID, "contextId": task.ContextID,
			"status": map[string]any{"state": a2aStateWorking},
		})
	}

	chatRequest := provider.ChatRequest{Model: model, Messages: []provider.Message{{Role: "user", Content: strings.Join(parts, "\n")}}}
	chat, headers, status, _, err := h.executeCompatibleChat(r, chatRequest)
	copyHeaders(w.Header(), headers)

	finish := func(failed bool, failureMessage string) {
		if failed {
			setState(task, a2aStateFailed)
			task.Status.Message = map[string]any{
				"role": "ROLE_AGENT", "parts": []map[string]string{{"text": failureMessage}},
			}
			a2aTasks.Store(task.ID, task)
			if stream {
				writeA2AStreamEvent(w, "status-update", taskPayload(task))
				fmt.Fprintf(w, "event: error\ndata: {\"code\":-32000,\"message\":%q}\n\n", failureMessage)
			} else {
				writeA2AError(w, request.ID, -32603, fmt.Sprintf("inference failed (HTTP %d)", status))
			}
			return
		}
		content := ""
		messageID := "msg_" + task.ID
		if len(chat.Choices) > 0 {
			content = chat.Choices[0].Message.Content
			messageID = "msg_" + chat.ID
		}
		message := map[string]any{
			"role":      "ROLE_AGENT",
			"parts":     []map[string]string{{"text": content}},
			"messageId": messageID,
		}
		if task.ContextID != "" {
			message["contextId"] = task.ContextID
		}
		task.History = append(task.History, message)
		task.Artifacts = append(task.Artifacts, a2aArtifact{
			Name:        "response",
			Description: "Agent response text",
			Parts:       []map[string]any{{"kind": "text", "text": content}},
		})
		setState(task, a2aStateCompleted)
		a2aTasks.Store(task.ID, task)

		payload := taskPayload(task)
		if stream {
			writeA2AStreamEvent(w, "artifact-update", map[string]any{
				"taskId": task.ID, "contextId": task.ContextID,
				"artifact": payload.Artifacts[len(payload.Artifacts)-1],
			})
			writeA2AStreamEvent(w, "status-update", payload)
		} else {
			response := map[string]any{
				"id":        task.ID,
				"contextId": task.ContextID,
				"status":    task.Status,
				"artifacts": task.Artifacts,
				"history":   task.History,
			}
			writeA2AResponse(w, request.ID, response)
		}
	}
	finish(err != nil, fmt.Sprintf("inference failed (HTTP %d)", status))
}

type a2aTaskView struct {
	ID        string           `json:"id"`
	ContextID string           `json:"contextId,omitempty"`
	Status    a2aTaskStatus    `json:"status"`
	Artifacts []a2aArtifact    `json:"artifacts,omitempty"`
	History   []map[string]any `json:"history,omitempty"`
}

func taskPayload(task *a2aTask) a2aTaskView {
	return a2aTaskView{ID: task.ID, ContextID: task.ContextID, Status: task.Status, Artifacts: task.Artifacts, History: task.History}
}

// writeA2AStreamEvent emits one SSE frame. Errors during flush are ignored:
// the client may have disconnected mid-stream.
func writeA2AStreamEvent(w http.ResponseWriter, event string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (h *Handler) a2aGetTask(w http.ResponseWriter, request a2aRPCRequest) {
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil || strings.TrimSpace(params.ID) == "" {
		writeA2AError(w, request.ID, -32602, "params.id is required")
		return
	}
	task, ok := getA2ATask(params.ID)
	if !ok {
		writeA2AError(w, request.ID, -32001, a2aTaskNotFoundError(params.ID))
		return
	}
	writeA2AResponse(w, request.ID, taskPayload(task))
}

func (h *Handler) a2aCancelTask(w http.ResponseWriter, r *http.Request, request a2aRPCRequest) {
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil || strings.TrimSpace(params.ID) == "" {
		writeA2AError(w, request.ID, -32602, "params.id is required")
		return
	}
	task, ok := getA2ATask(params.ID)
	if !ok {
		writeA2AError(w, request.ID, -32001, a2aTaskNotFoundError(params.ID))
		return
	}
	switch task.Status.State {
	case a2aStateCompleted:
		// Terminal state; report as-is per spec.
	case a2aStateCanceled:
	default:
		if task.Status.State == a2aStateWorking {
			// The governing request has not returned yet; mark canceled so the
			// completing writer records the outcome against a canceled task.
			setState(task, a2aStateCanceled)
			a2aTasks.Store(task.ID, task)
		}
	}
	writeA2AResponse(w, request.ID, taskPayload(task))
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
