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
		client:  newProviderClient(defaultRequestTimeout),
	}
}

func (o *OpenAI) Name() string { return "openai" }

func (o *OpenAI) Models() []ModelInfo {
	return catalogModelInfos("openai", "openai")
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		meta.Latency = time.Since(start)
		upstreamErr := newUpstreamError(meta.Provider, "openai error", resp)
		meta.Error = upstreamErr.Body
		return ChatResponse{}, meta, upstreamErr
	}
	respBody, err := readLimited(resp.Body, maxResponseBodyBytes)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, fmt.Errorf("read response: %w", err)
	}

	meta.Latency = time.Since(start)

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, fmt.Errorf("decode response: %w", err)
	}

	meta.Cost = pricingFor("openai", req.Model).Cost(chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens)
	return chatResp, meta, nil
}

func (o *OpenAI) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error) {
	return streamOpenAICompatible(ctx, streamingClient(o.client), o.baseURL+"/chat/completions", o.apiKey, withStreamUsage(req), "openai", nil)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		meta.Latency = time.Since(start)
		upstreamErr := newUpstreamError(meta.Provider, "openai embeddings error", resp)
		meta.Error = upstreamErr.Body
		return EmbeddingResponse{}, meta, upstreamErr
	}
	payload, err := readLimited(resp.Body, maxResponseBodyBytes)
	if err != nil {
		meta.Latency = time.Since(start)
		return EmbeddingResponse{}, meta, fmt.Errorf("read embeddings response: %w", err)
	}
	meta.Latency = time.Since(start)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		meta.Latency = time.Since(start)
		upstreamErr := newUpstreamError(meta.Provider, "openai image error", resp)
		meta.Error = upstreamErr.Body
		return ImageResponse{}, meta, upstreamErr
	}
	payload, err := readLimited(resp.Body, maxResponseBodyBytes)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ImageResponse{}, meta, fmt.Errorf("read image response: %w", err)
	}
	meta.Latency = time.Since(start)
	var response ImageResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		meta.Error = err.Error()
		return ImageResponse{}, meta, fmt.Errorf("decode image response: %w", err)
	}
	meta.Cost = estimateOpenAIImageCost(req, response.Usage)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		meta.Latency = time.Since(start)
		upstreamErr := newUpstreamError(meta.Provider, "openai transcription error", resp)
		meta.Error = upstreamErr.Body
		return TranscriptionResponse{}, meta, upstreamErr
	}
	payload, err := readLimited(resp.Body, maxResponseBodyBytes)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return TranscriptionResponse{}, meta, fmt.Errorf("read transcription response: %w", err)
	}
	meta.Latency = time.Since(start)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		meta.Latency = time.Since(start)
		upstreamErr := newUpstreamError(meta.Provider, "openai speech error", resp)
		_ = resp.Body.Close()
		meta.Error = upstreamErr.Body
		return nil, "", meta, upstreamErr
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	return resp.Body, contentType, meta, nil
}

// OpenAI image prices, verified 2026-10-18 against
// https://cdn.openai.com/API/docs/txt/llms-models-pricing.txt.
var (
	// dall-e-3 per-image price by quality, then by size class.
	dallE3Prices = map[string]map[string]float64{
		"standard": {"square": 0.04, "wide": 0.08},
		"hd":       {"square": 0.08, "wide": 0.12},
	}
	// dall-e-2 per-image price by size.
	dallE2Prices = map[string]float64{"256x256": 0.016, "512x512": 0.018, "1024x1024": 0.02}
	// gpt-image-1 per-image price by quality, then by size class, used
	// only when the response carries no token usage.
	gptImage1Prices = map[string]map[string]float64{
		"low":    {"square": 0.011, "wide": 0.016},
		"medium": {"square": 0.042, "wide": 0.063},
		"high":   {"square": 0.167, "wide": 0.25},
	}
)

// gpt-image-1 token prices per million tokens.
const (
	gptImage1TextInputPerMillion  = 5.0
	gptImage1ImageInputPerMillion = 10.0
	gptImage1OutputPerMillion     = 40.0
	// unknownImageModelPrice keeps the previous flat estimate for image
	// models that are not in the tables above.
	unknownImageModelPrice = 0.04
)

// estimateOpenAIImageCost returns the cost of an image generation call from
// OpenAI's published per-image (DALL-E) or per-token (gpt-image-1) prices.
func estimateOpenAIImageCost(req ImageRequest, usage *ImageUsage) float64 {
	n := req.N
	if n <= 0 {
		n = 1
	}
	model := strings.ToLower(strings.TrimSpace(req.Model))
	quality := strings.ToLower(strings.TrimSpace(req.Quality))
	size := strings.ToLower(strings.TrimSpace(req.Size))
	sizeClass := "square"
	if size != "" && size != "auto" && size != "1024x1024" {
		sizeClass = "wide"
	}
	switch {
	case model == "gpt-image-1" || strings.HasPrefix(model, "gpt-image-1-2"):
		if usage != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
			textIn, imageIn := usage.InputTokens, 0
			if d := usage.InputTokensDetails; d != nil {
				textIn, imageIn = d.TextTokens, d.ImageTokens
			}
			return (float64(textIn)*gptImage1TextInputPerMillion +
				float64(imageIn)*gptImage1ImageInputPerMillion +
				float64(usage.OutputTokens)*gptImage1OutputPerMillion) / 1_000_000
		}
		tier, ok := gptImage1Prices[quality]
		if !ok {
			// "auto" or unset: estimate at the medium tier.
			tier = gptImage1Prices["medium"]
		}
		return float64(n) * tier[sizeClass]
	case strings.HasPrefix(model, "dall-e-3"):
		tier, ok := dallE3Prices[quality]
		if !ok {
			tier = dallE3Prices["standard"]
		}
		return float64(n) * tier[sizeClass]
	case strings.HasPrefix(model, "dall-e-2"):
		price, ok := dallE2Prices[size]
		if !ok {
			price = dallE2Prices["1024x1024"]
		}
		return float64(n) * price
	default:
		return float64(n) * unknownImageModelPrice
	}
}

// Pricing for OpenAI models is resolved from the consolidated catalog
// (internal/provider/catalog/models.json); see pricingFor in pricing.go.
