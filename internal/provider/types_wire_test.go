package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStopSequencesAcceptsStringOrArray(t *testing.T) {
	cases := map[string][]string{
		`{"model":"m","stop":"END"}`:     {"END"},
		`{"model":"m","stop":["a","b"]}`: {"a", "b"},
		`{"model":"m","stop":null}`:      nil,
		`{"model":"m"}`:                  nil,
		`{"model":"m","stop":""}`:        nil,
	}
	for body, want := range cases {
		var req ChatRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if strings.Join(req.Stop, "|") != strings.Join(want, "|") || len(req.Stop) != len(want) {
			t.Fatalf("%s: stop = %#v, want %#v", body, req.Stop, want)
		}
	}
	var req ChatRequest
	if err := json.Unmarshal([]byte(`{"model":"m","stop":7}`), &req); err == nil {
		t.Fatal("numeric stop must be rejected")
	}
	out, _ := json.Marshal(ChatRequest{Model: "m", Stop: StopSequences{"x"}})
	if !strings.Contains(string(out), `"stop":["x"]`) {
		t.Fatalf("stop not encoded as array: %s", out)
	}
}

func TestChunkDeltaOmitsEmptyRoleAndContent(t *testing.T) {
	idx := 0
	chunk := ChatResponseChunk{ID: "c", Object: "chat.completion.chunk", Model: "m", Choices: []ChunkChoice{
		{Delta: Message{ToolCalls: []ToolCall{{Index: &idx, ID: "call_1", Type: "function", Function: FunctionCall{Name: "f"}}}}},
	}}
	out, err := json.Marshal(chunk)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"role"`) || strings.Contains(string(out), `"content"`) {
		t.Fatalf("tool-call delta leaks empty role/content: %s", out)
	}
	out, _ = json.Marshal(ChatResponseChunk{Choices: []ChunkChoice{{Delta: Message{Role: "assistant", Content: "hi"}}}})
	if !strings.Contains(string(out), `"delta":{"role":"assistant","content":"hi"}`) {
		t.Fatalf("first delta = %s", out)
	}
	out, _ = json.Marshal(ChatResponseChunk{Choices: []ChunkChoice{{FinishReason: "stop"}}})
	if !strings.Contains(string(out), `"delta":{}`) {
		t.Fatalf("finish delta = %s", out)
	}
	out, _ = json.Marshal(ChatResponseChunk{Usage: &Usage{TotalTokens: 3}})
	if !strings.Contains(string(out), `"choices":[]`) {
		t.Fatalf("usage-only chunk must have empty choices array: %s", out)
	}
}

func TestRequestMessageStillWritesContent(t *testing.T) {
	out, _ := json.Marshal(Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "x"}}, ReasoningContent: "think"})
	if !strings.Contains(string(out), `"content":""`) {
		t.Fatalf("request message must keep content: %s", out)
	}
	if strings.Contains(string(out), "reasoning_content") {
		t.Fatalf("reasoning_content must not be sent upstream: %s", out)
	}
}

func TestResponseFieldsPassThrough(t *testing.T) {
	upstream := `{"id":"x","object":"chat.completion","created":1,"model":"m","system_fingerprint":"fp_1","service_tier":"default",
"choices":[{"index":0,"message":{"role":"assistant","content":"","refusal":"no","reasoning_content":"because"},"logprobs":{"content":[]},"finish_reason":"stop"}],
"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":2}}}`
	var resp ChatResponse
	if err := json.Unmarshal([]byte(upstream), &resp); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"system_fingerprint":"fp_1"`, `"service_tier":"default"`, `"refusal":"no"`,
		`"reasoning_content":"because"`, `"logprobs":{"content":[]}`, `"cached_tokens":4`, `"reasoning_tokens":2`, `"content":""`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}

	var chunk ChatResponseChunk
	if err := json.Unmarshal([]byte(`{"id":"c","choices":[{"index":0,"delta":{"reasoning_content":"r"},"logprobs":null}]}`), &chunk); err != nil {
		t.Fatal(err)
	}
	out, _ = json.Marshal(chunk)
	if !strings.Contains(string(out), `"delta":{"reasoning_content":"r"}`) || strings.Contains(string(out), "logprobs") {
		t.Fatalf("chunk = %s", out)
	}
}
