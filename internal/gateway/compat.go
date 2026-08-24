package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

// Responses provides a pragmatic OpenAI Responses compatibility layer. It
// translates the text/message subset to OmniSwitch's common chat pipeline, so
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
	chatRequest, err := request.toChatRequest()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.Stream {
		chatRequest.Stream = true
		h.streamResponsesCompat(w, r, chatRequest)
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
// shape while using the same provider-neutral OmniSwitch request pipeline.
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
	chatRequest := provider.ChatRequest{Model: request.Model, Messages: request.Messages, MaxTokens: request.MaxTokens, Temperature: request.Temperature, TopP: request.TopP}
	if strings.TrimSpace(request.System) != "" {
		chatRequest.Messages = append([]provider.Message{{Role: "system", Content: request.System}}, chatRequest.Messages...)
	}
	if strings.TrimSpace(chatRequest.Model) == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	if request.Stream {
		chatRequest.Stream = true
		h.streamMessagesCompat(w, r, chatRequest)
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

func (h *Handler) streamResponsesCompat(w http.ResponseWriter, r *http.Request, request provider.ChatRequest) {
	h.executeCompatibleStream(w, r, request, newResponsesStreamTranslator())
}

func (h *Handler) streamMessagesCompat(w http.ResponseWriter, r *http.Request, request provider.ChatRequest) {
	h.executeCompatibleStream(w, r, request, newMessagesStreamTranslator())
}

func (h *Handler) executeCompatibleStream(w http.ResponseWriter, r *http.Request, request provider.ChatRequest, translator sseTranslator) {
	body, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	clone := r.Clone(r.Context())
	clone.Method = http.MethodPost
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	h.ChatCompletions(newTransformResponseWriter(w, translator), clone)
}

type sseTranslator interface {
	Translate(payload string) []sseEvent
}

type sseEvent struct {
	Event string
	Data  any
	Done  bool
}

type transformResponseWriter struct {
	target     http.ResponseWriter
	translator sseTranslator
	header     http.Header
	buffer     string
	streaming  bool
	wrote      bool
}

func newTransformResponseWriter(target http.ResponseWriter, translator sseTranslator) *transformResponseWriter {
	return &transformResponseWriter{target: target, translator: translator, header: make(http.Header)}
}

func (w *transformResponseWriter) Header() http.Header { return w.header }

func (w *transformResponseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	copyHeaders(w.target.Header(), w.header)
	w.target.WriteHeader(status)
	w.wrote = true
}

func (w *transformResponseWriter) Write(data []byte) (int, error) {
	if !w.streaming {
		if strings.Contains(strings.ToLower(w.header.Get("Content-Type")), "text/event-stream") {
			w.streaming = true
			copyHeaders(w.target.Header(), w.header)
			w.target.Header().Set("Content-Type", "text/event-stream")
			if !w.wrote {
				w.target.WriteHeader(http.StatusOK)
				w.wrote = true
			}
		} else {
			if !w.wrote {
				w.WriteHeader(http.StatusOK)
			}
			_, err := w.target.Write(data)
			return len(data), err
		}
	}
	w.buffer += string(data)
	for {
		index := strings.Index(w.buffer, "\n\n")
		if index < 0 {
			break
		}
		event := w.buffer[:index]
		w.buffer = w.buffer[index+2:]
		w.writeTranslatedEvent(event)
	}
	return len(data), nil
}

func (w *transformResponseWriter) writeTranslatedEvent(raw string) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		for _, event := range w.translator.Translate(payload) {
			writeNamedSSE(w.target, event.Event, event.Data, event.Done)
		}
		if flusher, ok := w.target.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

type responsesStreamTranslator struct {
	id        string
	model     string
	usage     *provider.Usage
	completed bool
}

func newResponsesStreamTranslator() *responsesStreamTranslator { return &responsesStreamTranslator{} }

func (t *responsesStreamTranslator) Translate(payload string) []sseEvent {
	if payload == "[DONE]" {
		return t.completedEvents()
	}
	var chunk provider.ChatResponseChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return nil
	}
	if chunk.ID != "" {
		t.id = strings.Replace(chunk.ID, "chat_", "resp_", 1)
	}
	if chunk.Model != "" {
		t.model = chunk.Model
	}
	if chunk.Usage != nil {
		t.usage = chunk.Usage
	}
	var events []sseEvent
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			events = append(events, sseEvent{
				Event: "response.output_text.delta",
				Data: map[string]any{
					"type":          "response.output_text.delta",
					"response_id":   firstNonEmpty(t.id, "resp_stream"),
					"item_id":       "msg_" + firstNonEmpty(t.id, "stream"),
					"output_index":  0,
					"content_index": choice.Index,
					"delta":         choice.Delta.Content,
				},
			})
		}
		if choice.FinishReason != "" {
			events = append(events, t.completedEvents()...)
		}
	}
	return events
}

func (t *responsesStreamTranslator) completedEvents() []sseEvent {
	if t.completed {
		return []sseEvent{{Done: true}}
	}
	t.completed = true
	usage := map[string]int{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	if t.usage != nil {
		usage = map[string]int{"input_tokens": t.usage.PromptTokens, "output_tokens": t.usage.CompletionTokens, "total_tokens": t.usage.TotalTokens}
	}
	return []sseEvent{
		{Event: "response.completed", Data: map[string]any{
			"type":       "response.completed",
			"response":   map[string]any{"id": firstNonEmpty(t.id, "resp_stream"), "object": "response", "status": "completed", "model": t.model, "usage": usage},
			"usage":      usage,
			"created_at": timeNowUnix(),
		}},
		{Done: true},
	}
}

type messagesStreamTranslator struct {
	id         string
	model      string
	usage      provider.Usage
	started    bool
	blockOpen  bool
	completed  bool
	stopReason string
}

func newMessagesStreamTranslator() *messagesStreamTranslator { return &messagesStreamTranslator{} }

func (t *messagesStreamTranslator) Translate(payload string) []sseEvent {
	if payload == "[DONE]" {
		return t.completedEvents()
	}
	var chunk provider.ChatResponseChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return nil
	}
	if chunk.ID != "" {
		t.id = chunk.ID
	}
	if chunk.Model != "" {
		t.model = chunk.Model
	}
	if chunk.Usage != nil {
		t.usage = *chunk.Usage
	}
	var events []sseEvent
	if !t.started {
		t.started = true
		events = append(events, sseEvent{Event: "message_start", Data: map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": t.id, "type": "message", "role": "assistant", "model": t.model,
				"content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": t.usage.PromptTokens, "output_tokens": 0},
			},
		}})
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			if !t.blockOpen {
				t.blockOpen = true
				events = append(events, sseEvent{Event: "content_block_start", Data: map[string]any{
					"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""},
				}})
			}
			events = append(events, sseEvent{Event: "content_block_delta", Data: map[string]any{
				"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": choice.Delta.Content},
			}})
		}
		if choice.FinishReason != "" {
			t.stopReason = mapOpenAIStopReason(choice.FinishReason)
			events = append(events, t.completedEvents()...)
		}
	}
	return events
}

func (t *messagesStreamTranslator) completedEvents() []sseEvent {
	if t.completed {
		return nil
	}
	t.completed = true
	var events []sseEvent
	if t.blockOpen {
		events = append(events, sseEvent{Event: "content_block_stop", Data: map[string]any{"type": "content_block_stop", "index": 0}})
	}
	if t.stopReason == "" {
		t.stopReason = "end_turn"
	}
	events = append(events,
		sseEvent{Event: "message_delta", Data: map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": t.stopReason}, "usage": map[string]int{"output_tokens": t.usage.CompletionTokens}}},
		sseEvent{Event: "message_stop", Data: map[string]string{"type": "message_stop"}},
	)
	return events
}

func writeNamedSSE(w http.ResponseWriter, eventName string, payload any, done bool) {
	if done {
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	if eventName != "" {
		fmt.Fprintf(w, "event: %s\n", eventName)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func mapOpenAIStopReason(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return "end_turn"
	}
}

func timeNowUnix() int64 {
	return time.Now().Unix()
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
