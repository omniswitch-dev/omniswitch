package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Google implements the Provider interface for the Google Gemini API.
type Google struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewGoogle creates a new Google Gemini provider.
func NewGoogle(apiKey string) *Google {
	return &Google{
		apiKey:  apiKey,
		baseURL: "https://generativelanguage.googleapis.com/v1beta",
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (g *Google) Name() string { return "google" }

func (g *Google) Models() []ModelInfo {
	models := []string{
		"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash",
		"gemini-1.5-pro", "gemini-1.5-flash",
	}
	out := make([]ModelInfo, len(models))
	for i, m := range models {
		out[i] = ModelInfo{ID: m, Object: "model", OwnedBy: "google", Provider: "google"}
	}
	return out
}

// geminiRequest is the Gemini API request format.
type geminiRequest struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []geminiTool            `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig       `json:"toolConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiBlob             `json:"inlineData,omitempty"`
	FileData         *geminiFileData         `json:"fileData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

type geminiFunctionDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type geminiToolConfig struct {
	FunctionCallingConfig *geminiFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type geminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type geminiGenerationConfig struct {
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   *int     `json:"maxOutputTokens,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
}

// geminiResponse is the Gemini API response format.
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text         string              `json:"text"`
				FunctionCall *geminiFunctionCall `json:"functionCall,omitempty"`
			} `json:"parts"`
			Role string `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func (g *Google) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "google", Model: req.Model, Timestamp: start}

	gemReq := toGeminiRequest(req)

	body, err := json.Marshal(gemReq)
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}

	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", g.baseURL, req.Model, g.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, fmt.Errorf("gemini request: %w", err)
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
		return ChatResponse{}, meta, fmt.Errorf("gemini error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var gemResp geminiResponse
	if err := json.Unmarshal(respBody, &gemResp); err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}

	content := ""
	finishReason := "stop"
	var toolCalls []ToolCall
	if len(gemResp.Candidates) > 0 {
		candidate := gemResp.Candidates[0]
		for index, part := range candidate.Content.Parts {
			content += part.Text
			if part.FunctionCall != nil {
				args, _ := json.Marshal(part.FunctionCall.Args)
				i := index
				toolCalls = append(toolCalls, ToolCall{
					Index: &i,
					ID:    fmt.Sprintf("call_%d", index),
					Type:  "function",
					Function: FunctionCall{
						Name:      part.FunctionCall.Name,
						Arguments: string(args),
					},
				})
			}
		}
		if candidate.FinishReason == "MAX_TOKENS" {
			finishReason = "length"
		}
		if len(toolCalls) > 0 {
			finishReason = "tool_calls"
		}
	}

	chatResp := ChatResponse{
		ID:      fmt.Sprintf("gemini-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []Choice{
			{
				Index:        0,
				Message:      Message{Role: "assistant", Content: content, ToolCalls: toolCalls},
				FinishReason: finishReason,
			},
		},
		Usage: Usage{
			PromptTokens:     gemResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: gemResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gemResp.UsageMetadata.TotalTokenCount,
		},
	}

	meta.Cost = geminiPricing(req.Model).Cost(gemResp.UsageMetadata.PromptTokenCount, gemResp.UsageMetadata.CandidatesTokenCount)
	return chatResp, meta, nil
}

func (g *Google) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "google", Model: req.Model, Timestamp: start}
	body, err := json.Marshal(toGeminiRequest(req))
	if err != nil {
		return nil, meta, err
	}
	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s", g.baseURL, req.Model, g.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return nil, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return nil, meta, fmt.Errorf("gemini stream request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		meta.Error = string(respBody)
		meta.Latency = time.Since(start)
		return nil, meta, fmt.Errorf("gemini stream error (status %d): %s", resp.StatusCode, string(respBody))
	}
	meta.Latency = time.Since(start)
	return streamGemini(ctx, resp.Body, req.Model), meta, nil
}

func toGeminiRequest(req ChatRequest) geminiRequest {
	gemReq := geminiRequest{
		GenerationConfig: &geminiGenerationConfig{
			Temperature: req.Temperature,
			MaxTokens:   req.MaxTokens,
			TopP:        req.TopP,
		},
	}

	if len(req.Tools) > 0 {
		tool := geminiTool{FunctionDeclarations: make([]geminiFunctionDeclaration, 0, len(req.Tools))}
		for _, openAITool := range req.Tools {
			if openAITool.Type != "" && openAITool.Type != "function" {
				continue
			}
			tool.FunctionDeclarations = append(tool.FunctionDeclarations, geminiFunctionDeclaration{
				Name:        openAITool.Function.Name,
				Description: openAITool.Function.Description,
				Parameters:  openAITool.Function.Parameters,
			})
		}
		if len(tool.FunctionDeclarations) > 0 {
			gemReq.Tools = []geminiTool{tool}
		}
	}
	if req.ToolChoice != nil {
		gemReq.ToolConfig = geminiToolChoice(req.ToolChoice)
	}

	toolCallNames := map[string]string{}
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			gemReq.SystemInstruction = &geminiContent{
				Parts: []geminiPart{{Text: msg.Text()}},
			}
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID != "" && call.Function.Name != "" {
				toolCallNames[call.ID] = call.Function.Name
			}
		}
		role := msg.Role
		if role == "assistant" {
			role = "model"
		} else if role == "tool" {
			role = "function"
		}
		parts := geminiMessageParts(msg, toolCallNames)
		gemReq.Contents = append(gemReq.Contents, geminiContent{
			Role:  role,
			Parts: parts,
		})
	}
	return gemReq
}

func geminiMessageParts(msg Message, toolCallNames map[string]string) []geminiPart {
	if msg.Role == "tool" {
		var response any
		if err := json.Unmarshal([]byte(msg.Text()), &response); err != nil {
			response = map[string]any{"content": msg.Text()}
		}
		wrapped, ok := response.(map[string]any)
		if !ok {
			wrapped = map[string]any{"content": response}
		}
		return []geminiPart{{
			FunctionResponse: &geminiFunctionResponse{
				Name:     firstNonEmptyString(toolCallNames[msg.ToolCallID], msg.ToolCallID),
				Response: wrapped,
			},
		}}
	}
	if len(msg.ContentParts) == 0 {
		if msg.Text() == "" {
			return []geminiPart{}
		}
		return []geminiPart{{Text: msg.Text()}}
	}
	parts := make([]geminiPart, 0, len(msg.ContentParts))
	for _, part := range msg.ContentParts {
		switch part.Type {
		case "text", "input_text":
			if part.Text != "" {
				parts = append(parts, geminiPart{Text: part.Text})
			}
		case "image_url", "input_image":
			if part.ImageURL == nil {
				continue
			}
			if decoded, ok := parseDataURL(part.ImageURL.URL); ok {
				parts = append(parts, geminiPart{InlineData: &geminiBlob{
					MimeType: decoded.MediaType,
					Data:     decoded.Data,
				}})
				continue
			}
			parts = append(parts, geminiPart{FileData: &geminiFileData{
				MimeType: mediaTypeFromURL(part.ImageURL.URL),
				FileURI:  part.ImageURL.URL,
			}})
		case "input_audio":
			if part.InputAudio != nil {
				mimeType := "audio/" + strings.TrimPrefix(part.InputAudio.Format, ".")
				parts = append(parts, geminiPart{InlineData: &geminiBlob{
					MimeType: mimeType,
					Data:     part.InputAudio.Data,
				}})
			}
		}
	}
	if len(parts) == 0 && msg.Text() != "" {
		return []geminiPart{{Text: msg.Text()}}
	}
	return parts
}

func geminiToolChoice(choice any) *geminiToolConfig {
	config := &geminiToolConfig{FunctionCallingConfig: &geminiFunctionCallingConfig{Mode: "AUTO"}}
	switch value := choice.(type) {
	case string:
		switch value {
		case "none":
			config.FunctionCallingConfig.Mode = "NONE"
		case "required":
			config.FunctionCallingConfig.Mode = "ANY"
		default:
			config.FunctionCallingConfig.Mode = "AUTO"
		}
	case map[string]any:
		if function, ok := value["function"].(map[string]any); ok {
			if name, ok := function["name"].(string); ok && name != "" {
				config.FunctionCallingConfig.Mode = "ANY"
				config.FunctionCallingConfig.AllowedFunctionNames = []string{name}
			}
		}
	}
	return config
}

func geminiPricing(model string) ModelPricing {
	switch model {
	case "gemini-2.5-pro":
		return ModelPricing{InputPerMillion: 1.25, OutputPerMillion: 10.00}
	case "gemini-2.5-flash":
		return ModelPricing{InputPerMillion: 0.15, OutputPerMillion: 0.60}
	case "gemini-2.0-flash":
		return ModelPricing{InputPerMillion: 0.10, OutputPerMillion: 0.40}
	case "gemini-1.5-pro":
		return ModelPricing{InputPerMillion: 1.25, OutputPerMillion: 5.00}
	case "gemini-1.5-flash":
		return ModelPricing{InputPerMillion: 0.075, OutputPerMillion: 0.30}
	default:
		return ModelPricing{InputPerMillion: 0.15, OutputPerMillion: 0.60}
	}
}
