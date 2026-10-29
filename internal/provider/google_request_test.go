package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiRequestReplaysToolCallsAndResults(t *testing.T) {
	gemReq, err := toGeminiRequest(ChatRequest{Model: "gemini-2.5-flash", Stop: StopSequences{"END"}, Messages: []Message{
		{Role: "system", Content: "be brief"},
		{Role: "developer", Content: "use metric units"},
		{Role: "user", Content: "weather in Paris and Rome?"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}},
			{ID: "call_2", Type: "function", Function: FunctionCall{Name: "weather", Arguments: `{"city":"Rome"}`}},
		}},
		{Role: "tool", ToolCallID: "call_1", Content: `{"temp":20}`},
		{Role: "tool", ToolCallID: "call_2", Content: `{"temp":25}`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if gemReq.SystemInstruction == nil || len(gemReq.SystemInstruction.Parts) != 2 ||
		gemReq.SystemInstruction.Parts[0].Text != "be brief" || gemReq.SystemInstruction.Parts[1].Text != "use metric units" {
		t.Fatalf("systemInstruction = %+v", gemReq.SystemInstruction)
	}
	if got := gemReq.GenerationConfig.StopSequences; len(got) != 1 || got[0] != "END" {
		t.Fatalf("stopSequences = %v", got)
	}
	if len(gemReq.Contents) != 3 {
		raw, _ := json.Marshal(gemReq.Contents)
		t.Fatalf("contents = %s, want user, model(functionCall x2), user(functionResponse x2)", raw)
	}
	model := gemReq.Contents[1]
	if model.Role != "model" || len(model.Parts) != 2 || model.Parts[0].FunctionCall == nil ||
		model.Parts[0].FunctionCall.Name != "weather" || model.Parts[1].FunctionCall.Args["city"] != "Rome" {
		raw, _ := json.Marshal(model)
		t.Fatalf("model turn = %s", raw)
	}
	results := gemReq.Contents[2]
	if results.Role != "user" || len(results.Parts) != 2 || results.Parts[1].FunctionResponse == nil ||
		results.Parts[1].FunctionResponse.Name != "weather" {
		raw, _ := json.Marshal(results)
		t.Fatalf("tool results turn = %s", raw)
	}
}

func TestGeminiSendsKeyInHeaderAndEscapesModel(t *testing.T) {
	var gotPath, gotRawQuery, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRawQuery, gotKey = r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("x-goog-api-key")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"thoughtsTokenCount":3,"totalTokenCount":6}}`)
	}))
	defer server.Close()
	g := NewGoogle("secret-key")
	g.baseURL = server.URL
	resp, _, err := g.ChatCompletion(context.Background(), ChatRequest{Model: "gemini-2.5-flash/../../evil?x=1", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "secret-key" || strings.Contains(gotRawQuery, "secret-key") {
		t.Fatalf("key header=%q query=%q", gotKey, gotRawQuery)
	}
	if !strings.HasPrefix(gotPath, "/models/gemini-2.5-flash%2F..%2F..%2Fevil%3Fx=1:generateContent") {
		t.Fatalf("path = %q", gotPath)
	}
	if resp.Usage.CompletionTokens != 4 || resp.Usage.TotalTokens != 6 ||
		resp.Usage.CompletionTokensDetails == nil || resp.Usage.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("usage = %+v (thinking tokens must count as completion tokens)", resp.Usage)
	}
}
