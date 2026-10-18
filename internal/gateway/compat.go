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
// translates the text/message subset (plus function tools and image inputs)
// to OmniSwitch's common chat pipeline, so budgets, guardrails, cache
// isolation, logging, and routing are identical.
func (h *Handler) Responses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	r, cfg := h.withSettings(r)
	r.Body = http.MaxBytesReader(w, r.Body, cfg.maxRequestBytes)
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
	writeJSON(w, http.StatusOK, responsesFromChat(chat))
}

func responsesFromChat(chat provider.ChatResponse) map[string]any {
	output := []any{}
	if len(chat.Choices) > 0 {
		message := chat.Choices[0].Message
		if message.Content != "" || len(message.ToolCalls) == 0 {
			output = append(output, map[string]any{
				"id":      "msg_" + chat.ID,
				"type":    "message",
				"role":    "assistant",
				"status":  "completed",
				"content": []any{map[string]any{"type": "output_text", "text": message.Content, "annotations": []any{}}},
			})
		}
		for _, call := range message.ToolCalls {
			output = append(output, map[string]any{
				"id":        "fc_" + call.ID,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   call.ID,
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			})
		}
	}
	return map[string]any{
		"id":         strings.Replace(chat.ID, "chat_", "resp_", 1),
		"object":     "response",
		"created_at": chat.Created,
		"status":     "completed",
		"model":      chat.Model,
		"output":     output,
		"usage": map[string]int{
			"input_tokens":  chat.Usage.PromptTokens,
			"output_tokens": chat.Usage.CompletionTokens,
			"total_tokens":  chat.Usage.TotalTokens,
		},
	}
}

// Messages accepts the core Anthropic Messages shape and returns its response
// shape while using the same provider-neutral OmniSwitch request pipeline.
func (h *Handler) Messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	r, cfg := h.withSettings(r)
	r.Body = http.MaxBytesReader(w, r.Body, cfg.maxRequestBytes)
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
		stopReason = mapOpenAIStopReason(chat.Choices[0].FinishReason)
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
	Tools           []responsesTool `json:"tools,omitempty"`
	ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
}

// responsesTool is a Responses API tool definition. Only function tools can
// be expressed as chat-completions tools.
type responsesTool struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
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
	request := provider.ChatRequest{Model: r.Model, Messages: messages, Temperature: r.Temperature, MaxTokens: r.MaxOutputTokens, TopP: r.TopP}
	for _, tool := range r.Tools {
		if tool.Type != "function" || strings.TrimSpace(tool.Name) == "" {
			return provider.ChatRequest{}, fmt.Errorf("unsupported tool %q of type %q: only named function tools are supported", tool.Name, tool.Type)
		}
		request.Tools = append(request.Tools, provider.Tool{
			Type:     "function",
			Function: provider.ToolFunction{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters},
		})
	}
	if choice, err := responsesToolChoice(r.ToolChoice); err != nil {
		return provider.ChatRequest{}, err
	} else if choice != nil {
		request.ToolChoice = choice
	}
	return request, nil
}

func responsesToolChoice(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var mode string
	if err := json.Unmarshal(raw, &mode); err == nil {
		return mode, nil
	}
	var named struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &named); err != nil || named.Type != "function" || named.Name == "" {
		return nil, fmt.Errorf("unsupported tool_choice: only auto, none, required, or a named function are supported")
	}
	return map[string]any{"type": "function", "function": map[string]string{"name": named.Name}}, nil
}

// responsesInputItem is one item of a Responses API input array.
type responsesInputItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// responsesContentPart is one typed content item of a Responses message.
type responsesContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ImageURL json.RawMessage `json:"image_url"`
	Detail   string          `json:"detail"`
}

func responseInputMessages(raw json.RawMessage) ([]provider.Message, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []provider.Message{{Role: "user", Content: text}}, nil
	}
	var items []responsesInputItem
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return nil, fmt.Errorf("input must be a string or message array")
	}
	messages := make([]provider.Message, 0, len(items))
	for _, item := range items {
		if item.Type != "" && item.Type != "message" {
			return nil, fmt.Errorf("unsupported input item type %q", item.Type)
		}
		message, err := responsesInputMessage(item)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func responsesInputMessage(item responsesInputItem) (provider.Message, error) {
	message := provider.Message{Role: firstNonEmpty(item.Role, "user")}
	if len(item.Content) == 0 || string(item.Content) == "null" {
		return message, nil
	}
	var text string
	if err := json.Unmarshal(item.Content, &text); err == nil {
		message.Content = text
		return message, nil
	}
	var parts []responsesContentPart
	if err := json.Unmarshal(item.Content, &parts); err != nil {
		return provider.Message{}, fmt.Errorf("input message content must be a string or content array")
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "input_text", "output_text", "text":
			message.ContentParts = append(message.ContentParts, provider.ContentPart{Type: "text", Text: part.Text})
			texts = append(texts, part.Text)
		case "input_image", "image_url":
			image, err := responsesImageURL(part)
			if err != nil {
				return provider.Message{}, err
			}
			message.ContentParts = append(message.ContentParts, provider.ContentPart{Type: "image_url", ImageURL: image})
		default:
			return provider.Message{}, fmt.Errorf("unsupported input content type %q", part.Type)
		}
	}
	message.Content = strings.Join(texts, "\n")
	return message, nil
}

// responsesImageURL accepts the Responses form ("image_url": "<url>") and the
// chat-completions form ("image_url": {"url": "<url>"}).
func responsesImageURL(part responsesContentPart) (*provider.ImageURL, error) {
	var url string
	if err := json.Unmarshal(part.ImageURL, &url); err == nil && url != "" {
		return &provider.ImageURL{URL: url, Detail: part.Detail}, nil
	}
	var image provider.ImageURL
	if err := json.Unmarshal(part.ImageURL, &image); err == nil && image.URL != "" {
		if image.Detail == "" {
			image.Detail = part.Detail
		}
		return &image, nil
	}
	return nil, fmt.Errorf("input_image requires an image_url")
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
	request.Stream = true
	// The translated formats report usage in their final events, so ask the
	// chat pipeline to forward the usage chunk.
	request.StreamOptions = &provider.StreamOptions{IncludeUsage: true}
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

// transformResponseWriter translates the chat-completions SSE stream written
// by ChatCompletions into another streaming format. Non-stream responses
// (errors, guardrail denials) pass through unchanged.
type transformResponseWriter struct {
	target     http.ResponseWriter
	translator sseTranslator
	header     http.Header
	buffer     bytes.Buffer
	streaming  bool
	wrote      bool
}

func newTransformResponseWriter(target http.ResponseWriter, translator sseTranslator) *transformResponseWriter {
	return &transformResponseWriter{target: target, translator: translator, header: make(http.Header)}
}

func (w *transformResponseWriter) Header() http.Header { return w.header }

// WriteHeader copies the headers to the target exactly once and commits the
// status.
func (w *transformResponseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.streaming = strings.Contains(strings.ToLower(w.header.Get("Content-Type")), "text/event-stream")
	copyHeaders(w.target.Header(), w.header)
	if w.streaming {
		w.target.Header().Set("Content-Type", "text/event-stream")
		w.target.Header().Del("Content-Length")
	}
	w.target.WriteHeader(status)
}

func (w *transformResponseWriter) Write(data []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if !w.streaming {
		return w.target.Write(data)
	}
	w.buffer.Write(data)
	if err := w.translateCompleteEvents(); err != nil {
		return 0, err
	}
	return len(data), nil
}

// Flush emits every complete translated event and flushes the target.
func (w *transformResponseWriter) Flush() {
	if w.streaming {
		_ = w.translateCompleteEvents()
	}
	if flusher, ok := w.target.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *transformResponseWriter) translateCompleteEvents() error {
	for {
		pending := w.buffer.Bytes()
		index := bytes.Index(pending, []byte("\n\n"))
		if index < 0 {
			return nil
		}
		event := string(pending[:index])
		w.buffer.Next(index + 2)
		if err := w.writeTranslatedEvent(event); err != nil {
			return err
		}
	}
}

func (w *transformResponseWriter) writeTranslatedEvent(raw string) error {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		for _, event := range w.translator.Translate(payload) {
			if err := writeNamedSSE(w.target, event.Event, event.Data, event.Done); err != nil {
				return err
			}
		}
	}
	return nil
}

// streamErrorMessage extracts the message of a gateway stream error event
// ({"error":{"message":...}}). ok is false for ordinary chunks.
func streamErrorMessage(payload string) (string, bool) {
	if !strings.Contains(payload, `"error"`) {
		return "", false
	}
	var envelope struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil || envelope.Error == nil {
		return "", false
	}
	return firstNonEmpty(envelope.Error.Message, "upstream stream failed"), true
}

type responsesStreamTranslator struct {
	id        string
	model     string
	usage     *provider.Usage
	started   bool
	completed bool
}

func newResponsesStreamTranslator() *responsesStreamTranslator { return &responsesStreamTranslator{} }

func (t *responsesStreamTranslator) Translate(payload string) []sseEvent {
	if t.completed {
		return nil
	}
	if payload == "[DONE]" {
		return t.completedEvents()
	}
	if message, ok := streamErrorMessage(payload); ok {
		t.completed = true
		return []sseEvent{{Event: "response.failed", Data: map[string]any{
			"type": "response.failed",
			"response": map[string]any{
				"id": firstNonEmpty(t.id, "resp_stream"), "object": "response", "status": "failed", "model": t.model,
				"error": map[string]string{"code": "upstream_stream_error", "message": message},
			},
		}}}
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
	if !t.started {
		t.started = true
		events = append(events, sseEvent{Event: "response.created", Data: map[string]any{
			"type":     "response.created",
			"response": map[string]any{"id": firstNonEmpty(t.id, "resp_stream"), "object": "response", "status": "in_progress", "model": t.model},
		}})
	}
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
	}
	// Completion waits for [DONE]: the usage chunk arrives after the
	// finish_reason chunk.
	return events
}

func (t *responsesStreamTranslator) completedEvents() []sseEvent {
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
	if t.completed {
		return nil
	}
	if payload == "[DONE]" {
		return t.completedEvents()
	}
	if message, ok := streamErrorMessage(payload); ok {
		t.completed = true
		return []sseEvent{{Event: "error", Data: map[string]any{
			"type":  "error",
			"error": map[string]string{"type": "api_error", "message": message},
		}}}
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
			// Completion waits for [DONE] so the trailing usage chunk is
			// included in message_delta.
			t.stopReason = mapOpenAIStopReason(choice.FinishReason)
		}
	}
	return events
}

func (t *messagesStreamTranslator) completedEvents() []sseEvent {
	t.completed = true
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
	if t.blockOpen {
		events = append(events, sseEvent{Event: "content_block_stop", Data: map[string]any{"type": "content_block_stop", "index": 0}})
	}
	if t.stopReason == "" {
		t.stopReason = "end_turn"
	}
	events = append(events,
		sseEvent{Event: "message_delta", Data: map[string]any{
			"type":  "message_delta",
			"delta": map[string]string{"stop_reason": t.stopReason},
			"usage": map[string]int{"input_tokens": t.usage.PromptTokens, "output_tokens": t.usage.CompletionTokens},
		}},
		sseEvent{Event: "message_stop", Data: map[string]string{"type": "message_stop"}},
	)
	return events
}

func writeNamedSSE(w http.ResponseWriter, eventName string, payload any, done bool) error {
	if done {
		_, err := fmt.Fprint(w, "data: [DONE]\n\n")
		return err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	if eventName != "" {
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, data)
	} else {
		_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	}
	return err
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

// copyHeaders copies the source headers that destination does not set yet.
// Headers already present on destination win, and copying twice never
// duplicates a value.
func copyHeaders(destination, source http.Header) {
	for key, values := range source {
		if _, exists := destination[key]; exists {
			continue
		}
		destination[key] = append([]string(nil), values...)
	}
}
