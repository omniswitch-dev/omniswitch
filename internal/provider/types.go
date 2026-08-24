package provider

import (
	"context"
	"encoding/json"
	"io"
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
	Tools       []Tool    `json:"tools,omitempty"`
	ToolChoice  any       `json:"tool_choice,omitempty"`
}

// Tool represents an OpenAI-compatible tool definition.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction describes a callable function.
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// ToolCall represents an LLM-initiated function invocation.
type ToolCall struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function,omitempty"`
}

// FunctionCall holds the function name and its JSON-encoded arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message represents a single message in the chat conversation.
type Message struct {
	Role         string        `json:"role"`
	Content      string        `json:"content"`
	ContentParts []ContentPart `json:"-"`
	ToolCalls    []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID   string        `json:"tool_call_id,omitempty"`
}

type ContentPart struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	ImageURL   *ImageURL   `json:"image_url,omitempty"`
	InputAudio *InputAudio `json:"input_audio,omitempty"`
}

type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type InputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.ToolCalls = raw.ToolCalls
	m.ToolCallID = raw.ToolCallID
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
	type wire struct {
		Role       string     `json:"role"`
		Content    any        `json:"content"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}
	w := wire{
		Role:       m.Role,
		Content:    m.Content,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ContentParts) > 0 {
		w.Content = m.ContentParts
	}
	return json.Marshal(w)
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

type ImageRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	Quality        string `json:"quality,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	User           string `json:"user,omitempty"`
}

type ImageResponse struct {
	Created int64       `json:"created"`
	Data    []ImageData `json:"data"`
}

type ImageData struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type ImageProvider interface {
	ImageGeneration(ctx context.Context, req ImageRequest) (ImageResponse, ProviderMeta, error)
}

type TranscriptionRequest struct {
	File           io.Reader `json:"-"`
	Model          string    `json:"model"`
	Language       string    `json:"language,omitempty"`
	Prompt         string    `json:"prompt,omitempty"`
	ResponseFormat string    `json:"response_format,omitempty"`
	Temperature    *float64  `json:"temperature,omitempty"`
	Filename       string    `json:"-"`
	ContentType    string    `json:"-"`
}

type TranscriptionResponse struct {
	Text string `json:"text"`
}

type SpeechRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format,omitempty"`
	Speed          float64 `json:"speed,omitempty"`
}

type AudioProvider interface {
	Transcription(ctx context.Context, req TranscriptionRequest) (TranscriptionResponse, ProviderMeta, error)
	Speech(ctx context.Context, req SpeechRequest) (io.ReadCloser, string, ProviderMeta, error)
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
