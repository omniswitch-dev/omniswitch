package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// streamAnthropic converts an Anthropic Messages SSE stream into OpenAI
// chunks. Tool calls get a per-stream ordinal index (content-block indexes
// count text and thinking blocks too), the first chunk carries the
// assistant role, "error" events and truncated streams end the channel
// with an Err chunk, and usage is emitted once in a final usage-only chunk.
func streamAnthropic(ctx context.Context, body io.ReadCloser, fallbackModel string) <-chan ChatResponseChunk {
	chunks := make(chan ChatResponseChunk)
	go func() {
		defer close(chunks)
		defer body.Close()
		emit := chunkEmitter{ctx: ctx, out: chunks}
		id := fmt.Sprintf("anthropic-%d", time.Now().UnixNano())
		model := fallbackModel
		var usage Usage
		toolOrdinals := map[int]int{}
		sentRole := false
		finished := false
		stopped := false
		newChunk := func(delta Message, finish string) ChatResponseChunk {
			if !sentRole {
				delta.Role = "assistant"
				sentRole = true
			}
			return ChatResponseChunk{
				ID:      id,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   model,
				Choices: []ChunkChoice{{Index: 0, Delta: delta, FinishReason: finish}},
			}
		}
		send := func(chunk ChatResponseChunk) error {
			if !emit.send(chunk) {
				stopped = true
				return errStopSSE
			}
			return nil
		}
		err := sseDataLines(body, func(data []byte) error {
			if string(data) == "[DONE]" {
				return nil
			}
			var event struct {
				Type    string `json:"type"`
				Index   int    `json:"index"`
				Message struct {
					ID    string         `json:"id"`
					Model string         `json:"model"`
					Usage anthropicUsage `json:"usage"`
				} `json:"message"`
				ContentBlock struct {
					Type  string          `json:"type"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content_block"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					Thinking    string `json:"thinking"`
					PartialJSON string `json:"partial_json"`
					StopReason  string `json:"stop_reason"`
				} `json:"delta"`
				Usage anthropicUsage  `json:"usage"`
				Error json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(data, &event); err != nil {
				return fmt.Errorf("anthropic stream: malformed event: %w", err)
			}
			switch event.Type {
			case "error":
				return upstreamStreamError("anthropic", event.Error)
			case "message_start":
				if event.Message.ID != "" {
					id = event.Message.ID
				}
				if event.Message.Model != "" {
					model = event.Message.Model
				}
				usage = event.Message.Usage.merge(usage)
				return send(newChunk(Message{}, ""))
			case "content_block_start":
				if event.ContentBlock.Type != "tool_use" {
					return nil
				}
				ordinal := len(toolOrdinals)
				toolOrdinals[event.Index] = ordinal
				args := string(event.ContentBlock.Input)
				if args == "{}" {
					args = ""
				}
				return send(newChunk(Message{ToolCalls: []ToolCall{{
					Index:    &ordinal,
					ID:       event.ContentBlock.ID,
					Type:     "function",
					Function: FunctionCall{Name: event.ContentBlock.Name, Arguments: args},
				}}}, ""))
			case "content_block_delta":
				switch {
				case event.Delta.Type == "input_json_delta":
					if event.Delta.PartialJSON == "" {
						return nil
					}
					ordinal, ok := toolOrdinals[event.Index]
					if !ok {
						return nil
					}
					return send(newChunk(Message{ToolCalls: []ToolCall{{
						Index:    &ordinal,
						Function: FunctionCall{Arguments: event.Delta.PartialJSON},
					}}}, ""))
				case event.Delta.Thinking != "":
					return send(newChunk(Message{ReasoningContent: event.Delta.Thinking}, ""))
				case event.Delta.Text != "":
					return send(newChunk(Message{Content: event.Delta.Text}, ""))
				}
			case "message_delta":
				usage = event.Usage.merge(usage)
				finish := mapAnthropicStopReason(event.Delta.StopReason)
				if finish == "" {
					finish = finishStop
				}
				return send(newChunk(Message{}, finish))
			case "message_stop":
				finished = true
				return errStopSSE
			}
			return nil
		})
		if stopped {
			return
		}
		if err == nil && !finished {
			err = fmt.Errorf("anthropic: %w", ErrStreamTruncated)
		}
		if err != nil {
			emit.fail(err)
			return
		}
		emit.send(usageOnlyChunk(id, model, usage))
	}()
	return chunks
}

// anthropicUsage is the usage object of message_start / message_delta
// events. message_delta counts are cumulative.
type anthropicUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
}

// merge applies the counts present in u to prev and returns the result.
// Prompt tokens stay the uncached input_tokens, as in the non-streaming
// path, so streamed and non-streamed costs match.
func (u anthropicUsage) merge(prev Usage) Usage {
	if u.InputTokens != nil {
		prev.PromptTokens = *u.InputTokens
	}
	if u.OutputTokens != nil {
		prev.CompletionTokens = *u.OutputTokens
	}
	if u.CacheReadInputTokens != nil && *u.CacheReadInputTokens > 0 {
		prev.PromptTokensDetails = &PromptTokensDetails{CachedTokens: *u.CacheReadInputTokens}
	}
	prev.TotalTokens = prev.PromptTokens + prev.CompletionTokens
	return prev
}

// streamGemini converts a Gemini streamGenerateContent SSE stream into
// OpenAI chunks, with per-stream tool ordinals and unique tool call IDs,
// mapped finish reasons, thinking tokens counted as completion tokens, and
// a final usage-only chunk. Error objects, read errors and streams that end
// without a finish reason end the channel with an Err chunk.
func streamGemini(ctx context.Context, body io.ReadCloser, model string) <-chan ChatResponseChunk {
	chunks := make(chan ChatResponseChunk)
	go func() {
		defer close(chunks)
		defer body.Close()
		emit := chunkEmitter{ctx: ctx, out: chunks}
		nonce := fmt.Sprintf("%d", time.Now().UnixNano())
		id := "gemini-" + nonce
		var usage *Usage
		toolOrdinal := 0
		sentRole := false
		finished := false
		stopped := false
		err := sseDataLines(body, func(data []byte) error {
			if string(data) == "[DONE]" {
				return nil
			}
			var response geminiResponse
			if err := json.Unmarshal(data, &response); err != nil {
				return fmt.Errorf("gemini stream: malformed event: %w", err)
			}
			if response.Error != nil {
				raw, _ := json.Marshal(response.Error)
				return upstreamStreamError("gemini", raw)
			}
			if response.UsageMetadata.TotalTokenCount > 0 || response.UsageMetadata.PromptTokenCount > 0 {
				u := response.UsageMetadata.usage()
				usage = &u
			}
			delta := Message{}
			finishReason := ""
			if len(response.Candidates) > 0 {
				candidate := response.Candidates[0]
				for _, part := range candidate.Content.Parts {
					if part.Thought {
						delta.ReasoningContent += part.Text
					} else {
						delta.Content += part.Text
					}
					if part.FunctionCall != nil {
						args, _ := json.Marshal(part.FunctionCall.Args)
						ordinal := toolOrdinal
						toolOrdinal++
						delta.ToolCalls = append(delta.ToolCalls, ToolCall{
							Index:    &ordinal,
							ID:       geminiToolCallID(part.FunctionCall, nonce, ordinal),
							Type:     "function",
							Function: FunctionCall{Name: part.FunctionCall.Name, Arguments: string(args)},
						})
					}
				}
				finishReason = mapGeminiFinishReason(candidate.FinishReason)
				if finishReason == finishStop && toolOrdinal > 0 {
					finishReason = finishToolCalls
				}
			} else if response.PromptFeedback.BlockReason != "" {
				finishReason = finishContentFilter
			}
			if finishReason != "" {
				finished = true
			}
			if delta.Content == "" && delta.ReasoningContent == "" && len(delta.ToolCalls) == 0 && finishReason == "" {
				return nil
			}
			if !sentRole {
				delta.Role = "assistant"
				sentRole = true
			}
			chunk := ChatResponseChunk{
				ID:      id,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   model,
				Choices: []ChunkChoice{{Index: 0, Delta: delta, FinishReason: finishReason}},
			}
			if !emit.send(chunk) {
				stopped = true
				return errStopSSE
			}
			return nil
		})
		if stopped {
			return
		}
		if err == nil && !finished {
			err = fmt.Errorf("gemini: %w", ErrStreamTruncated)
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
