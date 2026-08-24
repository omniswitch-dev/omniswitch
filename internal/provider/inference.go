package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type InferenceReplica struct {
	URL      string
	Runtime  string
	Models   []string
	Priority int
}

type InferenceBackend struct {
	name     string
	replicas []InferenceReplica
	client   *http.Client
	health   *healthTracker
}

type healthTracker struct {
	mu        sync.Mutex
	latency   map[string]time.Duration
	queueLen  map[string]int
	lastCheck map[string]time.Time
}

func NewInferenceBackend(name string, replicas []InferenceReplica, healthInterval time.Duration) *InferenceBackend {
	if healthInterval <= 0 {
		healthInterval = 10 * time.Second
	}
	backend := &InferenceBackend{
		name:     strings.TrimSpace(name),
		replicas: append([]InferenceReplica(nil), replicas...),
		client:   &http.Client{Timeout: 120 * time.Second},
		health: &healthTracker{
			latency:   map[string]time.Duration{},
			queueLen:  map[string]int{},
			lastCheck: map[string]time.Time{},
		},
	}
	if backend.name == "" {
		backend.name = "inference"
	}
	go backend.probeLoop(healthInterval)
	return backend
}

func (b *InferenceBackend) Name() string { return b.name }

func (b *InferenceBackend) Models() []ModelInfo {
	seen := map[string]bool{}
	var models []ModelInfo
	for _, replica := range b.replicas {
		for _, model := range replica.Models {
			model = strings.TrimSpace(model)
			if model == "" || seen[model] {
				continue
			}
			seen[model] = true
			models = append(models, ModelInfo{ID: model, Object: "model", OwnedBy: b.name, Provider: b.name})
		}
	}
	return models
}

func (b *InferenceBackend) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: b.name, ProviderType: "inference", Model: req.Model, Timestamp: start}
	replica, ok := b.selectReplica(req.Model)
	if !ok {
		return ChatResponse{}, meta, fmt.Errorf("no inference replica supports model %q", req.Model)
	}
	var resp ChatResponse
	var err error
	switch normalizedRuntime(replica) {
	case "tgi":
		resp, err = b.chatTGI(ctx, replica, req)
	case "triton":
		err = fmt.Errorf("triton runtime requires a model-specific adapter; use an OpenAI-compatible vLLM endpoint for chat completions")
	default:
		resp, err = b.chatOpenAICompatible(ctx, replica, req)
	}
	meta.Latency = time.Since(start)
	b.health.record(replica.URL, meta.Latency, 0)
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, meta, nil
}

func (b *InferenceBackend) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: b.name, ProviderType: "inference", Model: req.Model, Timestamp: start}
	replica, ok := b.selectReplica(req.Model)
	if !ok {
		return nil, meta, fmt.Errorf("no inference replica supports model %q", req.Model)
	}
	if normalizedRuntime(replica) != "openai" {
		resp, _, err := b.ChatCompletion(ctx, req)
		if err != nil {
			meta.Error = err.Error()
			meta.Latency = time.Since(start)
			return nil, meta, err
		}
		meta.Latency = time.Since(start)
		return StreamFromResponse(ctx, resp), meta, nil
	}
	chunks, meta, err := streamOpenAICompatible(ctx, b.client, strings.TrimRight(replica.URL, "/")+"/chat/completions", "", req, b.name, nil)
	meta.ProviderType = "inference"
	if err == nil {
		b.health.record(replica.URL, meta.Latency, 0)
	}
	return chunks, meta, err
}

func (b *InferenceBackend) selectReplica(model string) (InferenceReplica, bool) {
	var candidates []InferenceReplica
	for _, replica := range b.replicas {
		if len(replica.Models) == 0 || containsModel(replica.Models, model) {
			candidates = append(candidates, replica)
		}
	}
	if len(candidates) == 0 {
		return InferenceReplica{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return b.health.score(candidates[i]) < b.health.score(candidates[j])
	})
	return candidates[0], true
}

func (b *InferenceBackend) chatOpenAICompatible(ctx context.Context, replica InferenceReplica, req ChatRequest) (ChatResponse, error) {
	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(replica.URL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("inference replica %s error (status %d): %s", replica.URL, resp.StatusCode, string(payload))
	}
	var out ChatResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return ChatResponse{}, err
	}
	return out, nil
}

func (b *InferenceBackend) chatTGI(ctx context.Context, replica InferenceReplica, req ChatRequest) (ChatResponse, error) {
	prompt := chatPrompt(req.Messages)
	body, err := json.Marshal(map[string]any{
		"inputs": prompt,
		"parameters": map[string]any{
			"max_new_tokens": optionalInt(req.MaxTokens, 512),
			"temperature":    optionalFloat(req.Temperature, 0.7),
			"top_p":          optionalFloat(req.TopP, 1),
		},
	})
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(replica.URL, "/")+"/generate", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("inference TGI replica %s error (status %d): %s", replica.URL, resp.StatusCode, string(payload))
	}
	var tgi struct {
		GeneratedText string `json:"generated_text"`
	}
	if err := json.Unmarshal(payload, &tgi); err != nil {
		return ChatResponse{}, err
	}
	return ChatResponse{
		ID:      fmt.Sprintf("inf_%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []Choice{{Index: 0, Message: Message{Role: "assistant", Content: tgi.GeneratedText}, FinishReason: "stop"}},
	}, nil
}

func (b *InferenceBackend) probeLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		b.probeOnce()
		<-ticker.C
	}
}

func (b *InferenceBackend) probeOnce() {
	for _, replica := range b.replicas {
		go func(replica InferenceReplica) {
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			url := strings.TrimRight(replica.URL, "/") + "/health"
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return
			}
			resp, err := b.client.Do(req)
			if err != nil {
				b.health.record(replica.URL, 30*time.Second, 100)
				return
			}
			defer resp.Body.Close()
			queueLen := 0
			_ = json.NewDecoder(resp.Body).Decode(&struct {
				QueueLen *int `json:"queue_len,omitempty"`
			}{QueueLen: &queueLen})
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				b.health.record(replica.URL, time.Since(start), queueLen)
			}
		}(replica)
	}
}

func (t *healthTracker) record(url string, latency time.Duration, queueLen int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.latency[url] = latency
	t.queueLen[url] = queueLen
	t.lastCheck[url] = time.Now().UTC()
}

func (t *healthTracker) score(replica InferenceReplica) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	latency := t.latency[replica.URL]
	if latency <= 0 {
		latency = 500 * time.Millisecond
	}
	queueLen := t.queueLen[replica.URL]
	priorityPenalty := time.Duration(maxInt(replica.Priority, 0)) * 100 * time.Millisecond
	return latency*time.Duration(1+queueLen/10) + priorityPenalty
}

func normalizedRuntime(replica InferenceReplica) string {
	runtime := strings.ToLower(strings.TrimSpace(replica.Runtime))
	switch runtime {
	case "tgi", "text-generation-inference":
		return "tgi"
	case "triton":
		return "triton"
	default:
		return "openai"
	}
}

func containsModel(models []string, target string) bool {
	for _, model := range models {
		if model == target {
			return true
		}
	}
	return false
}

func chatPrompt(messages []Message) string {
	var parts []string
	for _, message := range messages {
		parts = append(parts, strings.ToUpper(message.Role)+": "+message.Text())
	}
	return strings.Join(parts, "\n") + "\nASSISTANT:"
}

func optionalInt(value *int, fallback int) int {
	if value == nil || *value <= 0 {
		return fallback
	}
	return *value
}

func optionalFloat(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
