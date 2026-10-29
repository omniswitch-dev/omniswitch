package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
		client:  newProviderClient(defaultRequestTimeout),
	}
}

func (g *Google) Name() string { return "google" }

func (g *Google) Models() []ModelInfo {
	return catalogModelInfos("google", "google")
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
	ID   string         `json:"id,omitempty"`
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
	Temperature      *float64 `json:"temperature,omitempty"`
	MaxTokens        *int     `json:"maxOutputTokens,omitempty"`
	TopP             *float64 `json:"topP,omitempty"`
	StopSequences    []string `json:"stopSequences,omitempty"`
	ResponseMimeType string   `json:"responseMimeType,omitempty"`
	ResponseSchema   any      `json:"responseSchema,omitempty"`
}

// geminiResponse is the Gemini API response format.
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text         string              `json:"text"`
				Thought      bool                `json:"thought,omitempty"`
				FunctionCall *geminiFunctionCall `json:"functionCall,omitempty"`
			} `json:"parts"`
			Role string `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata geminiUsageMetadata `json:"usageMetadata"`
	// Error is set when the API reports a failure inside a 200 stream.
	Error *geminiError `json:"error,omitempty"`
}

type geminiUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
}

type geminiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// usage converts Gemini usage metadata to OpenAI usage. Thinking tokens are
// billed as output tokens, so they count as completion tokens (and are
// reported as reasoning_tokens), like OpenAI reasoning models.
func (u geminiUsageMetadata) usage() Usage {
	out := Usage{
		PromptTokens:     u.PromptTokenCount,
		CompletionTokens: u.CandidatesTokenCount + u.ThoughtsTokenCount,
	}
	out.TotalTokens = out.PromptTokens + out.CompletionTokens
	if u.TotalTokenCount > out.TotalTokens {
		out.TotalTokens = u.TotalTokenCount
	}
	if u.ThoughtsTokenCount > 0 {
		out.CompletionTokensDetails = &CompletionTokensDetails{ReasoningTokens: u.ThoughtsTokenCount}
	}
	if u.CachedContentTokenCount > 0 {
		out.PromptTokensDetails = &PromptTokensDetails{CachedTokens: u.CachedContentTokenCount}
	}
	return out
}

// geminiToolCallID returns the upstream function call ID when present, or a
// unique synthetic one. Plain "call_<n>" IDs collide across turns and
// confuse clients that key tool results by ID.
func geminiToolCallID(call *geminiFunctionCall, prefix string, ordinal int) string {
	if call != nil && call.ID != "" {
		return call.ID
	}
	return fmt.Sprintf("call_%s_%d", prefix, ordinal)
}

func (g *Google) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "google", Model: req.Model, Timestamp: start}

	gemReq, err := toGeminiRequest(req)
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}

	body, err := json.Marshal(gemReq)
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.modelURL(req.Model, ":generateContent"), bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", g.apiKey)

	resp, err := g.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		meta.Latency = time.Since(start)
		upstreamErr := newUpstreamError(meta.Provider, "gemini error", resp)
		meta.Error = upstreamErr.Body
		return ChatResponse{}, meta, upstreamErr
	}
	respBody, err := readLimited(resp.Body, maxResponseBodyBytes)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, err
	}

	meta.Latency = time.Since(start)

	var gemResp geminiResponse
	if err := json.Unmarshal(respBody, &gemResp); err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}

	id := fmt.Sprintf("gemini-%d", time.Now().UnixNano())
	content := ""
	reasoning := ""
	finishReason := finishStop
	var toolCalls []ToolCall
	if len(gemResp.Candidates) > 0 {
		candidate := gemResp.Candidates[0]
		for _, part := range candidate.Content.Parts {
			if part.Thought {
				reasoning += part.Text
			} else {
				content += part.Text
			}
			if part.FunctionCall != nil {
				args, _ := json.Marshal(part.FunctionCall.Args)
				ordinal := len(toolCalls)
				toolCalls = append(toolCalls, ToolCall{
					Index: &ordinal,
					ID:    geminiToolCallID(part.FunctionCall, id[len("gemini-"):], ordinal),
					Type:  "function",
					Function: FunctionCall{
						Name:      part.FunctionCall.Name,
						Arguments: string(args),
					},
				})
			}
		}
		if mapped := mapGeminiFinishReason(candidate.FinishReason); mapped != "" {
			finishReason = mapped
		}
		if len(toolCalls) > 0 && finishReason == finishStop {
			finishReason = finishToolCalls
		}
	} else if gemResp.PromptFeedback.BlockReason != "" {
		finishReason = finishContentFilter
	}

	usage := gemResp.UsageMetadata.usage()
	chatResp := ChatResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []Choice{
			{
				Index:        0,
				Message:      Message{Role: "assistant", Content: content, ReasoningContent: reasoning, ToolCalls: toolCalls},
				FinishReason: finishReason,
			},
		},
		Usage: usage,
	}

	meta.Cost = pricingFor("google", req.Model).Cost(usage.PromptTokens, usage.CompletionTokens)
	return chatResp, meta, nil
}

func (g *Google) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "google", Model: req.Model, Timestamp: start}
	gemReq, err := toGeminiRequest(req)
	if err != nil {
		meta.Error = err.Error()
		return nil, meta, err
	}
	body, err := json.Marshal(gemReq)
	if err != nil {
		return nil, meta, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.modelURL(req.Model, ":streamGenerateContent?alt=sse"), bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return nil, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", g.apiKey)

	resp, err := streamingClient(g.client).Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return nil, meta, fmt.Errorf("gemini stream request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		meta.Latency = time.Since(start)
		upstreamErr := newUpstreamError(meta.Provider, "gemini stream error", resp)
		_ = resp.Body.Close()
		meta.Error = upstreamErr.Body
		return nil, meta, upstreamErr
	}
	meta.Latency = time.Since(start)
	return streamGemini(ctx, resp.Body, req.Model), meta, nil
}

// modelURL builds a Gemini model endpoint. The API key is sent in the
// x-goog-api-key header rather than the ?key= query parameter, so it does
// not end up in proxy/access logs or in error messages that echo the URL.
// The model ID is path-escaped so it cannot alter the request path.
func (g *Google) modelURL(model, method string) string {
	model = strings.TrimPrefix(model, "models/")
	return g.baseURL + "/models/" + url.PathEscape(model) + method
}

func toGeminiRequest(req ChatRequest) (geminiRequest, error) {
	if req.N != nil && *req.N > 1 {
		return geminiRequest{}, NewUnsupportedParameterError("google", "n")
	}
	if req.Seed != nil {
		return geminiRequest{}, NewUnsupportedParameterError("google", "seed")
	}
	if len(req.LogitBias) > 0 {
		return geminiRequest{}, NewUnsupportedParameterError("google", "logit_bias")
	}
	if req.PresencePenalty != nil {
		return geminiRequest{}, NewUnsupportedParameterError("google", "presence_penalty")
	}
	if req.FrequencyPenalty != nil {
		return geminiRequest{}, NewUnsupportedParameterError("google", "frequency_penalty")
	}
	if req.ReasoningEffort != "" {
		return geminiRequest{}, NewUnsupportedParameterError("google", "reasoning_effort")
	}
	if req.ServiceTier != "" {
		return geminiRequest{}, NewUnsupportedParameterError("google", "service_tier")
	}
	if req.ParallelToolCalls != nil {
		return geminiRequest{}, NewUnsupportedParameterError("google", "parallel_tool_calls")
	}

	maxTokens := req.MaxTokens
	if req.MaxCompletionTokens != nil {
		maxTokens = req.MaxCompletionTokens
	}
	gemReq := geminiRequest{
		GenerationConfig: &geminiGenerationConfig{
			Temperature:   req.Temperature,
			MaxTokens:     maxTokens,
			TopP:          req.TopP,
			StopSequences: append([]string(nil), req.Stop...),
		},
	}

	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case "json_object":
			gemReq.GenerationConfig.ResponseMimeType = "application/json"
		case "json_schema":
			gemReq.GenerationConfig.ResponseMimeType = "application/json"
			if req.ResponseFormat.JSONSchema != nil {
				gemReq.GenerationConfig.ResponseSchema = req.ResponseFormat.JSONSchema.Schema
			}
		case "", "text":
			// No structured output requested.
		default:
			return geminiRequest{}, NewUnsupportedParameterError("google", "response_format")
		}
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
	var systemParts []geminiPart
	for _, msg := range req.Messages {
		// Every system/developer message becomes one systemInstruction part,
		// in order; previously each one overwrote the last.
		if msg.Role == "system" || msg.Role == "developer" {
			if text := msg.Text(); text != "" {
				systemParts = append(systemParts, geminiPart{Text: text})
			}
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID != "" && call.Function.Name != "" {
				toolCallNames[call.ID] = call.Function.Name
			}
		}
		role := msg.Role
		switch role {
		case "assistant":
			role = "model"
		case "tool":
			// Gemini expects functionResponse parts in a user turn.
			role = "user"
		}
		parts := geminiMessageParts(msg, toolCallNames)
		if len(parts) == 0 {
			continue
		}
		// Parallel tool results (consecutive tool messages) must reach
		// Gemini as one turn holding every functionResponse part.
		if n := len(gemReq.Contents); n > 0 && msg.Role == "tool" && gemReq.Contents[n-1].Role == "user" && isGeminiFunctionResponseTurn(gemReq.Contents[n-1]) {
			gemReq.Contents[n-1].Parts = append(gemReq.Contents[n-1].Parts, parts...)
			continue
		}
		gemReq.Contents = append(gemReq.Contents, geminiContent{
			Role:  role,
			Parts: parts,
		})
	}
	if len(systemParts) > 0 {
		gemReq.SystemInstruction = &geminiContent{Parts: systemParts}
	}
	return gemReq, nil
}

func isGeminiFunctionResponseTurn(content geminiContent) bool {
	for _, part := range content.Parts {
		if part.FunctionResponse == nil {
			return false
		}
	}
	return len(content.Parts) > 0
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
	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		// Prior assistant tool calls must be replayed as functionCall
		// parts, or Gemini sees tool results with no matching call.
		var parts []geminiPart
		if text := msg.Text(); text != "" {
			parts = append(parts, geminiPart{Text: text})
		}
		for _, call := range msg.ToolCalls {
			args := map[string]any{}
			if strings.TrimSpace(call.Function.Arguments) != "" {
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					args = map[string]any{"arguments": call.Function.Arguments}
				}
			}
			parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{Name: call.Function.Name, Args: args}})
		}
		return parts
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

// Pricing for Gemini models is resolved from the consolidated catalog
// (internal/provider/catalog/models.json); see pricingFor in pricing.go.
