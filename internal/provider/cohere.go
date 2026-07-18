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

// Cohere implements native Cohere rerank support. Chat completions are not
// exposed through this provider; register a custom OpenAI-compatible Cohere
// proxy if chat compatibility is required.
type Cohere struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

func NewCohere(apiKey string) *Cohere {
	return &Cohere{
		apiKey:  apiKey,
		baseURL: "https://api.cohere.com/v2",
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *Cohere) Name() string { return "cohere" }

func (c *Cohere) Models() []ModelInfo {
	models := []string{"rerank-v3.5", "rerank-english-v3.0", "rerank-multilingual-v3.0"}
	out := make([]ModelInfo, len(models))
	for i, model := range models {
		out[i] = ModelInfo{ID: model, Object: "model", OwnedBy: "cohere", Provider: "cohere"}
	}
	return out
}

func (c *Cohere) ChatCompletion(context.Context, ChatRequest) (ChatResponse, ProviderMeta, error) {
	return ChatResponse{}, ProviderMeta{Provider: "cohere", Timestamp: time.Now().UTC()}, fmt.Errorf("cohere provider supports rerank only")
}

func (c *Cohere) Rerank(ctx context.Context, req RerankRequest) (RerankResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "cohere", Model: req.Model, Timestamp: start}
	req.Documents = cohereRerankDocuments(req.Documents)
	body, err := json.Marshal(req)
	if err != nil {
		return RerankResponse{}, meta, fmt.Errorf("marshal cohere rerank request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return RerankResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		meta.Latency = time.Since(start)
		return RerankResponse{}, meta, fmt.Errorf("cohere rerank request: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		meta.Latency = time.Since(start)
		return RerankResponse{}, meta, fmt.Errorf("read cohere rerank response: %w", err)
	}
	meta.Latency = time.Since(start)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		meta.Error = string(payload)
		return RerankResponse{}, meta, fmt.Errorf("cohere rerank error (status %d): %s", resp.StatusCode, string(payload))
	}
	var response RerankResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return RerankResponse{}, meta, fmt.Errorf("decode cohere rerank response: %w", err)
	}
	if response.Model == "" {
		response.Model = req.Model
	}
	if response.Object == "" {
		response.Object = "list"
	}
	return response, meta, nil
}

func cohereRerankDocuments(documents []any) []any {
	converted := make([]any, 0, len(documents))
	for _, document := range documents {
		if text, ok := document.(string); ok {
			converted = append(converted, text)
			continue
		}
		payload, err := json.Marshal(document)
		if err != nil {
			converted = append(converted, fmt.Sprint(document))
			continue
		}
		converted = append(converted, string(payload))
	}
	return converted
}
