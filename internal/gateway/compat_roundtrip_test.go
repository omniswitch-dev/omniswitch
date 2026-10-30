package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

// Exercise the public compatibility handlers through the real HTTP provider
// adapter, including serializing a returned call back into the next request.
func TestCompatibilityToolRoundTrip(t *testing.T) {
	for _, api := range []string{"responses", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", api, stream), func(t *testing.T) {
				requests := make(chan provider.ChatRequest, 2)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req provider.ChatRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						http.Error(w, err.Error(), 400)
						return
					}
					requests <- req
					if req.Stream {
						w.Header().Set("Content-Type", "text/event-stream")
						for _, chunk := range []provider.ChatResponseChunk{textDelta("Sunny."), finishDelta("stop")} {
							raw, _ := json.Marshal(chunk)
							fmt.Fprintf(w, "data: %s\n\n", raw)
						}
						fmt.Fprint(w, "data: [DONE]\n\n")
						return
					}
					message := provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call_weather", Type: "function", Function: provider.FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}}}}
					finish := "tool_calls"
					if len(req.Messages) > 1 {
						message = provider.Message{Role: "assistant", Content: "Sunny."}
						finish = "stop"
					}
					_ = json.NewEncoder(w).Encode(provider.ChatResponse{ID: "chat_roundtrip", Model: "test-model", Choices: []provider.Choice{{Message: message, FinishReason: finish}}})
				}))
				defer upstream.Close()
				registry := provider.NewRegistry()
				registry.Register(provider.NewCustom("test", upstream.URL, "test-key", provider.WithCustomModels([]string{"test-model"})))
				h := New(registry, router.New(registry), newGatewayTestStore(t), nil)
				var body map[string]any
				var fn http.HandlerFunc
				if api == "responses" {
					body = map[string]any{"model": "test-model", "input": "Weather?", "tools": []any{map[string]any{"type": "function", "name": "weather", "parameters": map[string]any{"type": "object"}, "strict": true}}, "store": false}
					fn = h.Responses
				} else {
					body = map[string]any{"model": "test-model", "max_tokens": 100, "messages": []any{map[string]any{"role": "user", "content": "Weather?"}}, "tools": []any{map[string]any{"name": "weather", "input_schema": map[string]any{"type": "object"}, "strict": true}}}
					fn = h.Messages
				}
				call := func() *httptest.ResponseRecorder {
					raw, _ := json.Marshal(body)
					rec := doRequest(fn, "/v1/"+api, string(raw), nil)
					if rec.Code != 200 {
						t.Fatalf("status/body = %d/%s", rec.Code, rec.Body)
					}
					return rec
				}
				first := call()
				var response map[string]json.RawMessage
				if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if req := <-requests; len(req.Tools) != 1 || req.Tools[0].Function.Name != "weather" || req.Tools[0].Function.Strict == nil || !*req.Tools[0].Function.Strict {
					t.Fatalf("lost tool definition: %+v", req.Tools)
				}
				if api == "responses" {
					var output []any
					_ = json.Unmarshal(response["output"], &output)
					body["input"] = append(append([]any{map[string]any{"role": "user", "content": "Weather?"}}, output...), map[string]any{"type": "function_call_output", "call_id": "call_weather", "output": "Sunny"})
				} else {
					if string(response["stop_reason"]) != `"tool_use"` {
						t.Fatalf("tool turn: %s", first.Body)
					}
					body["messages"] = []any{map[string]any{"role": "user", "content": "Weather?"}, map[string]any{"role": "assistant", "content": response["content"]}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call_weather", "content": "Sunny"}}}}
				}
				body["stream"] = stream
				second := call()
				if !strings.Contains(second.Body.String(), "Sunny.") {
					t.Fatalf("missing final answer: %s", second.Body)
				}
				req := <-requests
				if len(req.Messages) != 3 || len(req.Messages[1].ToolCalls) != 1 || req.Messages[1].ToolCalls[0].ID != "call_weather" || req.Messages[2].Role != "tool" || req.Messages[2].ToolCallID != "call_weather" || req.Messages[2].Content != "Sunny" {
					t.Fatalf("lost tool history: %+v", req.Messages)
				}
			})
		}
	}
}

func TestResponsesControlsAndParallelHistory(t *testing.T) {
	var request responsesRequest
	raw := `{"model":"m","store":false,"parallel_tool_calls":false,"text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object"},"strict":true}},"input":[{"role":"user","content":"weather?"},{"type":"function_call","call_id":"a","name":"weather","arguments":"{}"},{"type":"function_call","call_id":"b","name":"time","arguments":"{}"},{"type":"function_call_output","call_id":"b","output":"noon"},{"type":"function_call_output","call_id":"a","output":"sunny"}]}`
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	chat, err := request.toChatRequest()
	if err != nil || len(chat.Messages) != 4 || len(chat.Messages[1].ToolCalls) != 2 || chat.Messages[2].ToolCallID != "b" || chat.Messages[3].ToolCallID != "a" {
		t.Fatalf("parallel history: %+v / %v", chat, err)
	}
	if chat.ParallelToolCalls == nil || *chat.ParallelToolCalls || chat.ResponseFormat == nil || chat.ResponseFormat.JSONSchema.Name != "answer" || !*chat.ResponseFormat.JSONSchema.Strict {
		t.Fatalf("lost controls: %+v", chat)
	}
}

func TestMessagesVisionSystemAndToolChoice(t *testing.T) {
	var request anthropicMessagesRequest
	raw := `{"model":"m","max_tokens":100,"system":[{"type":"text","text":"Be concise"}],"stop_sequences":["END"],"tool_choice":{"type":"tool","name":"weather","disable_parallel_tool_use":true},"messages":[{"role":"user","content":[{"type":"text","text":"What is this?"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]},{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"weather","input":{}},{"type":"tool_use","id":"b","name":"time","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":[{"type":"text","text":"noon"}]},{"type":"tool_result","tool_use_id":"a","content":"offline","is_error":true},{"type":"text","text":"Try again"}]}]}`
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	chat, err := request.toChatRequest()
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 6 || chat.Messages[0].Content != "Be concise" || chat.Messages[1].ContentParts[1].ImageURL.URL != "data:image/png;base64,aGVsbG8=" || chat.Messages[3].ToolCallID != "b" || chat.Messages[4].Content != "Tool error: offline" || chat.Messages[5].Content != "Try again" {
		t.Fatalf("lost blocks: %+v", chat.Messages)
	}
	if len(chat.Stop) != 1 || chat.Stop[0] != "END" || chat.ParallelToolCalls == nil || *chat.ParallelToolCalls || chat.ToolChoice.(map[string]any)["function"].(map[string]any)["name"] != "weather" {
		t.Fatalf("lost controls: %+v", chat)
	}
}

func TestCompatibilityRejectsUnsupportedOrMalformedInputBeforeDispatch(t *testing.T) {
	registry := provider.NewRegistry()
	upstream := &enrichmentProvider{}
	registry.Register(upstream)
	h := New(registry, router.New(registry), newGatewayTestStore(t), nil)
	for _, tc := range []struct{ api, fields string }{
		{"responses", `"previous_response_id":"resp_other","input":"hi"`},
		{"responses", `"input":null`},
		{"responses", `"store":true,"input":"hi"`},
		{"responses", `"background":true,"input":"hi"`},
		{"responses", `"conversation":"conv_1","input":"hi"`},
		{"responses", `"input":"hi","tool_choice":"arbitrary"`},
		{"responses", `"input":"hi","text":{"format":{"type":"json_schema"}}`},
		{"responses", `"input":[{"type":"function_call_output","call_id":"missing","output":"x"}]`},
		{"responses", `"input":[{"type":"function_call","call_id":"a","name":"f","arguments":"[]"}]`},
		{"responses", `"input":[{"type":"function_call","call_id":"a","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":null}]`},
		{"messages", `"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"missing","content":"x"}]}]`},
		{"messages", `"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"f","input":null}]}]`},
		{"messages", `"messages":[{"role":"user","content":[{"type":"thinking","thinking":"secret"}]}]`},
		{"messages", `"thinking":{"type":"enabled","budget_tokens":1000},"messages":[{"role":"user","content":"hi"}]`},
		{"messages", `"tool_choice":{"type":"bad"},"messages":[{"role":"user","content":"hi"}]`},
		{"messages", `"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"hi"}]`},
		{"messages", `"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"bad!"}}]}]`},
	} {
		t.Run(tc.api+"/"+tc.fields, func(t *testing.T) {
			fn := h.Responses
			if tc.api == "messages" {
				fn = h.Messages
			}
			rec := doRequest(fn, "/v1/"+tc.api, `{"model":"support",`+tc.fields+`}`, nil)
			if rec.Code != 400 || upstream.calls != 0 {
				t.Fatalf("status/calls/body = %d/%d/%s", rec.Code, upstream.calls, rec.Body)
			}
		})
	}
}

func TestToolArgumentsRetainLargeIntegerPrecision(t *testing.T) {
	arguments := `{"record_id":9007199254740993,"nested":{"value":18446744073709551615}}`
	input, err := toolArgumentsObject(arguments)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(input)
	if err != nil || !strings.Contains(string(raw), "9007199254740993") || !strings.Contains(string(raw), "18446744073709551615") {
		t.Fatalf("tool arguments lost precision: %s / %v", raw, err)
	}
}

func TestCompatibleToolResultsStillPassInputGuardrails(t *testing.T) {
	registry := provider.NewRegistry()
	upstream := &enrichmentProvider{}
	registry.Register(upstream)
	gr := guardrail.NewEngineWithConfig(guardrail.Config{Rules: []guardrail.Rule{{Name: "tool-content", Stage: "input", Pattern: "blocked-tool-output", Action: "deny"}}})
	h := New(registry, router.New(registry), newGatewayTestStore(t), gr)
	for _, tc := range []struct {
		fn   http.HandlerFunc
		body string
	}{
		{h.Responses, `{"model":"support","input":[{"type":"function_call","call_id":"a","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"blocked-tool-output"}]}`},
		{h.Messages, `{"model":"support","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"blocked-tool-output"}]}]}`},
	} {
		rec := doRequest(tc.fn, "/v1/test", tc.body, nil)
		if rec.Code != 403 || upstream.calls != 0 {
			t.Fatalf("guardrails bypassed: %d/%d/%s", rec.Code, upstream.calls, rec.Body)
		}
	}
}
