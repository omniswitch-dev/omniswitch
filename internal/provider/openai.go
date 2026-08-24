package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
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

func (o *OpenAI) Embeddings(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "openai", Model: req.Model, Timestamp: start}
	body, err := json.Marshal(req)
	if err != nil {
		return EmbeddingResponse{}, meta, fmt.Errorf("marshal embeddings request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return EmbeddingResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	resp, err := o.client.Do(httpReq)
	if err != nil {
		meta.Latency = time.Since(start)
		return EmbeddingResponse{}, meta, fmt.Errorf("openai embeddings request: %w", err)
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
		return EmbeddingResponse{}, meta, fmt.Errorf("openai embeddings error (status %d): %s", resp.StatusCode, string(payload))
	}
	var response EmbeddingResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return EmbeddingResponse{}, meta, fmt.Errorf("decode embeddings response: %w", err)
	}
	meta.Cost = EstimateCost("openai", req.Model, response.Usage)
	return response, meta, nil
}

func (o *OpenAI) ImageGeneration(ctx context.Context, req ImageRequest) (ImageResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "openai", Model: req.Model, Timestamp: start}
	body, err := json.Marshal(req)
	if err != nil {
		meta.Error = err.Error()
		return ImageResponse{}, meta, fmt.Errorf("marshal image request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/images/generations", bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return ImageResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	resp, err := o.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ImageResponse{}, meta, fmt.Errorf("openai image request: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ImageResponse{}, meta, fmt.Errorf("read image response: %w", err)
	}
	meta.Latency = time.Since(start)
	if resp.StatusCode != http.StatusOK {
		meta.Error = string(payload)
		return ImageResponse{}, meta, fmt.Errorf("openai image error (status %d): %s", resp.StatusCode, string(payload))
	}
	var response ImageResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		meta.Error = err.Error()
		return ImageResponse{}, meta, fmt.Errorf("decode image response: %w", err)
	}
	meta.Cost = estimateOpenAIImageCost(req)
	return response, meta, nil
}

func (o *OpenAI) Transcription(ctx context.Context, req TranscriptionRequest) (TranscriptionResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "openai", Model: req.Model, Timestamp: start}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if req.File == nil {
		return TranscriptionResponse{}, meta, fmt.Errorf("audio file is required")
	}
	filename := strings.TrimSpace(req.Filename)
	if filename == "" {
		filename = "audio"
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return TranscriptionResponse{}, meta, fmt.Errorf("create multipart file: %w", err)
	}
	if _, err := io.Copy(part, req.File); err != nil {
		return TranscriptionResponse{}, meta, fmt.Errorf("copy multipart file: %w", err)
	}
	if err := writer.WriteField("model", req.Model); err != nil {
		return TranscriptionResponse{}, meta, err
	}
	if req.Language != "" {
		_ = writer.WriteField("language", req.Language)
	}
	if req.Prompt != "" {
		_ = writer.WriteField("prompt", req.Prompt)
	}
	if req.ResponseFormat != "" {
		_ = writer.WriteField("response_format", req.ResponseFormat)
	}
	if req.Temperature != nil {
		_ = writer.WriteField("temperature", fmt.Sprintf("%g", *req.Temperature))
	}
	if err := writer.Close(); err != nil {
		return TranscriptionResponse{}, meta, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return TranscriptionResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	resp, err := o.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return TranscriptionResponse{}, meta, fmt.Errorf("openai transcription request: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return TranscriptionResponse{}, meta, fmt.Errorf("read transcription response: %w", err)
	}
	meta.Latency = time.Since(start)
	if resp.StatusCode != http.StatusOK {
		meta.Error = string(payload)
		return TranscriptionResponse{}, meta, fmt.Errorf("openai transcription error (status %d): %s", resp.StatusCode, string(payload))
	}
	var response TranscriptionResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		meta.Error = err.Error()
		return TranscriptionResponse{}, meta, fmt.Errorf("decode transcription response: %w", err)
	}
	return response, meta, nil
}

func (o *OpenAI) Speech(ctx context.Context, req SpeechRequest) (io.ReadCloser, string, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: "openai", Model: req.Model, Timestamp: start}
	body, err := json.Marshal(req)
	if err != nil {
		meta.Error = err.Error()
		return nil, "", meta, fmt.Errorf("marshal speech request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return nil, "", meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	resp, err := o.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return nil, "", meta, fmt.Errorf("openai speech request: %w", err)
	}
	meta.Latency = time.Since(start)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		payload, _ := io.ReadAll(resp.Body)
		meta.Error = string(payload)
		return nil, "", meta, fmt.Errorf("openai speech error (status %d): %s", resp.StatusCode, string(payload))
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	return resp.Body, contentType, meta, nil
}

func estimateOpenAIImageCost(req ImageRequest) float64 {
	n := req.N
	if n <= 0 {
		n = 1
	}
	model := strings.ToLower(req.Model)
	quality := strings.ToLower(req.Quality)
	size := strings.ToLower(req.Size)
	unit := 0.04
	if strings.Contains(model, "dall-e-2") {
		unit = 0.02
	} else if strings.Contains(model, "gpt-image") {
		unit = 0.04
	}
	if quality == "hd" || strings.Contains(size, "1792") {
		unit *= 2
	}
	return float64(n) * unit
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
