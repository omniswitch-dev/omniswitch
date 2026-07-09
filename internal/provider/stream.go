package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func streamOpenAICompatible(ctx context.Context, client *http.Client, url string, apiKey string, req ChatRequest, providerName string, extraHeaders map[string]string) (<-chan ChatResponseChunk, ProviderMeta, error) {
	start := time.Now()
	req.Stream = true
	meta := ProviderMeta{Provider: providerName, Model: req.Model, Timestamp: start}

	body, err := json.Marshal(req)
	if err != nil {
		meta.Error = err.Error()
		return nil, meta, fmt.Errorf("marshal stream request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return nil, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for key, value := range extraHeaders {
		httpReq.Header.Set(key, value)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return nil, meta, fmt.Errorf("%s stream request: %w", providerName, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		meta.Error = string(respBody)
		meta.Latency = time.Since(start)
		return nil, meta, fmt.Errorf("%s stream error (status %d): %s", providerName, resp.StatusCode, string(respBody))
	}
	meta.Latency = time.Since(start)

	chunks := make(chan ChatResponseChunk)
	go func() {
		defer close(chunks)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				return
			}
			var chunk ChatResponseChunk
			if err := json.Unmarshal([]byte(data), &chunk); err == nil {
				chunks <- chunk
			}
		}
	}()
	return chunks, meta, nil
}

func StreamFromResponse(ctx context.Context, resp ChatResponse) <-chan ChatResponseChunk {
	chunks := make(chan ChatResponseChunk)
	go func() {
		defer close(chunks)
		content := ""
		finishReason := "stop"
		if len(resp.Choices) > 0 {
			content = resp.Choices[0].Message.Content
			finishReason = resp.Choices[0].FinishReason
		}
		words := strings.Fields(content)
		if len(words) == 0 && content != "" {
			words = []string{content}
		}
		for i, word := range words {
			select {
			case <-ctx.Done():
				return
			default:
			}
			token := word
			if i < len(words)-1 {
				token += " "
			}
			chunks <- ChatResponseChunk{
				ID:      resp.ID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   resp.Model,
				Choices: []ChunkChoice{{Index: 0, Delta: Message{Content: token}}},
			}
		}
		chunks <- ChatResponseChunk{
			ID:      resp.ID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   resp.Model,
			Choices: []ChunkChoice{{Index: 0, Delta: Message{}, FinishReason: finishReason}},
			Usage:   &resp.Usage,
		}
	}()
	return chunks
}

func streamAnthropic(ctx context.Context, body io.ReadCloser, fallbackModel string) <-chan ChatResponseChunk {
	chunks := make(chan ChatResponseChunk)
	go func() {
		defer close(chunks)
		defer body.Close()
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		id := fmt.Sprintf("anthropic-%d", time.Now().UnixNano())
		model := fallbackModel
		var usage Usage
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" || data == "[DONE]" {
				continue
			}
			var event struct {
				Type    string `json:"type"`
				Message struct {
					ID    string `json:"id"`
					Model string `json:"model"`
					Usage struct {
						InputTokens int `json:"input_tokens"`
					} `json:"usage"`
				} `json:"message"`
				Delta struct {
					Type       string `json:"type"`
					Text       string `json:"text"`
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}
			switch event.Type {
			case "message_start":
				if event.Message.ID != "" {
					id = event.Message.ID
				}
				if event.Message.Model != "" {
					model = event.Message.Model
				}
				usage.PromptTokens = event.Message.Usage.InputTokens
			case "content_block_delta":
				if event.Delta.Text != "" {
					chunks <- ChatResponseChunk{
						ID:      id,
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   model,
						Choices: []ChunkChoice{{Index: 0, Delta: Message{Content: event.Delta.Text}}},
					}
				}
			case "message_delta":
				usage.CompletionTokens = event.Usage.OutputTokens
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
				finish := mapAnthropicStopReason(event.Delta.StopReason)
				if finish == "" {
					finish = "stop"
				}
				chunks <- ChatResponseChunk{
					ID:      id,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   model,
					Choices: []ChunkChoice{{Index: 0, FinishReason: finish}},
					Usage:   &usage,
				}
			case "message_stop":
				return
			}
		}
	}()
	return chunks
}

func streamGemini(ctx context.Context, body io.ReadCloser, model string) <-chan ChatResponseChunk {
	chunks := make(chan ChatResponseChunk)
	go func() {
		defer close(chunks)
		defer body.Close()
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		id := fmt.Sprintf("gemini-%d", time.Now().UnixNano())
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" || data == "[DONE]" {
				continue
			}
			var response geminiResponse
			if err := json.Unmarshal([]byte(data), &response); err != nil {
				continue
			}
			finishReason := ""
			text := ""
			if len(response.Candidates) > 0 {
				candidate := response.Candidates[0]
				for _, part := range candidate.Content.Parts {
					text += part.Text
				}
				switch candidate.FinishReason {
				case "MAX_TOKENS":
					finishReason = "length"
				case "STOP":
					finishReason = "stop"
				default:
					finishReason = strings.ToLower(candidate.FinishReason)
				}
			}
			usage := Usage{
				PromptTokens:     response.UsageMetadata.PromptTokenCount,
				CompletionTokens: response.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      response.UsageMetadata.TotalTokenCount,
			}
			chunk := ChatResponseChunk{
				ID:      id,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   model,
				Choices: []ChunkChoice{{Index: 0, Delta: Message{Content: text}, FinishReason: finishReason}},
			}
			if usage.TotalTokens > 0 {
				chunk.Usage = &usage
			}
			chunks <- chunk
		}
	}()
	return chunks
}
