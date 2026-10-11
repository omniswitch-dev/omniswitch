package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// openAIMessage mirrors the minimal fields the harness payloads (P1-P4)
// need; it is deliberately not a full OpenAI schema implementation.
type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

func (s *server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	var req openAIChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}

	apiKey := bearerToken(r)
	s.stats.recordRequest("/v1/chat/completions", req.Model, apiKey)

	faultType, injected := s.resolveFault(r)
	if injected {
		if delay := stallOrSlowDelay(faultType); delay > 0 {
			time.Sleep(delay)
		}
		// "midstream-disconnect" only makes sense once a response has
		// started; a non-streaming request gets it as an immediate abort
		// instead. Every other fault type is handled here and returns.
		if faultType == "midstream-disconnect" && !req.Stream {
			s.stats.recordFault("midstream-disconnect")
			hijackAndReset(w)
			return
		}
		if faultType != "midstream-disconnect" && s.applyFault(w, faultType) {
			return
		}
	}

	time.Sleep(s.requestDelay(r))
	s.stats.recordStream(req.Stream)
	if req.Stream {
		s.streamOpenAIChatWithFault(w, r, req, body, faultType)
	} else {
		s.writeOpenAIChatNonStream(w, r, req, body)
	}
}

func (s *server) writeOpenAIChatNonStream(w http.ResponseWriter, r *http.Request, req openAIChatRequest, rawBody []byte) {
	seed := requestSeed(req.Model, rawBody)
	n := s.outputTokens(r)
	content := seededTokens(seed, n)
	promptTokens := estimatePromptTokens(req.Messages)

	resp := map[string]any{
		"id":      "chatcmpl-mock-" + shortHex(seed),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     promptTokens,
			"completion_tokens": n,
			"total_tokens":      promptTokens + n,
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// streamOpenAIChatWithFault emits the streaming response as
// cfg.StreamChunks fixed-cadence SSE events (see bench/cmd/sse-probe,
// which validates this cadence against the direct baseline within 1ms per
// the benchmark plan section 3.4). If faultType is "midstream-disconnect",
// the connection is hijacked and closed partway through the stream.
func (s *server) streamOpenAIChatWithFault(w http.ResponseWriter, r *http.Request, req openAIChatRequest, rawBody []byte, faultType string) {
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
	id := "chatcmpl-mock-" + shortHex(seed)
	created := time.Now().Unix()

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

		finishReason := any(nil)
		if i == chunks-1 {
			finishReason = "stop"
		}
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   req.Model,
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]string{
						"content": delta,
					},
					"finish_reason": finishReason,
				},
			},
		}
		writeSSEData(w, chunk)
		flusher.Flush()
		if i < chunks-1 && interval > 0 {
			time.Sleep(interval)
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeSSEData(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	if k := r.Header.Get("x-api-key"); k != "" {
		return k
	}
	return ""
}

func estimatePromptTokens(msgs []openAIMessage) int {
	total := 0
	for _, m := range msgs {
		// Deliberately crude: word count, not a real tokenizer. The
		// harness never asserts exact token accounting against the
		// mock, only that it is present and stable.
		total += len(strings.Fields(m.Content)) + 1
	}
	if total == 0 {
		total = 1
	}
	return total
}

func shortHex(seed []byte) string {
	return strconv.FormatUint(uint64(len(seed)), 16) + fmt.Sprintf("%x", seed[:min(8, len(seed))])
}
