package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func streamOpenAICompatible(ctx context.Context, client *http.Client, url string, apiKey string, req ChatRequest, providerName string, extraHeaders map[string]string) (<-chan ChatResponseChunk, ProviderMeta, error) {
	start := time.Now()
	req.Stream = true
	meta := ProviderMeta{Provider: providerName, Model: req.Model, Timestamp: start}

	body, err := json.Marshal(req)
	if err != nil {
		meta.Error = err.Error()
		return nil, meta, fmt.Errorf("marshal stream request: %w", err)
	}
	return streamOpenAICompatibleBody(ctx, client, url, apiKey, body, req.Model, providerName, extraHeaders, start)
}

func streamOpenAICompatibleBody(ctx context.Context, client *http.Client, url string, apiKey string, body []byte, model string, providerName string, extraHeaders map[string]string, start time.Time) (<-chan ChatResponseChunk, ProviderMeta, error) {
	meta := ProviderMeta{Provider: providerName, Model: model, Timestamp: start}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return nil, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for key, value := range extraHeaders {
		httpReq.Header.Set(key, value)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return nil, meta, fmt.Errorf("%s stream request: %w", providerName, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		upstreamErr := newUpstreamError(meta.Provider, providerName+" stream error", resp)
		_ = resp.Body.Close()
		meta.Error = upstreamErr.Body
		return nil, meta, upstreamErr
	}
	meta.Latency = time.Since(start)

	return streamOpenAIEvents(ctx, resp.Body, providerName), meta, nil
}
