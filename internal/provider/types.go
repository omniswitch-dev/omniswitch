package provider

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// ChatRequest is the OpenAI-compatible chat completion request format.
// All providers must accept this canonical format.
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
}

// Message represents a single message in the chat conversation.
type Message struct {
	Role         string        `json:"role"`
	Content      string        `json:"content"`
	ContentParts []ContentPart `json:"-"`
}

type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	if len(raw.Content) == 0 || string(raw.Content) == "null" {
		m.Content = ""
		m.ContentParts = nil
		return nil
	}
	var text string
	if err := json.Unmarshal(raw.Content, &text); err == nil {
		m.Content = text
		m.ContentParts = nil
		return nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(raw.Content, &parts); err != nil {
		return err
	}
	m.ContentParts = parts
	m.Content = contentPartsText(parts)
	return nil
}

func (m Message) MarshalJSON() ([]byte, error) {
	payload := struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}{
		Role:    m.Role,
		Content: m.Content,
	}
	if len(m.ContentParts) > 0 {
		payload.Content = m.ContentParts
	}
	return json.Marshal(payload)
}

func (m Message) Text() string {
	if len(m.ContentParts) > 0 {
		return contentPartsText(m.ContentParts)
	}
	return m.Content
}

func contentPartsText(parts []ContentPart) string {
	text := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" || part.Type == "input_text" || part.Text != "" {
			text = append(text, part.Text)
		}
	}
	return strings.Join(text, "\n")
}

// ChatResponse is the OpenAI-compatible chat completion response format.
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a single completion choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage tracks token consumption for a single request.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ModelInfo describes a model exposed through the gateway.
type ModelInfo struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	Created  int64  `json:"created"`
	OwnedBy  string `json:"owned_by"`
	Provider string `json:"provider"`
}

// ProviderMeta contains per-request metadata produced by a provider call.
type ProviderMeta struct {
	Provider     string        `json:"provider"`
	ProviderType string        `json:"provider_type,omitempty"`
	Model        string        `json:"model"`
	Latency      time.Duration `json:"latency"`
	Cost         float64       `json:"cost"`
	Cached       bool          `json:"cached"`
	Retries      int           `json:"retries"`
	Fallback     bool          `json:"fallback"`
	Error        string        `json:"error,omitempty"`
	Timestamp    time.Time     `json:"timestamp"`
}

type StreamProvider interface {
	ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error)
}

// EmbeddingRequest and EmbeddingResponse mirror the OpenAI embeddings API.
// Input intentionally remains JSON-compatible because providers accept either
// one string, token IDs, or an array of strings.
type EmbeddingRequest struct {
	Model          string `json:"model"`
	Input          any    `json:"input"`
	EncodingFormat string `json:"encoding_format,omitempty"`
	Dimensions     *int   `json:"dimensions,omitempty"`
	User           string `json:"user,omitempty"`
}

type Embedding struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

type EmbeddingResponse struct {
	Object string      `json:"object"`
	Data   []Embedding `json:"data"`
	Model  string      `json:"model"`
	Usage  Usage       `json:"usage"`
}

type EmbeddingProvider interface {
	Embeddings(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, ProviderMeta, error)
}

// RerankRequest and RerankResponse mirror the common Cohere/Jina-style rerank
// shape while keeping documents flexible enough for strings or document
// objects.
type RerankRequest struct {
	Model           string `json:"model"`
	Query           string `json:"query"`
	Documents       []any  `json:"documents"`
	TopN            *int   `json:"top_n,omitempty"`
	ReturnDocuments bool   `json:"return_documents,omitempty"`
	MaxChunksPerDoc *int   `json:"max_chunks_per_doc,omitempty"`
	User            string `json:"user,omitempty"`
}

type RerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
	Document       any     `json:"document,omitempty"`
}

type RerankResponse struct {
	ID      string         `json:"id,omitempty"`
	Object  string         `json:"object,omitempty"`
	Model   string         `json:"model,omitempty"`
	Results []RerankResult `json:"results"`
	Usage   Usage          `json:"usage,omitempty"`
}

type RerankProvider interface {
	Rerank(ctx context.Context, req RerankRequest) (RerankResponse, ProviderMeta, error)
}

type ChatResponseChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
}

type ChunkChoice struct {
	Index        int     `json:"index"`
	Delta        Message `json:"delta"`
	FinishReason string  `json:"finish_reason,omitempty"`
}

// ModelPricing holds cost per token for a specific model.
type ModelPricing struct {
	InputPerMillion  float64
	OutputPerMillion float64
}

// Cost calculates the total cost for the given token counts.
func (p ModelPricing) Cost(inputTokens, outputTokens int) float64 {
	return (float64(inputTokens) * p.InputPerMillion / 1_000_000) +
		(float64(outputTokens) * p.OutputPerMillion / 1_000_000)
}
