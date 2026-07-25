package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AzureOpenAI struct {
	name       string
	endpoint   string
	apiKey     string
	apiVersion string
	models     []string
	client     *http.Client
}

func NewAzureOpenAI(name, endpoint, apiKey, apiVersion string, models []string) *AzureOpenAI {
	if strings.TrimSpace(name) == "" {
		name = "azure"
	}
	if strings.TrimSpace(apiVersion) == "" {
		apiVersion = "2024-10-21"
	}
	return &AzureOpenAI{
		name:       strings.ToLower(strings.TrimSpace(name)),
		endpoint:   strings.TrimRight(endpoint, "/"),
		apiKey:     apiKey,
		apiVersion: apiVersion,
		models:     append([]string(nil), models...),
		client:     &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *AzureOpenAI) Name() string { return a.name }

func (a *AzureOpenAI) Models() []ModelInfo {
	models := a.models
	if len(models) == 0 {
		models = []string{"gpt-4o", "gpt-4o-mini"}
	}
	out := make([]ModelInfo, len(models))
	for i, model := range models {
		out[i] = ModelInfo{ID: model, Object: "model", OwnedBy: "azure-openai", Provider: a.name}
	}
	return out
}

func (a *AzureOpenAI) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: a.name, ProviderType: "azure", Model: req.Model, Timestamp: start}
	body, err := azureRequestBody(req)
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.chatURL(req.Model), bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api-key", a.apiKey)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, fmt.Errorf("azure openai request: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, err
	}
	meta.Latency = time.Since(start)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		meta.Error = string(payload)
		return ChatResponse{}, meta, fmt.Errorf("azure openai error (status %d): %s", resp.StatusCode, string(payload))
	}
	var chatResp ChatResponse
	if err := json.Unmarshal(payload, &chatResp); err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	if chatResp.Model == "" {
		chatResp.Model = req.Model
	}
	meta.Cost = openAIPricing(req.Model).Cost(chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens)
	return chatResp, meta, nil
}

func (a *AzureOpenAI) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error) {
	start := time.Now()
	req.Stream = true
	body, err := azureRequestBody(req)
	if err != nil {
		return nil, ProviderMeta{Provider: a.name, ProviderType: "azure", Model: req.Model, Timestamp: start, Error: err.Error()}, err
	}
	return streamOpenAICompatibleBody(ctx, a.client, a.chatURL(req.Model), "", body, req.Model, a.name, map[string]string{"api-key": a.apiKey}, start)
}

func (a *AzureOpenAI) chatURL(model string) string {
	deployment := url.PathEscape(model)
	return fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s", a.endpoint, deployment, url.QueryEscape(a.apiVersion))
}

func azureRequestBody(req ChatRequest) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	delete(payload, "model")
	return json.Marshal(payload)
}
