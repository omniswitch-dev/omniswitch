package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnthropicRequestSamplingParams(t *testing.T) {
	temp, topP := 1.5, 0.9
	anthReq, err := toAnthropicRequest(ChatRequest{
		Model:       "claude-3-5-haiku-20241022",
		Messages:    []Message{{Role: "user", Content: "hi"}},
		Temperature: &temp,
		TopP:        &topP,
		Stop:        StopSequences{"END", "STOP"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(anthReq)
	for _, want := range []string{`"temperature":1`, `"top_p":0.9`, `"stop_sequences":["END","STOP"]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s in %s", want, raw)
		}
	}
	zero := 0.0
	anthReq, _ = toAnthropicRequest(ChatRequest{Model: "m", Temperature: &zero}, false)
	raw, _ = json.Marshal(anthReq)
	if !strings.Contains(string(raw), `"temperature":0`) {
		t.Fatalf("temperature 0 must be sent: %s", raw)
	}
}

func TestAnthropicRequestJoinsSystemAndDeveloperMessages(t *testing.T) {
	anthReq, err := toAnthropicRequest(ChatRequest{Model: "m", Messages: []Message{
		{Role: "system", Content: "be brief"},
		{Role: "developer", Content: "answer in French"},
		{Role: "user", Content: "hi"},
		{Role: "system", Content: "no emojis"},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if anthReq.System != "be brief\n\nanswer in French\n\nno emojis" {
		t.Fatalf("system = %q", anthReq.System)
	}
	if len(anthReq.Messages) != 1 || anthReq.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v (developer must not be sent as a role)", anthReq.Messages)
	}
}

func TestFinishReasonMapping(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{mapAnthropicStopReason("end_turn"), "stop"},
		{mapAnthropicStopReason("stop_sequence"), "stop"},
		{mapAnthropicStopReason("max_tokens"), "length"},
		{mapAnthropicStopReason("tool_use"), "tool_calls"},
		{mapAnthropicStopReason("refusal"), "content_filter"},
		{mapAnthropicStopReason("pause_turn"), "stop"},
		{mapBedrockStopReason("guardrail_intervened"), "content_filter"},
		{mapBedrockStopReason("content_filtered"), "content_filter"},
		{mapBedrockStopReason("tool_use"), "tool_calls"},
		{mapBedrockStopReason("max_tokens"), "length"},
		{mapBedrockStopReason("something_new"), "stop"},
		{mapGeminiFinishReason("SAFETY"), "content_filter"},
		{mapGeminiFinishReason("RECITATION"), "content_filter"},
		{mapGeminiFinishReason("PROHIBITED_CONTENT"), "content_filter"},
		{mapGeminiFinishReason("BLOCKLIST"), "content_filter"},
		{mapGeminiFinishReason("SPII"), "content_filter"},
		{mapGeminiFinishReason("MAX_TOKENS"), "length"},
		{mapGeminiFinishReason("STOP"), "stop"},
		{mapGeminiFinishReason("OTHER"), "stop"},
		{mapGeminiFinishReason(""), ""},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: got %q, want %q", i, c.got, c.want)
		}
	}
}
