package provider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Bedrock struct {
	name             string
	region           string
	accessKeyID      string
	secretAccessKey  string
	sessionToken     string
	guardrailID      string
	guardrailVersion string
	models           []string
	client           *http.Client
}

func NewBedrock(name, region, accessKeyID, secretAccessKey, sessionToken string, models []string) *Bedrock {
	if strings.TrimSpace(name) == "" {
		name = "bedrock"
	}
	if strings.TrimSpace(region) == "" {
		region = "us-east-1"
	}
	return &Bedrock{
		name:            strings.ToLower(strings.TrimSpace(name)),
		region:          region,
		accessKeyID:     accessKeyID,
		secretAccessKey: secretAccessKey,
		sessionToken:    sessionToken,
		models:          append([]string(nil), models...),
		client:          &http.Client{Timeout: 120 * time.Second},
	}
}

func (b *Bedrock) WithGuardrail(id, version string) *Bedrock {
	b.guardrailID = strings.TrimSpace(id)
	b.guardrailVersion = strings.TrimSpace(version)
	return b
}

func (b *Bedrock) Name() string { return b.name }

func (b *Bedrock) Models() []ModelInfo {
	models := b.models
	if len(models) == 0 {
		models = []string{
			"anthropic.claude-3-5-sonnet-20241022-v2:0",
			"anthropic.claude-3-5-haiku-20241022-v1:0",
			"meta.llama3-1-70b-instruct-v1:0",
		}
	}
	out := make([]ModelInfo, len(models))
	for i, model := range models {
		out[i] = ModelInfo{ID: model, Object: "model", OwnedBy: "aws-bedrock", Provider: b.name}
	}
	return out
}

type bedrockConverseRequest struct {
	Messages        []bedrockMessage          `json:"messages"`
	System          []bedrockContentBlock     `json:"system,omitempty"`
	InferenceConfig *bedrockInferenceConfig   `json:"inferenceConfig,omitempty"`
	ToolConfig      *bedrockToolConfig        `json:"toolConfig,omitempty"`
	GuardrailConfig *bedrockGuardrailConfig   `json:"guardrailConfig,omitempty"`
	AdditionalModelRequestFields map[string]any `json:"additionalModelRequestFields,omitempty"`
}

type bedrockMessage struct {
	Role    string                `json:"role"`
	Content []bedrockContentBlock `json:"content"`
}

type bedrockContentBlock struct {
	Text       string               `json:"text,omitempty"`
	Image      *bedrockImageBlock   `json:"image,omitempty"`
	ToolUse    *bedrockToolUse      `json:"toolUse,omitempty"`
	ToolResult *bedrockToolResult   `json:"toolResult,omitempty"`
}

type bedrockImageBlock struct {
	Format string             `json:"format"`
	Source bedrockImageSource `json:"source"`
}

type bedrockImageSource struct {
	Bytes string `json:"bytes"`
}

type bedrockToolUse struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
}

type bedrockToolResult struct {
	ToolUseID string                `json:"toolUseId"`
	Content   []bedrockContentBlock `json:"content"`
	Status    string                `json:"status,omitempty"`
}

type bedrockInferenceConfig struct {
	MaxTokens     *int     `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

type bedrockToolConfig struct {
	Tools      []bedrockTool       `json:"tools,omitempty"`
	ToolChoice *bedrockToolChoice  `json:"toolChoice,omitempty"`
}

type bedrockTool struct {
	ToolSpec bedrockToolSpec `json:"toolSpec"`
}

type bedrockToolSpec struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	InputSchema bedrockInputSchema `json:"inputSchema"`
}

type bedrockInputSchema struct {
	JSON any `json:"json"`
}

type bedrockToolChoice struct {
	Auto *struct{}          `json:"auto,omitempty"`
	Any  *struct{}          `json:"any,omitempty"`
	Tool *bedrockNamedTool  `json:"tool,omitempty"`
}

type bedrockNamedTool struct {
	Name string `json:"name"`
}

type bedrockGuardrailConfig struct {
	GuardrailIdentifier string `json:"guardrailIdentifier"`
	GuardrailVersion    string `json:"guardrailVersion"`
	Trace               string `json:"trace,omitempty"`
}

type bedrockConverseResponse struct {
	Output struct {
		Message bedrockMessage `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
	Metrics struct {
		LatencyMs int `json:"latencyMs"`
	} `json:"metrics"`
}

func (b *Bedrock) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, ProviderMeta, error) {
	start := time.Now()
	meta := ProviderMeta{Provider: b.name, ProviderType: "bedrock", Model: req.Model, Timestamp: start}
	bedrockReq := b.toConverseRequest(req)
	body, err := json.Marshal(bedrockReq)
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	endpoint := fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s/converse", b.region, url.PathEscape(req.Model))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := b.sign(httpReq, body, time.Now().UTC()); err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	resp, err := b.client.Do(httpReq)
	if err != nil {
		meta.Error = err.Error()
		meta.Latency = time.Since(start)
		return ChatResponse{}, meta, fmt.Errorf("bedrock converse request: %w", err)
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
		return ChatResponse{}, meta, fmt.Errorf("bedrock converse error (status %d): %s", resp.StatusCode, string(payload))
	}
	var bedrockResp bedrockConverseResponse
	if err := json.Unmarshal(payload, &bedrockResp); err != nil {
		meta.Error = err.Error()
		return ChatResponse{}, meta, err
	}
	content, toolCalls := fromBedrockContent(bedrockResp.Output.Message.Content)
	finishReason := mapBedrockStopReason(bedrockResp.StopReason)
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	total := bedrockResp.Usage.TotalTokens
	if total == 0 {
		total = bedrockResp.Usage.InputTokens + bedrockResp.Usage.OutputTokens
	}
	usage := Usage{PromptTokens: bedrockResp.Usage.InputTokens, CompletionTokens: bedrockResp.Usage.OutputTokens, TotalTokens: total}
	meta.Cost = bedrockPricing(req.Model).Cost(usage.PromptTokens, usage.CompletionTokens)
	return ChatResponse{
		ID:      fmt.Sprintf("bedrock-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: content, ToolCalls: toolCalls},
			FinishReason: finishReason,
		}},
		Usage: usage,
	}, meta, nil
}

func (b *Bedrock) toConverseRequest(req ChatRequest) bedrockConverseRequest {
	out := bedrockConverseRequest{
		InferenceConfig: &bedrockInferenceConfig{
			MaxTokens:     req.MaxTokens,
			Temperature:   req.Temperature,
			TopP:          req.TopP,
			StopSequences: append([]string(nil), req.Stop...),
		},
	}
	if out.InferenceConfig.MaxTokens == nil && out.InferenceConfig.Temperature == nil && out.InferenceConfig.TopP == nil && len(out.InferenceConfig.StopSequences) == 0 {
		out.InferenceConfig = nil
	}
	if b.guardrailID != "" && b.guardrailVersion != "" {
		out.GuardrailConfig = &bedrockGuardrailConfig{
			GuardrailIdentifier: b.guardrailID,
			GuardrailVersion:    b.guardrailVersion,
			Trace:               "enabled",
		}
	}
	if len(req.Tools) > 0 {
		out.ToolConfig = &bedrockToolConfig{Tools: make([]bedrockTool, 0, len(req.Tools))}
		for _, tool := range req.Tools {
			if tool.Type != "" && tool.Type != "function" {
				continue
			}
			schema := tool.Function.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			out.ToolConfig.Tools = append(out.ToolConfig.Tools, bedrockTool{ToolSpec: bedrockToolSpec{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				InputSchema: bedrockInputSchema{JSON: schema},
			}})
		}
		out.ToolConfig.ToolChoice = mapBedrockToolChoice(req.ToolChoice)
	}
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			if text := msg.Text(); text != "" {
				out.System = append(out.System, bedrockContentBlock{Text: text})
			}
			continue
		}
		role := msg.Role
		if role == "tool" {
			role = "user"
		}
		if role != "user" && role != "assistant" {
			role = "user"
		}
		content := toBedrockContent(msg)
		if len(content) == 0 {
			content = []bedrockContentBlock{{Text: msg.Text()}}
		}
		out.Messages = append(out.Messages, bedrockMessage{Role: role, Content: content})
	}
	return out
}

func toBedrockContent(msg Message) []bedrockContentBlock {
	if msg.Role == "tool" {
		return []bedrockContentBlock{{ToolResult: &bedrockToolResult{
			ToolUseID: msg.ToolCallID,
			Content:   []bedrockContentBlock{{Text: msg.Text()}},
		}}}
	}
	var blocks []bedrockContentBlock
	if len(msg.ContentParts) > 0 {
		for _, part := range msg.ContentParts {
			switch part.Type {
			case "text", "input_text":
				if part.Text != "" {
					blocks = append(blocks, bedrockContentBlock{Text: part.Text})
				}
			case "image_url", "input_image":
				if part.ImageURL == nil {
					continue
				}
				if decoded, ok := parseDataURL(part.ImageURL.URL); ok && strings.HasPrefix(decoded.MediaType, "image/") {
					blocks = append(blocks, bedrockContentBlock{Image: &bedrockImageBlock{
						Format: strings.TrimPrefix(decoded.MediaType, "image/"),
						Source: bedrockImageSource{Bytes: decoded.Data},
					}})
				} else {
					blocks = append(blocks, bedrockContentBlock{Text: "Image URL: " + part.ImageURL.URL})
				}
			}
		}
	}
	if msg.Text() != "" && len(blocks) == 0 {
		blocks = append(blocks, bedrockContentBlock{Text: msg.Text()})
	}
	for _, call := range msg.ToolCalls {
		var input map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
			input = map[string]any{}
		}
		blocks = append(blocks, bedrockContentBlock{ToolUse: &bedrockToolUse{
			ToolUseID: call.ID,
			Name:      call.Function.Name,
			Input:     input,
		}})
	}
	return blocks
}

func fromBedrockContent(blocks []bedrockContentBlock) (string, []ToolCall) {
	var text strings.Builder
	var toolCalls []ToolCall
	for index, block := range blocks {
		if block.Text != "" {
			text.WriteString(block.Text)
		}
		if block.ToolUse != nil {
			args, _ := json.Marshal(block.ToolUse.Input)
			i := index
			toolCalls = append(toolCalls, ToolCall{
				Index: &i,
				ID:    block.ToolUse.ToolUseID,
				Type:  "function",
				Function: FunctionCall{
					Name:      block.ToolUse.Name,
					Arguments: string(args),
				},
			})
		}
	}
	return text.String(), toolCalls
}

func mapBedrockToolChoice(choice any) *bedrockToolChoice {
	if choice == nil {
		return nil
	}
	switch value := choice.(type) {
	case string:
		switch value {
		case "required":
			return &bedrockToolChoice{Any: &struct{}{}}
		case "none":
			return nil
		default:
			return &bedrockToolChoice{Auto: &struct{}{}}
		}
	case map[string]any:
		if function, ok := value["function"].(map[string]any); ok {
			if name, ok := function["name"].(string); ok && name != "" {
				return &bedrockToolChoice{Tool: &bedrockNamedTool{Name: name}}
			}
		}
	}
	return &bedrockToolChoice{Auto: &struct{}{}}
}

func mapBedrockStopReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return strings.ToLower(reason)
	}
}

func bedrockPricing(model string) ModelPricing {
	switch {
	case strings.Contains(model, "claude-3-5-haiku"):
		return ModelPricing{InputPerMillion: 0.80, OutputPerMillion: 4.00}
	case strings.Contains(model, "claude-3-5-sonnet"):
		return ModelPricing{InputPerMillion: 3.00, OutputPerMillion: 15.00}
	case strings.Contains(model, "claude-3-opus"):
		return ModelPricing{InputPerMillion: 15.00, OutputPerMillion: 75.00}
	default:
		return ModelPricing{}
	}
}

func (b *Bedrock) sign(req *http.Request, payload []byte, now time.Time) error {
	if b.accessKeyID == "" || b.secretAccessKey == "" {
		return fmt.Errorf("bedrock AWS credentials are required")
	}
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	if b.sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", b.sessionToken)
	}
	payloadHash := sha256Hex(payload)
	canonicalHeaders, signedHeaders := canonicalAWSHeaders(req)
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	credentialScope := strings.Join([]string{dateStamp, b.region, "bedrock", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signingKey := awsSigningKey(b.secretAccessKey, dateStamp, b.region, "bedrock")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		b.accessKeyID,
		credentialScope,
		signedHeaders,
		signature,
	))
	return nil
}

func canonicalAWSHeaders(req *http.Request) (string, string) {
	headers := map[string]string{"host": req.URL.Host}
	for key, values := range req.Header {
		lower := strings.ToLower(key)
		if lower == "authorization" {
			continue
		}
		headers[lower] = strings.Join(values, ",")
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var canonical strings.Builder
	for _, key := range keys {
		canonical.WriteString(key)
		canonical.WriteByte(':')
		canonical.WriteString(strings.Join(strings.Fields(headers[key]), " "))
		canonical.WriteByte('\n')
	}
	return canonical.String(), strings.Join(keys, ";")
}

func awsSigningKey(secret, dateStamp, region, service string) []byte {
	dateKey := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	dateRegionKey := hmacSHA256(dateKey, region)
	dateRegionServiceKey := hmacSHA256(dateRegionKey, service)
	return hmacSHA256(dateRegionServiceKey, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
