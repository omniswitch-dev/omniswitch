package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"sentinel/internal/provider"
)

// Responses provides a pragmatic OpenAI Responses compatibility layer. It
// translates the text/message subset to Sentinel's common chat pipeline, so
// budgets, guardrails, cache isolation, logging, and routing are identical.
func (h *Handler) Responses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxRequestBytes)
	var request responsesRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if request.Stream {
		writeError(w, http.StatusBadRequest, "Responses streaming is not supported yet; use /v1/chat/completions with stream=true")
		return
	}
	chatRequest, err := request.toChatRequest()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	chat, headers, status, body, err := h.executeCompatibleChat(r, chatRequest)
	copyHeaders(w.Header(), headers)
	if err != nil {
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	content := ""
	if len(chat.Choices) > 0 {
		content = chat.Choices[0].Message.Content
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         strings.Replace(chat.ID, "chat_", "resp_", 1),
		"object":     "response",
		"created_at": chat.Created,
		"status":     "completed",
		"model":      chat.Model,
		"output": []any{map[string]any{
			"id":      "msg_" + chat.ID,
			"type":    "message",
			"role":    "assistant",
			"content": []any{map[string]string{"type": "output_text", "text": content}},
		}},
		"usage": map[string]int{
			"input_tokens":  chat.Usage.PromptTokens,
			"output_tokens": chat.Usage.CompletionTokens,
			"total_tokens":  chat.Usage.TotalTokens,
		},
	})
}

// Messages accepts the core Anthropic Messages shape and returns its response
// shape while using the same provider-neutral Sentinel request pipeline.
func (h *Handler) Messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxRequestBytes)
	var request anthropicMessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if request.Stream {
		writeError(w, http.StatusBadRequest, "Messages streaming is not supported yet; use /v1/chat/completions with stream=true")
		return
	}
	chatRequest := provider.ChatRequest{Model: request.Model, Messages: request.Messages, MaxTokens: request.MaxTokens, Temperature: request.Temperature, TopP: request.TopP}
	if strings.TrimSpace(request.System) != "" {
		chatRequest.Messages = append([]provider.Message{{Role: "system", Content: request.System}}, chatRequest.Messages...)
	}
	if strings.TrimSpace(chatRequest.Model) == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	chat, headers, status, body, err := h.executeCompatibleChat(r, chatRequest)
	copyHeaders(w.Header(), headers)
	if err != nil {
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	content := ""
	stopReason := "end_turn"
	if len(chat.Choices) > 0 {
		content = chat.Choices[0].Message.Content
		if chat.Choices[0].FinishReason != "" {
			stopReason = chat.Choices[0].FinishReason
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          chat.ID,
		"type":        "message",
		"role":        "assistant",
		"model":       chat.Model,
		"stop_reason": stopReason,
		"content":     []any{map[string]string{"type": "text", "text": content}},
		"usage": map[string]int{
			"input_tokens":  chat.Usage.PromptTokens,
			"output_tokens": chat.Usage.CompletionTokens,
		},
	})
}

type responsesRequest struct {
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input"`
	Instructions    string          `json:"instructions,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
}

func (r responsesRequest) toChatRequest() (provider.ChatRequest, error) {
	if strings.TrimSpace(r.Model) == "" {
		return provider.ChatRequest{}, fmt.Errorf("model is required")
	}
	messages, err := responseInputMessages(r.Input)
	if err != nil {
		return provider.ChatRequest{}, err
	}
	if strings.TrimSpace(r.Instructions) != "" {
		messages = append([]provider.Message{{Role: "system", Content: r.Instructions}}, messages...)
	}
	if len(messages) == 0 {
		return provider.ChatRequest{}, fmt.Errorf("input is required")
	}
	return provider.ChatRequest{Model: r.Model, Messages: messages, Temperature: r.Temperature, MaxTokens: r.MaxOutputTokens, TopP: r.TopP}, nil
}

func responseInputMessages(raw json.RawMessage) ([]provider.Message, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []provider.Message{{Role: "user", Content: text}}, nil
	}
	var messages []provider.Message
	if err := json.Unmarshal(raw, &messages); err == nil && len(messages) > 0 {
		return messages, nil
	}
	return nil, fmt.Errorf("input must be a string or message array")
}

type anthropicMessagesRequest struct {
	Model       string             `json:"model"`
	Messages    []provider.Message `json:"messages"`
	System      string             `json:"system,omitempty"`
	MaxTokens   *int               `json:"max_tokens,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

func (h *Handler) executeCompatibleChat(r *http.Request, request provider.ChatRequest) (provider.ChatResponse, http.Header, int, []byte, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return provider.ChatResponse{}, nil, 0, nil, err
	}
	clone := r.Clone(r.Context())
	clone.Method = http.MethodPost
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	recorder := &bufferedResponseWriter{header: make(http.Header)}
	h.ChatCompletions(recorder, clone)
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	if recorder.status != http.StatusOK {
		return provider.ChatResponse{}, recorder.header, recorder.status, recorder.body.Bytes(), fmt.Errorf("chat request failed")
	}
	var response provider.ChatResponse
	if err := json.Unmarshal(recorder.body.Bytes(), &response); err != nil {
		return provider.ChatResponse{}, recorder.header, http.StatusBadGateway, recorder.body.Bytes(), err
	}
	return response, recorder.header, recorder.status, recorder.body.Bytes(), nil
}

type bufferedResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *bufferedResponseWriter) Header() http.Header { return w.header }

func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferedResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func copyHeaders(destination, source http.Header) {
	for key, values := range source {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}
