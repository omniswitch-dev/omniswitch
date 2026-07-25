package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Anthropic implements the Provider interface for the Anthropic API.
type Anthropic struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewAnthropic creates a new Anthropic provider.
func NewAnthropic(apiKey string) *Anthropic {
	return &Anthropic{
		apiKey:  apiKey,
		baseURL: "https://api.anthropic.com",
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *Anthropic) Name() string { return "anthropic" }

func (a *Anthropic) Models() []ModelInfo {
	models := []string{
		"claude-sonnet-4-20250514", "claude-opus-4-20250514",
		"claude-3-5-sonnet-20241022", "claude-3-5-haiku-20241022",
		"claude-3-opus-20240229",
	}
	out := make([]ModelInfo, len(models))
	for i, m := range models {
		out[i] = ModelInfo{ID: m, Object: "model", OwnedBy: "anthropic", Provider: "anthropic"}
	}
	return out
}

// anthropicRequest is the Anthropic API request format.
type anthropicRequest struct {
	Model      string               `json:"model"`
	Messages   []anthropicMessage   `json:"messages"`
	MaxTokens  int                  `json:"max_tokens"`
	System     string               `json:"system,omitempty"`
	Stream     bool                 `json:"stream,omitempty"`
	Tools      []anthropicTool      `json:"tools,omitempty"`
	ToolChoice *anthropicToolChoice `json:"tool_choice,omitempty"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// anthropicResponse is the Anthropic API response format.
type anthropicResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Model   string `json:"model"`
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text,omitempty"`
		ID    string          `json:"id,omitempty"`
		Name  string          `json:"name,omitempty"`
		Input json.RawMessage `json:"input,omitempty"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (a *Anthropic) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "anthropic", Model: req.Model, Timestamp: start}

	body, err := json.Marshal(toAnthropicRequest(req, false))
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, err
	}

	meta.Latency = time.Since(start)

	if resp.StatusCode != http.StatusOK {
		meta.Error = string(respBody)
		return ChatResponse{}, meta, fmt.Errorf("anthropic error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var anthResp anthropicResponse
	if err := json.Unmarshal(respBody, &anthResp); err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}

	// Convert Anthropic response back to OpenAI format.
	var content string
	var toolCalls []ToolCall
	for _, block := range anthResp.Content {
		if block.Type == "text" {
			content += block.Text
		} else if block.Type == "tool_use" {
			toolCalls = append(toolCalls, ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      block.Name,
					Arguments: string(block.Input),
				},
			})
		}
	}

	finishReason := mapAnthropicStopReason(anthResp.StopReason)
	if len(toolCalls) > 0 && anthResp.StopReason == "tool_use" {
		finishReason = "tool_calls"
	}

	chatResp := ChatResponse{
		ID:      anthResp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   anthResp.Model,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:      "assistant",
					Content:   content,
					ToolCalls: toolCalls,
				},
				FinishReason: finishReason,
			},
		},
		Usage: Usage{
			PromptTokens:     anthResp.Usage.InputTokens,
			CompletionTokens: anthResp.Usage.OutputTokens,
			TotalTokens:      anthResp.Usage.InputTokens + anthResp.Usage.OutputTokens,
		},
	}

	meta.Cost = anthropicPricing(req.Model).Cost(anthResp.Usage.InputTokens, anthResp.Usage.OutputTokens)
	return chatResp, meta, nil
}

func (a *Anthropic) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "anthropic", Model: req.Model, Timestamp: start}

	body, err := json.Marshal(toAnthropicRequest(req, true))
	if err != nil {
		return nil, meta, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return nil, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return nil, meta, fmt.Errorf("anthropic stream request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		meta.Error = string(respBody)
		meta.Latency = time.Since(start)
		return nil, meta, fmt.Errorf("anthropic stream error (status %d): %s", resp.StatusCode, string(respBody))
	}
	meta.Latency = time.Since(start)
	return streamAnthropic(ctx, resp.Body, req.Model), meta, nil
}

func toAnthropicRequest(req ChatRequest, stream bool) anthropicRequest {
	anthReq := anthropicRequest{
		Model:     req.Model,
		MaxTokens: 4096,
		Stream:    stream,
	}
	if req.MaxTokens != nil {
		anthReq.MaxTokens = *req.MaxTokens
	}

	// Convert tools
	for _, t := range req.Tools {
		schema := t.Function.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		anthReq.Tools = append(anthReq.Tools, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: schema,
		})
	}

	// Convert tool_choice
	if req.ToolChoice != nil {
		switch v := req.ToolChoice.(type) {
		case string:
			if v == "auto" || v == "none" {
				anthReq.ToolChoice = &anthropicToolChoice{Type: v}
			} else if v == "required" {
				anthReq.ToolChoice = &anthropicToolChoice{Type: "any"}
			}
		case map[string]any:
			if f, ok := v["function"].(map[string]any); ok {
				if name, ok := f["name"].(string); ok {
					anthReq.ToolChoice = &anthropicToolChoice{Type: "tool", Name: name}
				}
			}
		}
	}

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			anthReq.System = msg.Text()
			continue
		}

		var content any = msg.Text()

		// Assistant message with tool_calls → Anthropic tool_use blocks
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			var blocks []any
			if msg.Text() != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": msg.Text()})
			}
			for _, tc := range msg.ToolCalls {
				input := json.RawMessage(tc.Function.Arguments)
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": input,
				})
			}
			content = blocks
		} else if msg.Role == "tool" {
			// Tool result → Anthropic user message with tool_result block
			content = []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": msg.ToolCallID,
					"content":     msg.Text(),
				},
			}
			msg.Role = "user"
		} else if len(msg.ContentParts) > 0 {
			// Vision / multimodal → Anthropic content blocks
			var blocks []any
			for _, part := range msg.ContentParts {
				if part.Type == "text" {
					blocks = append(blocks, map[string]any{"type": "text", "text": part.Text})
				} else if part.Type == "image_url" && part.ImageURL != nil {
					source := map[string]any{"type": "url", "url": part.ImageURL.URL}
					if decoded, ok := parseDataURL(part.ImageURL.URL); ok {
						source = map[string]any{
							"type":       "base64",
							"media_type": decoded.MediaType,
							"data":       decoded.Data,
						}
					}
					blocks = append(blocks, map[string]any{"type": "image", "source": source})
				}
			}
			content = blocks
		}

		anthReq.Messages = append(anthReq.Messages, anthropicMessage{
			Role:    msg.Role,
			Content: content,
		})
	}
	return anthReq
}

func mapAnthropicStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	default:
		return reason
	}
}

func anthropicPricing(model string) ModelPricing {
	switch model {
	case "claude-opus-4-20250514":
		return ModelPricing{InputPerMillion: 15.00, OutputPerMillion: 75.00}
	case "claude-sonnet-4-20250514":
		return ModelPricing{InputPerMillion: 3.00, OutputPerMillion: 15.00}
	case "claude-3-5-sonnet-20241022":
		return ModelPricing{InputPerMillion: 3.00, OutputPerMillion: 15.00}
	case "claude-3-5-haiku-20241022":
		return ModelPricing{InputPerMillion: 0.80, OutputPerMillion: 4.00}
	case "claude-3-opus-20240229":
		return ModelPricing{InputPerMillion: 15.00, OutputPerMillion: 75.00}
	default:
		return ModelPricing{InputPerMillion: 3.00, OutputPerMillion: 15.00}
	}
}
