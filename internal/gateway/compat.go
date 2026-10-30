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
	chatRequest, err := request.toChatRequest()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
	content := []any{}
	stopReason := "end_turn"
	if len(chat.Choices) > 0 {
		message := chat.Choices[0].Message
		if message.Content != "" || len(message.ToolCalls) == 0 {
			content = append(content, map[string]any{"type": "text", "text": message.Content})
		}
		for _, call := range message.ToolCalls {
			input, err := toolArgumentsObject(call.Function.Arguments)
			if err != nil {
				writeError(w, http.StatusBadGateway, "upstream returned invalid tool arguments")
				return
			}
			content = append(content, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input})
		}
		stopReason = mapOpenAIStopReason(chat.Choices[0].FinishReason)
		if len(message.ToolCalls) > 0 && stopReason == "end_turn" {
			stopReason = "tool_use"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            chat.ID,
		"type":          "message",
		"role":          "assistant",
		"model":         chat.Model,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"content":       content,
		"usage": map[string]int{
			"input_tokens":  chat.Usage.PromptTokens,
			"output_tokens": chat.Usage.CompletionTokens,
		},
	})
}

type responsesRequest struct {
	Model              string               `json:"model"`
	Input              json.RawMessage      `json:"input"`
	Instructions       string               `json:"instructions,omitempty"`
	Temperature        *float64             `json:"temperature,omitempty"`
	MaxOutputTokens    *int                 `json:"max_output_tokens,omitempty"`
	TopP               *float64             `json:"top_p,omitempty"`
	Stream             bool                 `json:"stream,omitempty"`
	Tools              []responsesTool      `json:"tools,omitempty"`
	ToolChoice         json.RawMessage      `json:"tool_choice,omitempty"`
	ParallelToolCalls  *bool                `json:"parallel_tool_calls,omitempty"`
	Text               *responsesTextConfig `json:"text,omitempty"`
	PreviousResponseID string               `json:"previous_response_id,omitempty"`
	Conversation       json.RawMessage      `json:"conversation,omitempty"`
	Store              *bool                `json:"store,omitempty"`
	Background         bool                 `json:"background,omitempty"`
}

type responsesTextConfig struct {
	Format *struct {
		Type        string `json:"type"`
		Name        string `json:"name,omitempty"`
		Description string `json:"description,omitempty"`
		Schema      any    `json:"schema,omitempty"`
		Strict      *bool  `json:"strict,omitempty"`
	} `json:"format,omitempty"`
}

// responsesTool is a Responses API tool definition. Only function tools can
// be expressed as chat-completions tools.
type responsesTool struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
	Strict      *bool  `json:"strict,omitempty"`
}

func (r responsesRequest) toChatRequest() (provider.ChatRequest, error) {
	if strings.TrimSpace(r.Model) == "" {
		return provider.ChatRequest{}, fmt.Errorf("model is required")
	}
	if r.PreviousResponseID != "" || (len(r.Conversation) > 0 && string(r.Conversation) != "null") || r.Background || (r.Store != nil && *r.Store) {
		return provider.ChatRequest{}, fmt.Errorf("stored responses, conversations, and background mode are unsupported; send the complete input history with store:false")
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
	request.ParallelToolCalls = r.ParallelToolCalls
	if r.Text != nil && r.Text.Format != nil {
		format := r.Text.Format
		switch format.Type {
		case "text", "json_object":
			request.ResponseFormat = &provider.ResponseFormat{Type: format.Type}
		case "json_schema":
			if strings.TrimSpace(format.Name) == "" || format.Schema == nil {
				return provider.ChatRequest{}, fmt.Errorf("text.format json_schema requires name and schema")
			}
			request.ResponseFormat = &provider.ResponseFormat{Type: "json_schema", JSONSchema: &provider.JSONSchemaSpec{Name: format.Name, Description: format.Description, Schema: format.Schema, Strict: format.Strict}}
		default:
			return provider.ChatRequest{}, fmt.Errorf("unsupported text.format type %q", format.Type)
		}
	}
	for _, tool := range r.Tools {
		if tool.Type != "function" || strings.TrimSpace(tool.Name) == "" {
			return provider.ChatRequest{}, fmt.Errorf("unsupported tool %q of type %q: only named function tools are supported", tool.Name, tool.Type)
		}
		request.Tools = append(request.Tools, provider.Tool{
			Type:     "function",
			Function: provider.ToolFunction{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters, Strict: tool.Strict},
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
		switch mode {
		case "auto", "none", "required":
			return mode, nil
		default:
			return nil, fmt.Errorf("unsupported tool_choice %q", mode)
		}
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
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Output    json.RawMessage `json:"output"`
}

// responsesContentPart is one typed content item of a Responses message.
type responsesContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ImageURL json.RawMessage `json:"image_url"`
	Detail   string          `json:"detail"`
}

func responseInputMessages(raw json.RawMessage) ([]provider.Message, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, fmt.Errorf("input is required")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []provider.Message{{Role: "user", Content: text}}, nil
	}
	var items []responsesInputItem
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return nil, fmt.Errorf("input must be a string or message array")
	}
	messages := make([]provider.Message, 0, len(items))
	calls := make(map[string]bool)
	for _, item := range items {
		switch item.Type {
		case "function_call":
			if strings.TrimSpace(item.CallID) == "" || strings.TrimSpace(item.Name) == "" {
				return nil, fmt.Errorf("function_call requires call_id and name")
			}
			if _, exists := calls[item.CallID]; exists {
				return nil, fmt.Errorf("duplicate function call_id %q", item.CallID)
			}
			if _, err := toolArgumentsObject(item.Arguments); err != nil {
				return nil, err
			}
			calls[item.CallID] = false
			call := provider.ToolCall{ID: item.CallID, Type: "function", Function: provider.FunctionCall{Name: item.Name, Arguments: item.Arguments}}
			if n := len(messages); n > 0 && messages[n-1].Role == "assistant" {
				messages[n-1].ToolCalls = append(messages[n-1].ToolCalls, call)
			} else {
				messages = append(messages, provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{call}})
			}
			continue
		case "function_call_output":
			resolved, exists := calls[item.CallID]
			if !exists || resolved {
				return nil, fmt.Errorf("function_call_output requires an unresolved call_id from the input history")
			}
			var output string
			if err := json.Unmarshal(item.Output, &output); err != nil || string(item.Output) == "null" {
				return nil, fmt.Errorf("function_call_output output must be a string")
			}
			calls[item.CallID] = true
			messages = append(messages, provider.Message{Role: "tool", ToolCallID: item.CallID, Content: output})
			continue
		case "", "message":
		default:
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
	switch message.Role {
	case "user", "assistant", "system", "developer":
	default:
		return provider.Message{}, fmt.Errorf("unsupported message role %q", message.Role)
	}
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

// streamToolCall accumulates one streamed tool call. Chat-completions
// streams send the id and function name on the first delta of a call and
// append argument fragments on later deltas with the same index.
type streamToolCall struct {
	id        string
	name      string
	arguments strings.Builder
	// position is the output_index (Responses) or content block index
	// (Messages) assigned when the call was first seen.
	position int
}

// streamToolCalls collects tool-call deltas keyed by their stream index, in
// the order they were first seen.
type streamToolCalls struct {
	calls   []*streamToolCall
	byIndex map[int]*streamToolCall
}

// add merges one delta. It returns the call and whether the delta opened
// it. A delta without an index continues the most recent call unless it
// carries a new id.
func (s *streamToolCalls) add(delta provider.ToolCall, position int) (*streamToolCall, bool) {
	if s.byIndex == nil {
		s.byIndex = make(map[int]*streamToolCall)
	}
	var call *streamToolCall
	if delta.Index != nil {
		call = s.byIndex[*delta.Index]
	} else if n := len(s.calls); n > 0 && (delta.ID == "" || delta.ID == s.calls[n-1].id) {
		call = s.calls[n-1]
	}
	opened := false
	if call == nil {
		call = &streamToolCall{position: position}
		index := len(s.calls)
		if delta.Index != nil {
			index = *delta.Index
		}
		s.byIndex[index] = call
		s.calls = append(s.calls, call)
		opened = true
	}
	if call.id == "" {
		call.id = delta.ID
	}
	if call.name == "" {
		call.name = delta.Function.Name
	}
	call.arguments.WriteString(delta.Function.Arguments)
	return call, opened
}

type responsesStreamTranslator struct {
	id        string
	model     string
	usage     *provider.Usage
	started   bool
	completed bool
	// text is the streamed assistant text; messageIndex is the output_index
	// of the message item (-1 until the first text delta).
	text         strings.Builder
	messageIndex int
	outputs      int
	toolCalls    streamToolCalls
	sequence     int
}

func newResponsesStreamTranslator() *responsesStreamTranslator {
	return &responsesStreamTranslator{messageIndex: -1}
}

func (t *responsesStreamTranslator) Translate(payload string) []sseEvent {
	events := t.translate(payload)
	for _, event := range events {
		if data, ok := event.Data.(map[string]any); ok {
			data["sequence_number"] = t.sequence
			t.sequence++
		}
	}
	return events
}

func (t *responsesStreamTranslator) translate(payload string) []sseEvent {
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
			"response": map[string]any{"id": firstNonEmpty(t.id, "resp_stream"), "object": "response", "status": "in_progress", "model": t.model, "output": []any{}},
		}})
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			if t.messageIndex < 0 {
				t.messageIndex = t.outputs
				t.outputs++
				events = append(events,
					sseEvent{Event: "response.output_item.added", Data: map[string]any{
						"type": "response.output_item.added", "output_index": t.messageIndex,
						"item": map[string]any{"id": t.messageItemID(), "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}},
					}},
					sseEvent{Event: "response.content_part.added", Data: map[string]any{
						"type": "response.content_part.added", "item_id": t.messageItemID(), "output_index": t.messageIndex, "content_index": 0,
						"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
					}},
				)
			}
			t.text.WriteString(choice.Delta.Content)
			events = append(events, sseEvent{
				Event: "response.output_text.delta",
				Data: map[string]any{
					"type":          "response.output_text.delta",
					"response_id":   firstNonEmpty(t.id, "resp_stream"),
					"item_id":       t.messageItemID(),
					"output_index":  t.messageIndex,
					"content_index": 0,
					"delta":         choice.Delta.Content,
				},
			})
		}
		for _, delta := range choice.Delta.ToolCalls {
			call, opened := t.toolCalls.add(delta, t.outputs)
			if opened {
				t.outputs++
				events = append(events, sseEvent{Event: "response.output_item.added", Data: map[string]any{
					"type":         "response.output_item.added",
					"response_id":  firstNonEmpty(t.id, "resp_stream"),
					"output_index": call.position,
					"item":         t.functionCallItem(call, "in_progress", ""),
				}})
			}
			if delta.Function.Arguments != "" {
				events = append(events, sseEvent{Event: "response.function_call_arguments.delta", Data: map[string]any{
					"type":         "response.function_call_arguments.delta",
					"response_id":  firstNonEmpty(t.id, "resp_stream"),
					"item_id":      t.functionCallItemID(call),
					"output_index": call.position,
					"delta":        delta.Function.Arguments,
				}})
			}
		}
	}
	// Completion waits for [DONE]: the usage chunk arrives after the
	// finish_reason chunk.
	return events
}

func (t *responsesStreamTranslator) messageItemID() string {
	return "msg_" + firstNonEmpty(t.id, "stream")
}

// functionCallItemID matches the non-stream path ("fc_" + call id) and
// falls back to the output index when the upstream sent no call id.
func (t *responsesStreamTranslator) functionCallItemID(call *streamToolCall) string {
	if call.id != "" {
		return "fc_" + call.id
	}
	return fmt.Sprintf("fc_%s_%d", firstNonEmpty(t.id, "stream"), call.position)
}

func (t *responsesStreamTranslator) functionCallItem(call *streamToolCall, status, arguments string) map[string]any {
	return map[string]any{
		"id":        t.functionCallItemID(call),
		"type":      "function_call",
		"status":    status,
		"call_id":   call.id,
		"name":      call.name,
		"arguments": arguments,
	}
}

func (t *responsesStreamTranslator) completedEvents() []sseEvent {
	t.completed = true
	usage := map[string]int{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	if t.usage != nil {
		usage = map[string]int{"input_tokens": t.usage.PromptTokens, "output_tokens": t.usage.CompletionTokens, "total_tokens": t.usage.TotalTokens}
	}
	response := map[string]any{"id": firstNonEmpty(t.id, "resp_stream"), "object": "response", "status": "completed", "model": t.model, "usage": usage}
	var events []sseEvent
	// The output list mirrors the non-stream response: the message
	// item (when text was streamed) followed by the function calls,
	// ordered by output_index.
	output := make([]any, t.outputs)
	if t.messageIndex >= 0 {
		part := map[string]any{"type": "output_text", "text": t.text.String(), "annotations": []any{}}
		item := map[string]any{
			"id": t.messageItemID(), "type": "message", "role": "assistant", "status": "completed",
			"content": []any{part},
		}
		output[t.messageIndex] = item
		events = append(events,
			sseEvent{Event: "response.output_text.done", Data: map[string]any{"type": "response.output_text.done", "item_id": t.messageItemID(), "output_index": t.messageIndex, "content_index": 0, "text": t.text.String()}},
			sseEvent{Event: "response.content_part.done", Data: map[string]any{"type": "response.content_part.done", "item_id": t.messageItemID(), "output_index": t.messageIndex, "content_index": 0, "part": part}},
			sseEvent{Event: "response.output_item.done", Data: map[string]any{"type": "response.output_item.done", "output_index": t.messageIndex, "item": item}},
		)
	}
	for _, call := range t.toolCalls.calls {
		arguments := call.arguments.String()
		item := t.functionCallItem(call, "completed", arguments)
		output[call.position] = item
		events = append(events,
			sseEvent{Event: "response.function_call_arguments.done", Data: map[string]any{
				"type":         "response.function_call_arguments.done",
				"response_id":  firstNonEmpty(t.id, "resp_stream"),
				"item_id":      t.functionCallItemID(call),
				"output_index": call.position,
				"arguments":    arguments,
			}},
			sseEvent{Event: "response.output_item.done", Data: map[string]any{
				"type":         "response.output_item.done",
				"response_id":  firstNonEmpty(t.id, "resp_stream"),
				"output_index": call.position,
				"item":         item,
			}},
		)
	}
	response["output"] = output
	return append(events,
		sseEvent{Event: "response.completed", Data: map[string]any{
			"type":       "response.completed",
			"response":   response,
			"usage":      usage,
			"created_at": timeNowUnix(),
		}},
		sseEvent{Done: true},
	)
}

type messagesStreamTranslator struct {
	id         string
	model      string
	usage      provider.Usage
	started    bool
	completed  bool
	stopReason string
	// finishReason is the raw chat-completions finish reason.
	finishReason string
	// blocks is the number of content blocks started so far; textBlock is
	// the index of the open text block (-1 when none is open).
	blocks    int
	textBlock int
	open      []int
	toolCalls streamToolCalls
}

func newMessagesStreamTranslator() *messagesStreamTranslator {
	return &messagesStreamTranslator{textBlock: -1}
}

func (t *messagesStreamTranslator) messageStart() sseEvent {
	return sseEvent{Event: "message_start", Data: map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": t.id, "type": "message", "role": "assistant", "model": t.model,
			"content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": t.usage.PromptTokens, "output_tokens": 0},
		},
	}}
}

// closeTextBlock stops the open text block so a following tool_use block
// starts at the next index, as Anthropic streams do.
func (t *messagesStreamTranslator) closeTextBlock() []sseEvent {
	if t.textBlock < 0 {
		return nil
	}
	index := t.textBlock
	t.textBlock = -1
	for i, open := range t.open {
		if open == index {
			t.open = append(t.open[:i], t.open[i+1:]...)
			break
		}
	}
	return []sseEvent{{Event: "content_block_stop", Data: map[string]any{"type": "content_block_stop", "index": index}}}
}

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
		events = append(events, t.messageStart())
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			if t.textBlock < 0 {
				t.textBlock = t.blocks
				t.blocks++
				t.open = append(t.open, t.textBlock)
				events = append(events, sseEvent{Event: "content_block_start", Data: map[string]any{
					"type": "content_block_start", "index": t.textBlock, "content_block": map[string]string{"type": "text", "text": ""},
				}})
			}
			events = append(events, sseEvent{Event: "content_block_delta", Data: map[string]any{
				"type": "content_block_delta", "index": t.textBlock, "delta": map[string]string{"type": "text_delta", "text": choice.Delta.Content},
			}})
		}
		for _, delta := range choice.Delta.ToolCalls {
			events = append(events, t.closeTextBlock()...)
			call, opened := t.toolCalls.add(delta, t.blocks)
			if opened {
				t.blocks++
				t.open = append(t.open, call.position)
				id := call.id
				if id == "" {
					id = fmt.Sprintf("toolu_%s_%d", firstNonEmpty(t.id, "stream"), call.position)
				}
				events = append(events, sseEvent{Event: "content_block_start", Data: map[string]any{
					"type": "content_block_start", "index": call.position,
					"content_block": map[string]any{"type": "tool_use", "id": id, "name": call.name, "input": map[string]any{}},
				}})
			}
			if delta.Function.Arguments != "" {
				events = append(events, sseEvent{Event: "content_block_delta", Data: map[string]any{
					"type": "content_block_delta", "index": call.position,
					"delta": map[string]string{"type": "input_json_delta", "partial_json": delta.Function.Arguments},
				}})
			}
		}
		if choice.FinishReason != "" {
			// Completion waits for [DONE] so the trailing usage chunk is
			// included in message_delta.
			t.finishReason = choice.FinishReason
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
		events = append(events, t.messageStart())
	}
	for _, index := range t.open {
		events = append(events, sseEvent{Event: "content_block_stop", Data: map[string]any{"type": "content_block_stop", "index": index}})
	}
	t.open = nil
	t.textBlock = -1
	if t.stopReason == "" {
		t.stopReason = "end_turn"
	}
	if len(t.toolCalls.calls) > 0 && (t.finishReason == "" || t.finishReason == "stop") {
		// Some upstreams finish a tool-calling turn with "stop".
		t.stopReason = "tool_use"
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
