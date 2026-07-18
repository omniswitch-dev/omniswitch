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

// Custom implements the Provider interface for any OpenAI-compatible API endpoint.
// This covers Ollama, vLLM, LiteLLM, Together AI, Fireworks, DeepSeek, Perplexity,
// Azure OpenAI, AWS Bedrock (via proxy), and any other OpenAI-compatible server.
type Custom struct {
	name         string
	baseURL      string
	apiKey       string
	client       *http.Client
	models       []string
	extraHeaders map[string]string
}

// CustomOption configures a Custom provider.
type CustomOption func(*Custom)

// WithCustomModels sets the advertised model list for the provider.
func WithCustomModels(models []string) CustomOption {
	return func(c *Custom) { c.models = models }
}

// WithCustomHeaders sets extra HTTP headers sent with every request.
func WithCustomHeaders(headers map[string]string) CustomOption {
	return func(c *Custom) { c.extraHeaders = headers }
}

// WithCustomTimeout sets the HTTP client timeout.
func WithCustomTimeout(d time.Duration) CustomOption {
	return func(c *Custom) { c.client.Timeout = d }
}

// NewCustom creates a new OpenAI-compatible custom provider.
//
// Usage examples:
//
//	NewCustom("ollama", "http://localhost:11434/v1", "", WithCustomModels([]string{"llama3.2"}))
//	NewCustom("together", "https://api.together.xyz/v1", "tok-xxx")
//	NewCustom("deepseek", "https://api.deepseek.com/v1", "sk-xxx")
//	NewCustom("azure-openai", "https://mydeployment.openai.azure.com/openai/deployments/gpt-4o/", azureKey,
//	    WithCustomHeaders(map[string]string{"api-key": azureKey}))
func NewCustom(name, baseURL, apiKey string, opts ...CustomOption) *Custom {
	baseURL = strings.TrimRight(baseURL, "/")
	c := &Custom{
		name:    strings.ToLower(strings.TrimSpace(name)),
		baseURL: baseURL,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Custom) Name() string { return c.name }

func (c *Custom) Models() []ModelInfo {
	out := make([]ModelInfo, len(c.models))
	for i, m := range c.models {
		out[i] = ModelInfo{
			ID:       m,
			Object:   "model",
			OwnedBy:  c.name,
			Provider: c.name,
		}
	}
	return out
}

func (c *Custom) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: c.name, ProviderType: "custom", Model: req.Model, Timestamp: start}

	body, err := json.Marshal(req)
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, fmt.Errorf("marshal request: %w", err)
	}

	url := c.chatURL()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey := c.bearerAPIKey(); apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for key, value := range c.extraHeaders {
		httpReq.Header.Set(key, value)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, fmt.Errorf("%s request: %w", c.name, err)
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
		return ChatResponse{}, meta, fmt.Errorf("%s error (status %d): %s", c.name, resp.StatusCode, string(respBody))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, fmt.Errorf("decode response: %w", err)
	}

	// Try provider-specific pricing first, fall back to generic estimate.
	meta.Cost = EstimateCost(c.name, req.Model, chatResp.Usage)
	return chatResp, meta, nil
}

func (c *Custom) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error) {
	return streamOpenAICompatible(ctx, c.client, c.chatURL(), c.bearerAPIKey(), req, c.name, c.extraHeaders)
}

func (c *Custom) Embeddings(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: c.name, ProviderType: "custom", Model: req.Model, Timestamp: start}
	body, err := json.Marshal(req)
	if err != nil {
		return EmbeddingResponse{}, meta, fmt.Errorf("marshal embeddings request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.embeddingURL(), bytes.NewReader(body))
	if err != nil {
		return EmbeddingResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey := c.bearerAPIKey(); apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for key, value := range c.extraHeaders {
		httpReq.Header.Set(key, value)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		meta.Latency = time.Since(start)
		return EmbeddingResponse{}, meta, fmt.Errorf("%s embeddings request: %w", c.name, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		meta.Latency = time.Since(start)
		return EmbeddingResponse{}, meta, fmt.Errorf("read embeddings response: %w", err)
	}
	meta.Latency = time.Since(start)
	if resp.StatusCode != http.StatusOK {
		meta.Error = string(payload)
		return EmbeddingResponse{}, meta, fmt.Errorf("%s embeddings error (status %d): %s", c.name, resp.StatusCode, string(payload))
	}
	var response EmbeddingResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return EmbeddingResponse{}, meta, fmt.Errorf("decode embeddings response: %w", err)
	}
	meta.Cost = EstimateCost(c.name, req.Model, response.Usage)
	return response, meta, nil
}

func (c *Custom) Rerank(ctx context.Context, req RerankRequest) (RerankResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: c.name, ProviderType: "custom", Model: req.Model, Timestamp: start}
	body, err := json.Marshal(req)
	if err != nil {
		return RerankResponse{}, meta, fmt.Errorf("marshal rerank request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rerankURL(), bytes.NewReader(body))
	if err != nil {
		return RerankResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey := c.bearerAPIKey(); apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for key, value := range c.extraHeaders {
		httpReq.Header.Set(key, value)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		meta.Latency = time.Since(start)
		return RerankResponse{}, meta, fmt.Errorf("%s rerank request: %w", c.name, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		meta.Latency = time.Since(start)
		return RerankResponse{}, meta, fmt.Errorf("read rerank response: %w", err)
	}
	meta.Latency = time.Since(start)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		meta.Error = string(payload)
		return RerankResponse{}, meta, fmt.Errorf("%s rerank error (status %d): %s", c.name, resp.StatusCode, string(payload))
	}
	var response RerankResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return RerankResponse{}, meta, fmt.Errorf("decode rerank response: %w", err)
	}
	if response.Model == "" {
		response.Model = req.Model
	}
	meta.Cost = EstimateCost(c.name, req.Model, response.Usage)
	return response, meta, nil
}

func (c *Custom) chatURL() string {
	// If the base URL already contains "chat/completions", use as-is (e.g. Azure deployments).
	if strings.Contains(c.baseURL, "/chat/completions") {
		return c.baseURL
	}
	return c.baseURL + "/chat/completions"
}

func (c *Custom) embeddingURL() string {
	if strings.Contains(c.baseURL, "/chat/completions") {
		return strings.Replace(c.baseURL, "/chat/completions", "/embeddings", 1)
	}
	return c.baseURL + "/embeddings"
}

func (c *Custom) rerankURL() string {
	if strings.Contains(c.baseURL, "/rerank") {
		return c.baseURL
	}
	if strings.Contains(c.baseURL, "/chat/completions") {
		return strings.Replace(c.baseURL, "/chat/completions", "/rerank", 1)
	}
	if strings.Contains(c.baseURL, "/embeddings") {
		return strings.Replace(c.baseURL, "/embeddings", "/rerank", 1)
	}
	return c.baseURL + "/rerank"
}

func (c *Custom) bearerAPIKey() string {
	if c.apiKey == "" || hasHeader(c.extraHeaders, "api-key") || hasHeader(c.extraHeaders, "x-api-key") {
		return ""
	}
	return c.apiKey
}

func hasHeader(headers map[string]string, target string) bool {
	for key := range headers {
		if strings.EqualFold(strings.TrimSpace(key), target) {
			return true
		}
	}
	return false
}
