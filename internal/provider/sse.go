package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// maxSSELineBytes bounds a single SSE line. Upstreams put a whole JSON
// event on one line; large tool-call arguments or base64 payloads can
// exceed bufio.Scanner's default 64 KiB and the previous 1 MiB cap.
const maxSSELineBytes = 32 << 20

// ErrStreamTruncated reports that an upstream stream ended without its
// terminal event, i.e. the answer the client received is incomplete. The
// router also reports it for a stream that closes without sending any
// chunk.
var ErrStreamTruncated = errors.New("upstream stream ended before completion")

// chunkEmitter sends chunks to the consumer and never blocks once the
// request context is done, so a client disconnect cannot leak the
// producing goroutine or keep the upstream connection open.
type chunkEmitter struct {
	ctx context.Context
	out chan<- ChatResponseChunk
}

// send delivers chunk and reports false when the context ended first.
func (e chunkEmitter) send(chunk ChatResponseChunk) bool {
	if e.ctx.Err() != nil {
		return false
	}
	select {
	case e.out <- chunk:
		return true
	case <-e.ctx.Done():
		return false
	}
}

// fail ends the stream with a terminal chunk that carries err and no
// choices (see ChatResponseChunk.Err). Callers return right after.
func (e chunkEmitter) fail(err error) {
	if err == nil {
		return
	}
	if ctxErr := e.ctx.Err(); ctxErr != nil {
		// The consumer is gone or timed out; nobody reads the error.
		return
	}
	e.send(ChatResponseChunk{Object: "chat.completion.chunk", Err: err})
}

// sseDataLines calls fn with the payload of every "data:" line of an SSE
// body. It returns fn's error, a scanner error, or nil at EOF. It stops
// early (returning nil) when fn returns errStopSSE.
func sseDataLines(body io.Reader, fn func(data []byte) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		text := strings.TrimSpace(string(line))
		if !strings.HasPrefix(text, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
		if data == "" {
			continue
		}
		if err := fn([]byte(data)); err != nil {
			if errors.Is(err, errStopSSE) {
				return nil
			}
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return fmt.Errorf("upstream stream event exceeds %d bytes: %w", maxSSELineBytes, err)
		}
		return fmt.Errorf("read upstream stream: %w", err)
	}
	return nil
}

var errStopSSE = errors.New("stop reading SSE")

// upstreamStreamError formats an error object an upstream sent inside a
// 200 stream (OpenAI-style {"error":{...}}, Anthropic "error" events,
// Gemini error objects).
func upstreamStreamError(providerName string, raw json.RawMessage) error {
	var detail struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Status  string `json:"status"`
	}
	msg := strings.TrimSpace(string(raw))
	if err := json.Unmarshal(raw, &detail); err == nil && detail.Message != "" {
		msg = detail.Message
		if kind := firstNonEmptyString(detail.Type, detail.Status); kind != "" {
			msg = kind + ": " + msg
		}
	} else {
		var text string
		if json.Unmarshal(raw, &text) == nil && text != "" {
			msg = text
		}
	}
	if len(msg) > maxErrorBodyBytes {
		msg = msg[:maxErrorBodyBytes] + "...(truncated)"
	}
	return fmt.Errorf("%s stream error: %s", providerName, msg)
}

// usageOnlyChunk is the final chunk every adapter emits: no choices, the
// accumulated usage. The gateway prices the request from it and forwards it
// only to clients that asked for stream_options.include_usage.
func usageOnlyChunk(id, model string, usage Usage) ChatResponseChunk {
	return ChatResponseChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ChunkChoice{},
		Usage:   &usage,
	}
}

// withStreamUsage returns a copy of req that asks an OpenAI-compatible
// upstream to append its usage chunk, whatever the client sent, so the
// gateway can always price streamed requests.
func withStreamUsage(req ChatRequest) ChatRequest {
	opts := StreamOptions{}
	if req.StreamOptions != nil {
		opts = *req.StreamOptions
	}
	opts.IncludeUsage = true
	req.StreamOptions = &opts
	return req
}

// openAIStreamEvent is one OpenAI-compatible SSE payload. Error is set by
// upstreams that report failures mid-stream; XGroq carries usage on older
// Groq deployments.
type openAIStreamEvent struct {
	ChatResponseChunk
	Error json.RawMessage `json:"error"`
	XGroq *struct {
		Usage *Usage `json:"usage"`
	} `json:"x_groq"`
}

// streamOpenAIEvents parses an OpenAI-compatible SSE body. Usage is removed
// from chunks with choices and re-emitted once in a final usage-only chunk.
// Upstream error events, malformed events, read errors and truncated
// streams end the channel with an Err chunk.
func streamOpenAIEvents(ctx context.Context, body io.ReadCloser, providerName string) <-chan ChatResponseChunk {
	chunks := make(chan ChatResponseChunk)
	go func() {
		defer close(chunks)
		defer body.Close()
		emit := chunkEmitter{ctx: ctx, out: chunks}
		var (
			usage       *Usage
			id, model   string
			done        bool
			sawFinish   bool
			stopEmitted bool
		)
		err := sseDataLines(body, func(data []byte) error {
			if string(data) == "[DONE]" {
				done = true
				return errStopSSE
			}
			var event openAIStreamEvent
			if err := json.Unmarshal(data, &event); err != nil {
				return fmt.Errorf("%s stream: malformed event: %w", providerName, err)
			}
			if len(event.Error) > 0 && string(event.Error) != "null" {
				return upstreamStreamError(providerName, event.Error)
			}
			chunk := event.ChatResponseChunk
			if chunk.ID != "" {
				id = chunk.ID
			}
			if chunk.Model != "" {
				model = chunk.Model
			}
			if chunk.Usage != nil {
				u := *chunk.Usage
				usage = &u
			} else if event.XGroq != nil && event.XGroq.Usage != nil {
				u := *event.XGroq.Usage
				usage = &u
			}
			chunk.Usage = nil
			if len(chunk.Choices) == 0 {
				return nil
			}
			for _, choice := range chunk.Choices {
				if choice.FinishReason != "" {
					sawFinish = true
				}
			}
			if !emit.send(chunk) {
				stopEmitted = true
				return errStopSSE
			}
			return nil
		})
		if stopEmitted {
			return
		}
		if err == nil && !done && !sawFinish {
			err = fmt.Errorf("%s: %w", providerName, ErrStreamTruncated)
		}
		if err != nil {
			emit.fail(err)
			return
		}
		if usage != nil {
			emit.send(usageOnlyChunk(id, model, *usage))
		}
	}()
	return chunks
}

// StreamFromResponse replays a complete response as a stream: a role chunk,
// word-sized content deltas, one tool-call delta, a finish chunk and a
// final usage-only chunk.
func StreamFromResponse(ctx context.Context, resp ChatResponse) <-chan ChatResponseChunk {
	chunks := make(chan ChatResponseChunk)
	go func() {
		defer close(chunks)
		emit := chunkEmitter{ctx: ctx, out: chunks}
		chunk := func(delta Message, finish string) ChatResponseChunk {
			return ChatResponseChunk{
				ID:                resp.ID,
				Object:            "chat.completion.chunk",
				Created:           time.Now().Unix(),
				Model:             resp.Model,
				SystemFingerprint: resp.SystemFingerprint,
				Choices:           []ChunkChoice{{Index: 0, Delta: delta, FinishReason: finish}},
			}
		}
		var message Message
		finishReason := finishStop
		if len(resp.Choices) > 0 {
			message = resp.Choices[0].Message
			if resp.Choices[0].FinishReason != "" {
				finishReason = resp.Choices[0].FinishReason
			}
		}
		role := message.Role
		if role == "" {
			role = "assistant"
		}
		if !emit.send(chunk(Message{Role: role, ReasoningContent: message.ReasoningContent, Refusal: message.Refusal}, "")) {
			return
		}
		content := message.Text()
		words := strings.Fields(content)
		if len(words) == 0 && content != "" {
			words = []string{content}
		}
		for i, word := range words {
			token := word
			if i < len(words)-1 {
				token += " "
			}
			if !emit.send(chunk(Message{Content: token}, "")) {
				return
			}
		}
		if len(message.ToolCalls) > 0 {
			deltas := make([]ToolCall, len(message.ToolCalls))
			for index, call := range message.ToolCalls {
				i := index
				call.Index = &i
				deltas[index] = call
			}
			if !emit.send(chunk(Message{ToolCalls: deltas}, "")) {
				return
			}
		}
		if !emit.send(chunk(Message{}, finishReason)) {
			return
		}
		emit.send(usageOnlyChunk(resp.ID, resp.Model, resp.Usage))
	}()
	return chunks
}
