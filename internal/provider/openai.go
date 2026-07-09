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

// OpenAI implements the Provider interface for the OpenAI API.
type OpenAI struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewOpenAI creates a new OpenAI provider.
func NewOpenAI(apiKey string) *OpenAI {
	return &OpenAI{
		apiKey:  apiKey,
		baseURL: "https://api.openai.com/v1",
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (o *OpenAI) Name() string { return "openai" }

func (o *OpenAI) Models() []ModelInfo {
	models := []string{
		"gpt-4o", "gpt-4o-mini", "gpt-4-turbo", "gpt-4",
		"gpt-3.5-turbo", "o1", "o1-mini", "o3-mini",
	}
	out := make([]ModelInfo, len(models))
	for i, m := range models {
		out[i] = ModelInfo{ID: m, Object: "model", OwnedBy: "openai", Provider: "openai"}
	}
	return out
}

func (o *OpenAI) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "openai", Model: req.Model, Timestamp: start}

	body, err := json.Marshal(req)
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, fmt.Errorf("read response: %w", err)
	}

	meta.Latency = time.Since(start)

	if resp.StatusCode != http.StatusOK {
		meta.Error = string(respBody)
		return ChatResponse{}, meta, fmt.Errorf("openai error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, fmt.Errorf("decode response: %w", err)
	}

	meta.Cost = openAIPricing(req.Model).Cost(chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens)
	return chatResp, meta, nil
}

func (o *OpenAI) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error) {
	return streamOpenAICompatible(ctx, o.client, o.baseURL+"/chat/completions", o.apiKey, req, "openai", nil)
}

func openAIPricing(model string) ModelPricing {
	switch model {
	case "gpt-4o":
		return ModelPricing{InputPerMillion: 2.50, OutputPerMillion: 10.00}
	case "gpt-4o-mini":
		return ModelPricing{InputPerMillion: 0.15, OutputPerMillion: 0.60}
	case "gpt-4-turbo":
		return ModelPricing{InputPerMillion: 10.00, OutputPerMillion: 30.00}
	case "gpt-4":
		return ModelPricing{InputPerMillion: 30.00, OutputPerMillion: 60.00}
	case "gpt-3.5-turbo":
		return ModelPricing{InputPerMillion: 0.50, OutputPerMillion: 1.50}
	case "o1":
		return ModelPricing{InputPerMillion: 15.00, OutputPerMillion: 60.00}
	case "o1-mini":
		return ModelPricing{InputPerMillion: 3.00, OutputPerMillion: 12.00}
	case "o3-mini":
		return ModelPricing{InputPerMillion: 1.10, OutputPerMillion: 4.40}
	default:
		return ModelPricing{InputPerMillion: 5.00, OutputPerMillion: 15.00}
	}
}
