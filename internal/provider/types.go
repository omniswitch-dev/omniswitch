package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ChatRequest is the OpenAI-compatible chat completion request format.
// All providers must accept this canonical format.
//
// Beyond the fields modeled explicitly, ChatRequest captures every other
// top-level key an OpenAI SDK sends in Extra and forwards it verbatim to
// OpenAI-compatible upstreams (openai, azure, groq, custom, and presets built
// on Custom). This keeps passthrough fidelity for parameters OmniSwitch does
// not (yet) model by name, instead of silently dropping them. See
// UnmarshalJSON/MarshalJSON below.
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

	// ResponseFormat requests structured output (text, json_object, or
	// json_schema). Adapters that cannot honour a schema natively must
	// translate it or reject with UnsupportedParameterError.
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	// MaxCompletionTokens is the modern replacement for MaxTokens used by
	// reasoning models. When both are set, MaxCompletionTokens wins.
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`
	// N requests multiple independent completions. Only OpenAI-compatible
	// upstreams can honour N>1; other adapters must reject.
	N                 *int              `json:"n,omitempty"`
	Seed              *int              `json:"seed,omitempty"`
	PresencePenalty   *float64          `json:"presence_penalty,omitempty"`
	FrequencyPenalty  *float64          `json:"frequency_penalty,omitempty"`
	LogitBias         map[string]int    `json:"logit_bias,omitempty"`
	User              string            `json:"user,omitempty"`
	StreamOptions     *StreamOptions    `json:"stream_options,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort   string            `json:"reasoning_effort,omitempty"`
	ServiceTier       string            `json:"service_tier,omitempty"`
	Store             *bool             `json:"store,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`

	// Extra holds any top-level request keys not modeled above (future or
	// provider-beta OpenAI parameters). It round-trips through
	// UnmarshalJSON/MarshalJSON and is forwarded byte-for-byte to
	// OpenAI-compatible upstreams.
	Extra map[string]json.RawMessage `json:"-"`
}

// ResponseFormat mirrors the OpenAI response_format request field used to
// request plain text, a generic JSON object, or a JSON-Schema-validated
// structured output.
type ResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *JSONSchemaSpec `json:"json_schema,omitempty"`
}

// JSONSchemaSpec is the json_schema payload nested under response_format.
type JSONSchemaSpec struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Schema      any    `json:"schema,omitempty"`
	Strict      *bool  `json:"strict,omitempty"`
}

// StreamOptions mirrors the OpenAI stream_options request field.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// chatRequestKnownFields lists every JSON key modeled explicitly by
// ChatRequest. Anything outside this set is preserved in Extra.
var chatRequestKnownFields = map[string]struct{}{
	"model":                 {},
	"messages":              {},
	"temperature":           {},
	"max_tokens":            {},
	"top_p":                 {},
	"stream":                {},
	"stop":                  {},
	"tools":                 {},
	"tool_choice":           {},
	"response_format":       {},
	"max_completion_tokens": {},
	"n":                     {},
	"seed":                  {},
	"presence_penalty":      {},
	"frequency_penalty":     {},
	"logit_bias":            {},
	"user":                  {},
	"stream_options":        {},
	"parallel_tool_calls":   {},
	"reasoning_effort":      {},
	"service_tier":          {},
	"store":                 {},
	"metadata":              {},
}

// UnmarshalJSON decodes the known ChatRequest fields normally and stashes
// every other top-level key in Extra so it can be forwarded verbatim later.
func (c *ChatRequest) UnmarshalJSON(data []byte) error {
	type wire ChatRequest
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*c = ChatRequest(w)
	c.Extra = nil

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for key := range chatRequestKnownFields {
		delete(raw, key)
	}
	if len(raw) > 0 {
		c.Extra = raw
	}
	return nil
}

// MarshalJSON encodes the known ChatRequest fields and merges in any Extra
// keys that were captured on decode, so unmodeled OpenAI parameters survive
// a full round trip to OpenAI-compatible upstreams.
func (c ChatRequest) MarshalJSON() ([]byte, error) {
	type wire ChatRequest
	base, err := json.Marshal(wire(c))
	if err != nil {
		return nil, err
	}
	if len(c.Extra) == 0 {
		return base, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for key, value := range c.Extra {
		if _, exists := merged[key]; !exists {
			merged[key] = value
		}
	}
	return json.Marshal(merged)
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
	Name         string        `json:"name,omitempty"`
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
		Name       string          `json:"name,omitempty"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.Name = raw.Name
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
		Name       string     `json:"name,omitempty"`
		Content    any        `json:"content"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}
	w := wire{
		Role:       m.Role,
		Name:       m.Name,
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

// UnsupportedParameterError signals that an adapter received a request field
// it has no faithful way to honour (e.g. Anthropic given seed or n>1).
// Handlers must surface this as an HTTP 400 with an OpenAI-style
// unsupported_parameter error body instead of silently dropping the field.
type UnsupportedParameterError struct {
	Provider string
	Param    string
	Message  string
}

func (e *UnsupportedParameterError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("%s does not support the %q parameter", e.Provider, e.Param)
}

// NewUnsupportedParameterError builds an UnsupportedParameterError with a
// default, descriptive message.
func NewUnsupportedParameterError(providerName, param string) *UnsupportedParameterError {
	return &UnsupportedParameterError{
		Provider: providerName,
		Param:    param,
		Message:  fmt.Sprintf("%s does not support the %q parameter; remove it or use an OpenAI-compatible provider", providerName, param),
	}
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
