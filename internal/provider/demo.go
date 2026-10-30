package provider

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Demo is an explicitly selected simulated provider. It makes no network
// requests and must never be presented as a real model or quality benchmark.
type Demo struct{}

func NewDemo() *Demo         { return &Demo{} }
func (d *Demo) Name() string { return "demo" }
func (d *Demo) Models() []ModelInfo {
	return []ModelInfo{{ID: "demo-model", Object: "model", OwnedBy: "demo", Provider: "demo"}}
}
func (d *Demo) ChatCompletion(ctx context.Context, request ChatRequest) (ChatResponse, ProviderMeta, error) {
	if err := ctx.Err(); err != nil {
		return ChatResponse{}, ProviderMeta{}, err
	}
	text := "Demo response: your app is connected to OmniSwitch. This is a simulated reply, not a real AI model. Connect a provider with omniswitch start --setup when you are ready."
	if request.ResponseFormat != nil && request.ResponseFormat.Type != "text" {
		return ChatResponse{}, ProviderMeta{}, NewUnsupportedParameterError("demo", "response_format")
	}
	if len(request.Tools) > 0 && request.ToolChoice != "none" {
		return ChatResponse{}, ProviderMeta{}, NewUnsupportedParameterError("demo", "tools")
	}
	// Include a bounded text echo so changing the prompt is visible, without
	// pretending to generate a model answer.
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == "user" {
			prompt := strings.TrimSpace(request.Messages[i].Text())
			runes := []rune(prompt)
			if len(runes) > 160 {
				prompt = string(runes[:160]) + "…"
			}
			text += "\n\nYou sent: " + prompt
			break
		}
	}
	now := time.Now()
	response := ChatResponse{ID: fmt.Sprintf("chat_demo_%d", now.UnixNano()), Object: "chat.completion", Created: now.Unix(), Model: request.Model, Choices: []Choice{{Index: 0, Message: Message{Role: "assistant", Content: text}, FinishReason: "stop"}}}
	return response, ProviderMeta{Provider: d.Name(), ProviderType: "demo", Model: request.Model, Timestamp: now, Cost: 0}, nil
}
func (d *Demo) ChatCompletionStream(ctx context.Context, request ChatRequest) (<-chan ChatResponseChunk, ProviderMeta, error) {
	response, meta, err := d.ChatCompletion(ctx, request)
	if err != nil {
		return nil, meta, err
	}
	return StreamFromResponse(ctx, response), meta, nil
}
