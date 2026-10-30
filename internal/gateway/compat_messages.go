package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

// Keep Anthropic blocks separate from provider.Message: decoding them as
// OpenAI content parts silently discards tool_use and tool_result payloads.
type anthropicMessagesRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	System        json.RawMessage `json:"system,omitempty"`
	MaxTokens     *int            `json:"max_tokens,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Tools         []struct {
		Type        string         `json:"type,omitempty"`
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		InputSchema map[string]any `json:"input_schema"`
		Strict      *bool          `json:"strict,omitempty"`
	} `json:"tools,omitempty"`
	ToolChoice *struct {
		Type                   string `json:"type"`
		Name                   string `json:"name,omitempty"`
		DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
	} `json:"tool_choice,omitempty"`
	Thinking json.RawMessage `json:"thinking,omitempty"`
}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type,omitempty"`
		Data      string `json:"data,omitempty"`
		URL       string `json:"url,omitempty"`
	} `json:"source,omitempty"`
}

func (r anthropicMessagesRequest) toChatRequest() (provider.ChatRequest, error) {
	request := provider.ChatRequest{Model: r.Model, MaxTokens: r.MaxTokens, Temperature: r.Temperature, TopP: r.TopP, Stop: r.StopSequences}
	if strings.TrimSpace(r.Model) == "" {
		return request, fmt.Errorf("model is required")
	}
	if len(r.Messages) == 0 {
		return request, fmt.Errorf("messages is required")
	}
	if r.MaxTokens != nil && *r.MaxTokens <= 0 {
		return request, fmt.Errorf("max_tokens must be positive")
	}
	if len(r.Thinking) > 0 && string(r.Thinking) != "null" {
		return request, fmt.Errorf("thinking is unsupported by the Messages compatibility endpoint")
	}
	if len(r.System) > 0 && string(r.System) != "null" {
		text, err := anthropicText(r.System)
		if err != nil {
			return request, fmt.Errorf("system: %w", err)
		}
		request.Messages = append(request.Messages, provider.Message{Role: "system", Content: text})
	}
	calls := make(map[string]bool)
	for _, message := range r.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return request, fmt.Errorf("messages role must be user or assistant")
		}
		var text string
		if err := json.Unmarshal(message.Content, &text); err == nil && string(message.Content) != "null" {
			request.Messages = append(request.Messages, provider.Message{Role: message.Role, Content: text})
			continue
		}
		var blocks []anthropicContentBlock
		if err := json.Unmarshal(message.Content, &blocks); err != nil || len(blocks) == 0 {
			return request, fmt.Errorf("message content must be a string or nonempty content block array")
		}
		msg := provider.Message{Role: message.Role}
		// Tool results must precede any user text/images. Keeping them as
		// separate canonical tool messages preserves parallel call IDs.
		seenContent := false
		for _, block := range blocks {
			switch block.Type {
			case "text":
				seenContent = true
				msg.ContentParts = append(msg.ContentParts, provider.ContentPart{Type: "text", Text: block.Text})
			case "image":
				if message.Role != "user" {
					return request, fmt.Errorf("image blocks require a user message")
				}
				seenContent = true
				image, err := anthropicImage(block)
				if err != nil {
					return request, err
				}
				msg.ContentParts = append(msg.ContentParts, provider.ContentPart{Type: "image_url", ImageURL: image})
			case "tool_use":
				if message.Role != "assistant" || strings.TrimSpace(block.ID) == "" || strings.TrimSpace(block.Name) == "" {
					return request, fmt.Errorf("tool_use requires an assistant message, id, and name")
				}
				if _, exists := calls[block.ID]; exists {
					return request, fmt.Errorf("duplicate tool_use id %q", block.ID)
				}
				if _, err := toolArgumentsObject(string(block.Input)); err != nil {
					return request, err
				}
				calls[block.ID] = false
				msg.ToolCalls = append(msg.ToolCalls, provider.ToolCall{ID: block.ID, Type: "function", Function: provider.FunctionCall{Name: block.Name, Arguments: string(block.Input)}})
			case "tool_result":
				if message.Role != "user" || seenContent {
					return request, fmt.Errorf("tool_result blocks must precede text/images in a user message")
				}
				resolved, exists := calls[block.ToolUseID]
				if !exists || resolved {
					return request, fmt.Errorf("tool_result requires an unresolved tool_use_id from the message history")
				}
				result := ""
				if len(block.Content) > 0 {
					var err error
					result, err = anthropicText(block.Content)
					if err != nil {
						return request, fmt.Errorf("tool_result: %w", err)
					}
				}
				if block.IsError {
					result = "Tool error: " + result
				}
				calls[block.ToolUseID] = true
				request.Messages = append(request.Messages, provider.Message{Role: "tool", ToolCallID: block.ToolUseID, Content: result})
			default:
				return request, fmt.Errorf("unsupported Messages content block %q", block.Type)
			}
		}
		if len(msg.ContentParts) > 0 || len(msg.ToolCalls) > 0 {
			var texts []string
			for _, part := range msg.ContentParts {
				if part.Type == "text" {
					texts = append(texts, part.Text)
				}
			}
			msg.Content = strings.Join(texts, "\n")
			request.Messages = append(request.Messages, msg)
		}
	}
	for _, tool := range r.Tools {
		if tool.Type != "" && tool.Type != "custom" {
			return request, fmt.Errorf("only custom tools are supported, got %q", tool.Type)
		}
		if strings.TrimSpace(tool.Name) == "" || tool.InputSchema == nil {
			return request, fmt.Errorf("tools require name and input_schema")
		}
		request.Tools = append(request.Tools, provider.Tool{Type: "function", Function: provider.ToolFunction{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema, Strict: tool.Strict}})
	}
	if choice := r.ToolChoice; choice != nil {
		switch choice.Type {
		case "auto", "none":
			request.ToolChoice = choice.Type
		case "any":
			request.ToolChoice = "required"
		case "tool":
			if strings.TrimSpace(choice.Name) == "" {
				return request, fmt.Errorf("tool_choice tool requires name")
			}
			request.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": choice.Name}}
		default:
			return request, fmt.Errorf("unsupported tool_choice type %q", choice.Type)
		}
		if choice.DisableParallelToolUse != nil {
			parallel := !*choice.DisableParallelToolUse
			request.ParallelToolCalls = &parallel
		}
	}
	return request, nil
}

func anthropicText(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && string(raw) != "null" {
		return text, nil
	}
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil || blocks == nil {
		return "", fmt.Errorf("expected a string or text block array")
	}
	var texts []string
	for _, block := range blocks {
		if block.Type != "text" {
			return "", fmt.Errorf("unsupported text content block %q", block.Type)
		}
		texts = append(texts, block.Text)
	}
	return strings.Join(texts, "\n"), nil
}

func anthropicImage(block anthropicContentBlock) (*provider.ImageURL, error) {
	if block.Source == nil {
		return nil, fmt.Errorf("image requires source")
	}
	source := block.Source
	switch source.Type {
	case "url":
		if !strings.HasPrefix(source.URL, "https://") && !strings.HasPrefix(source.URL, "http://") {
			return nil, fmt.Errorf("image source url must be HTTP or HTTPS")
		}
		return &provider.ImageURL{URL: source.URL}, nil
	case "base64":
		switch source.MediaType {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			return nil, fmt.Errorf("unsupported image media_type %q", source.MediaType)
		}
		if _, err := base64.StdEncoding.DecodeString(source.Data); err != nil || source.Data == "" {
			return nil, fmt.Errorf("image source data must be nonempty base64")
		}
		return &provider.ImageURL{URL: "data:" + source.MediaType + ";base64," + source.Data}, nil
	default:
		return nil, fmt.Errorf("unsupported image source type %q", source.Type)
	}
}

func toolArgumentsObject(arguments string) (map[string]json.RawMessage, error) {
	var input map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arguments), &input); err != nil || input == nil {
		return nil, fmt.Errorf("tool arguments must be a JSON object")
	}
	return input, nil
}
