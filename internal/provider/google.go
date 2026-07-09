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
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
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
				Text string `json:"text"`
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

	gemReq := geminiRequest{
		GenerationConfig: &geminiGenerationConfig{
			Temperature: req.Temperature,
			MaxTokens:   req.MaxTokens,
			TopP:        req.TopP,
		},
	}

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			gemReq.SystemInstruction = &geminiContent{
				Parts: []geminiPart{{Text: msg.Text()}},
			}
			continue
		}
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}
		gemReq.Contents = append(gemReq.Contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: msg.Text()}},
		})
	}

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
	if len(gemResp.Candidates) > 0 {
		candidate := gemResp.Candidates[0]
		for _, part := range candidate.Content.Parts {
			content += part.Text
		}
		if candidate.FinishReason == "MAX_TOKENS" {
			finishReason = "length"
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
				Message:      Message{Role: "assistant", Content: content},
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

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			gemReq.SystemInstruction = &geminiContent{
				Parts: []geminiPart{{Text: msg.Text()}},
			}
			continue
		}
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}
		gemReq.Contents = append(gemReq.Contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: msg.Text()}},
		})
	}
	return gemReq
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
