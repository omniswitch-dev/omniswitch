package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicMessagesRequest struct {
	Model     string             `json:"model"`
	Messages  []anthropicMessage `json:"messages"`
	Stream    bool               `json:"stream"`
	MaxTokens int                `json:"max_tokens,omitempty"`
}

func (s *server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var req anthropicMessagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}

	apiKey := r.Header.Get("x-api-key")
	s.stats.recordRequest("/v1/messages", req.Model, apiKey)

	faultType, injected := s.resolveFault(r)
	if injected {
		if delay := stallOrSlowDelay(faultType); delay > 0 {
			time.Sleep(delay)
		}
		if faultType == "midstream-disconnect" && !req.Stream {
			s.stats.recordFault("midstream-disconnect")
			hijackAndReset(w)
			return
		}
		if faultType != "midstream-disconnect" && s.applyAnthropicFault(w, faultType) {
			return
		}
	}

	time.Sleep(s.requestDelay(r))
	s.stats.recordStream(req.Stream)
	if req.Stream {
		s.streamAnthropicMessage(w, r, req, body, faultType)
	} else {
		s.writeAnthropicMessageNonStream(w, r, req, body)
	}
}

// applyAnthropicFault mirrors applyFault but with Anthropic's error body
// shape ({"type":"error","error":{"type":...,"message":...}}).
func (s *server) applyAnthropicFault(w http.ResponseWriter, faultType string) bool {
	errBody := func(status int, errType, msg string) {
		if errType == "rate_limit_error" {
			w.Header().Set("Retry-After", "1")
		}
		writeJSON(w, status, map[string]any{
			"type": "error",
			"error": map[string]string{
				"type":    errType,
				"message": msg,
			},
		})
	}
	switch faultType {
	case "429":
		s.stats.recordFault("429")
		errBody(http.StatusTooManyRequests, "rate_limit_error", "mock-upstream: injected 429 rate limit")
		return true
	case "500":
		s.stats.recordFault("500")
		errBody(http.StatusInternalServerError, "api_error", "mock-upstream: injected 500 internal error")
		return true
	case "503":
		s.stats.recordFault("503")
		errBody(http.StatusServiceUnavailable, "overloaded_error", "mock-upstream: injected 503 unavailable")
		return true
	case "reset":
		s.stats.recordFault("reset")
		hijackAndReset(w)
		return true
	default:
		return false
	}
}

func (s *server) writeAnthropicMessageNonStream(w http.ResponseWriter, r *http.Request, req anthropicMessagesRequest, rawBody []byte) {
	seed := requestSeed(req.Model, rawBody)
	n := s.outputTokens(r)
	content := seededTokens(seed, n)
	promptTokens := estimateAnthropicPromptTokens(req.Messages)

	writeJSON(w, http.StatusOK, map[string]any{
		"id":            "msg-mock-" + shortHex(seed),
		"type":          "message",
		"role":          "assistant",
		"model":         req.Model,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"content": []map[string]string{
			{"type": "text", "text": content},
		},
		"usage": map[string]int{
			"input_tokens":  promptTokens,
			"output_tokens": n,
		},
	})
}

// streamAnthropicMessage emits the Anthropic Messages streaming event
// sequence (message_start, content_block_start, N x content_block_delta,
// content_block_stop, message_delta, message_stop) at the configured fixed
// chunk cadence, matching the same cfg.StreamChunks/StreamChunkInterval
// knobs used by the OpenAI-shaped stream so S4 is directly comparable
// across both request shapes.
func (s *server) streamAnthropicMessage(w http.ResponseWriter, r *http.Request, req anthropicMessagesRequest, rawBody []byte, faultType string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	seed := requestSeed(req.Model, rawBody)
	n := s.outputTokens(r)
	chunks := s.streamChunks(r)
	interval := s.streamChunkInterval(r)
	tokens := strings.Split(seededTokens(seed, n), " ")
	id := "msg-mock-" + shortHex(seed)

	writeSSEEvent(w, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":    id,
			"type":  "message",
			"role":  "assistant",
			"model": req.Model,
			"usage": map[string]int{"input_tokens": estimateAnthropicPromptTokens(req.Messages), "output_tokens": 0},
		},
	})
	writeSSEEvent(w, "content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]string{
			"type": "text",
			"text": "",
		},
	})
	flusher.Flush()

	disconnectAt := -1
	if faultType == "midstream-disconnect" {
		disconnectAt = chunks / 2
	}

	for i := 0; i < chunks; i++ {
		if i == disconnectAt {
			s.stats.recordFault("midstream-disconnect")
			hijackAndReset(w)
			return
		}
		start := (i * len(tokens)) / chunks
		end := ((i + 1) * len(tokens)) / chunks
		delta := strings.Join(tokens[start:end], " ")
		writeSSEEvent(w, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]string{
				"type": "text_delta",
				"text": delta,
			},
		})
		flusher.Flush()
		if i < chunks-1 && interval > 0 {
			time.Sleep(interval)
		}
	}

	writeSSEEvent(w, "content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	})
	writeSSEEvent(w, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]string{"stop_reason": "end_turn"},
		"usage": map[string]int{"output_tokens": n},
	})
	writeSSEEvent(w, "message_stop", map[string]any{"type": "message_stop"})
	flusher.Flush()
}

func writeSSEEvent(w io.Writer, event string, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

func estimateAnthropicPromptTokens(msgs []anthropicMessage) int {
	total := 0
	for _, m := range msgs {
		total += len(strings.Fields(m.Content)) + 1
	}
	if total == 0 {
		total = 1
	}
	return total
}
