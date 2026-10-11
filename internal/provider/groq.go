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

// Groq implements the Provider interface for the Groq API.
// Groq uses an OpenAI-compatible API format, so the adapter is thin.
type Groq struct {
	apiKey string
	client *http.Client
}

// NewGroq creates a new Groq provider.
func NewGroq(apiKey string) *Groq {
	return &Groq{
		apiKey: apiKey,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

func (g *Groq) Name() string { return "groq" }

func (g *Groq) Models() []ModelInfo {
	return catalogModelInfos("groq", "groq")
}

func (g *Groq) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "groq", Model: req.Model, Timestamp: start}

	body, err := json.Marshal(req)
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+g.apiKey)

	resp, err := g.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, fmt.Errorf("groq request: %w", err)
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
		return ChatResponse{}, meta, fmt.Errorf("groq error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}

	meta.Cost = pricingFor("groq", req.Model).Cost(chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens)
	return chatResp, meta, nil
}

func (g *Groq) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error) {
	return streamOpenAICompatible(ctx, g.client, "https://api.groq.com/openai/v1/chat/completions", g.apiKey, req, "groq", nil)
}

// Pricing for Groq models is resolved from the consolidated catalog
// (internal/provider/catalog/models.json); see pricingFor in pricing.go.
