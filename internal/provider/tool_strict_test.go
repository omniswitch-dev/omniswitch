package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStrictToolsPreservedOrRejected(t *testing.T) {
	strict := true
	req := ChatRequest{Tools: []Tool{{Type: "function", Function: ToolFunction{Name: "lookup", Parameters: map[string]any{"type": "object"}, Strict: &strict}}}}
	raw, err := json.Marshal(req)
	if err != nil || !strings.Contains(string(raw), `"strict":true`) {
		t.Fatalf("OpenAI strict dropped: %s / %v", raw, err)
	}
	anthropic, err := toAnthropicRequest(req, false)
	if err != nil || anthropic.Tools[0].Strict == nil || !*anthropic.Tools[0].Strict {
		t.Fatalf("Anthropic strict dropped: %+v / %v", anthropic, err)
	}
	if _, err := toGeminiRequest(req); err == nil || !strings.Contains(err.Error(), "strict") {
		t.Fatalf("Gemini silently ignored strict: %v", err)
	}
	if _, err := (&Bedrock{}).toConverseRequest(req); err == nil || !strings.Contains(err.Error(), "strict") {
		t.Fatalf("Bedrock silently ignored strict: %v", err)
	}
}
