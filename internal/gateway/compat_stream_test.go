package gateway

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

func intPtr(v int) *int { return &v }

func toolDelta(index int, id, name, args string) provider.ChatResponseChunk {
	return provider.ChatResponseChunk{ID: "chat_1", Model: "m", Choices: []provider.ChunkChoice{{Delta: provider.Message{
		ToolCalls: []provider.ToolCall{{Index: intPtr(index), ID: id, Type: "function", Function: provider.FunctionCall{Name: name, Arguments: args}}},
	}}}}
}

func textDelta(text string) provider.ChatResponseChunk {
	return provider.ChatResponseChunk{ID: "chat_1", Model: "m", Choices: []provider.ChunkChoice{{Delta: provider.Message{Content: text}}}}
}

func finishDelta(reason string) provider.ChatResponseChunk {
	return provider.ChatResponseChunk{ID: "chat_1", Model: "m", Choices: []provider.ChunkChoice{{FinishReason: reason}}}
}

// translate runs chunks plus [DONE] through a translator and returns the
// events with their data decoded back into generic JSON.
func translate(t *testing.T, tr sseTranslator, chunks ...provider.ChatResponseChunk) ([]string, []map[string]any) {
	t.Helper()
	var payloads []string
	for _, c := range chunks {
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, string(raw))
	}
	payloads = append(payloads, "[DONE]")
	var names []string
	var data []map[string]any
	for _, p := range payloads {
		for _, ev := range tr.Translate(p) {
			if ev.Done {
				names = append(names, "[DONE]")
				data = append(data, nil)
				continue
			}
			raw, _ := json.Marshal(ev.Data)
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			names = append(names, ev.Event)
			data = append(data, m)
		}
	}
	return names, data
}

func eventsNamed(names []string, data []map[string]any, name string) []map[string]any {
	var out []map[string]any
	for i, n := range names {
		if n == name {
			out = append(out, data[i])
		}
	}
	return out
}

func TestResponsesStreamTranslatorToolCalls(t *testing.T) {
	cases := []struct {
		name       string
		chunks     []provider.ChatResponseChunk
		wantEvents []string
		// wantOutput lists "type:name-or-text:arguments" per output item;
		wantOutput []string
	}{
		{
			name:       "text only",
			chunks:     []provider.ChatResponseChunk{textDelta("hello "), textDelta("world"), finishDelta("stop")},
			wantEvents: []string{"response.created", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done", "response.completed", "[DONE]"},
			wantOutput: []string{"message:hello world:"},
		},
		{
			name: "tool call only",
			chunks: []provider.ChatResponseChunk{
				toolDelta(0, "call_1", "get_weather", ""), toolDelta(0, "", "", `{"city":`), toolDelta(0, "", "", `"Paris"}`), finishDelta("tool_calls"),
			},
			wantEvents: []string{
				"response.created", "response.output_item.added",
				"response.function_call_arguments.delta", "response.function_call_arguments.delta",
				"response.function_call_arguments.done", "response.output_item.done", "response.completed", "[DONE]",
			},
			wantOutput: []string{`function_call:get_weather:{"city":"Paris"}`},
		},
		{
			name: "text then tool call",
			chunks: []provider.ChatResponseChunk{
				textDelta("Let me check."), toolDelta(0, "call_1", "get_weather", `{"city":"Paris"}`), finishDelta("tool_calls"),
			},
			wantEvents: []string{
				"response.created", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_item.added",
				"response.function_call_arguments.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done", "response.function_call_arguments.done",
				"response.output_item.done", "response.completed", "[DONE]",
			},
			wantOutput: []string{"message:Let me check.:", `function_call:get_weather:{"city":"Paris"}`},
		},
		{
			name: "two parallel tool calls with interleaved arguments",
			chunks: []provider.ChatResponseChunk{
				toolDelta(0, "call_a", "get_weather", ""), toolDelta(1, "call_b", "get_time", ""),
				toolDelta(0, "", "", `{"city":`), toolDelta(1, "", "", `{"tz":`),
				toolDelta(1, "", "", `"CET"}`), toolDelta(0, "", "", `"Oslo"}`), finishDelta("tool_calls"),
			},
			wantEvents: []string{
				"response.created", "response.output_item.added", "response.output_item.added",
				"response.function_call_arguments.delta", "response.function_call_arguments.delta",
				"response.function_call_arguments.delta", "response.function_call_arguments.delta",
				"response.function_call_arguments.done", "response.output_item.done",
				"response.function_call_arguments.done", "response.output_item.done",
				"response.completed", "[DONE]",
			},
			wantOutput: []string{`function_call:get_weather:{"city":"Oslo"}`, `function_call:get_time:{"tz":"CET"}`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names, data := translate(t, newResponsesStreamTranslator(), tc.chunks...)
			if !reflect.DeepEqual(names, tc.wantEvents) {
				t.Fatalf("events = %v\nwant     %v", names, tc.wantEvents)
			}
			completed := eventsNamed(names, data, "response.completed")[0]["response"].(map[string]any)
			output, hasOutput := completed["output"].([]any)
			if tc.wantOutput == nil {
				if hasOutput {
					t.Fatalf("text-only completed response changed: %v", completed)
				}
				return
			}
			var got []string
			for _, raw := range output {
				item := raw.(map[string]any)
				if item["status"] != "completed" {
					t.Errorf("item status = %v", item["status"])
				}
				switch item["type"] {
				case "message":
					text := item["content"].([]any)[0].(map[string]any)["text"]
					got = append(got, "message:"+text.(string)+":")
				case "function_call":
					if item["id"] != "fc_"+item["call_id"].(string) {
						t.Errorf("item id %v does not match call_id %v", item["id"], item["call_id"])
					}
					got = append(got, "function_call:"+item["name"].(string)+":"+item["arguments"].(string))
				}
			}
			if !reflect.DeepEqual(got, tc.wantOutput) {
				t.Fatalf("output = %v, want %v", got, tc.wantOutput)
			}
			// Every function_call event must point at the item's output
			// index in the completed output.
			for _, name := range []string{"response.output_item.added", "response.output_item.done"} {
				for _, ev := range eventsNamed(names, data, name) {
					item := ev["item"].(map[string]any)
					idx := int(ev["output_index"].(float64))
					if output[idx].(map[string]any)["id"] != item["id"] {
						t.Errorf("%s output_index %d points at %v, want %v", name, idx, output[idx], item["id"])
					}
				}
			}
			for _, ev := range eventsNamed(names, data, "response.output_item.added") {
				item := ev["item"].(map[string]any)
				if item["status"] != "in_progress" || (item["type"] != "message" && item["type"] != "function_call") {
					t.Errorf("added item = %v", item)
				}
				if item["type"] == "function_call" && item["arguments"] != "" {
					t.Errorf("new call already has arguments: %v", item)
				}
			}
			sequence := 0
			for _, ev := range data {
				if ev != nil {
					if ev["sequence_number"] != float64(sequence) {
						t.Errorf("sequence = %v, want %d", ev["sequence_number"], sequence)
					}
					sequence++
				}
			}
			for _, ev := range eventsNamed(names, data, "response.function_call_arguments.done") {
				idx := int(ev["output_index"].(float64))
				item := output[idx].(map[string]any)
				if ev["arguments"] != item["arguments"] || ev["item_id"] != item["id"] {
					t.Errorf("arguments.done = %v, item = %v", ev, item)
				}
			}
		})
	}
}

func TestMessagesStreamTranslatorToolCalls(t *testing.T) {
	type block struct {
		Index int
		Kind  string // "text" or tool name
		Body  string // text or accumulated partial_json
	}
	cases := []struct {
		name       string
		chunks     []provider.ChatResponseChunk
		wantEvents []string
		wantBlocks []block
		wantStop   string
	}{
		{
			name:   "text only",
			chunks: []provider.ChatResponseChunk{textDelta("hello "), textDelta("world"), finishDelta("stop")},
			wantEvents: []string{
				"message_start", "content_block_start", "content_block_delta", "content_block_delta",
				"content_block_stop", "message_delta", "message_stop",
			},
			wantBlocks: []block{{0, "text", "hello world"}},
			wantStop:   "end_turn",
		},
		{
			name: "tool call only",
			chunks: []provider.ChatResponseChunk{
				toolDelta(0, "call_1", "get_weather", ""), toolDelta(0, "", "", `{"city":`), toolDelta(0, "", "", `"Paris"}`), finishDelta("tool_calls"),
			},
			wantEvents: []string{
				"message_start", "content_block_start", "content_block_delta", "content_block_delta",
				"content_block_stop", "message_delta", "message_stop",
			},
			wantBlocks: []block{{0, "get_weather", `{"city":"Paris"}`}},
			wantStop:   "tool_use",
		},
		{
			name: "text then tool call",
			chunks: []provider.ChatResponseChunk{
				textDelta("Let me check."), toolDelta(0, "call_1", "get_weather", `{"city":"Paris"}`), finishDelta("tool_calls"),
			},
			wantEvents: []string{
				"message_start", "content_block_start", "content_block_delta", "content_block_stop",
				"content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop",
			},
			wantBlocks: []block{{0, "text", "Let me check."}, {1, "get_weather", `{"city":"Paris"}`}},
			wantStop:   "tool_use",
		},
		{
			name: "two parallel tool calls with interleaved arguments",
			chunks: []provider.ChatResponseChunk{
				toolDelta(0, "call_a", "get_weather", ""), toolDelta(1, "call_b", "get_time", ""),
				toolDelta(0, "", "", `{"city":`), toolDelta(1, "", "", `{"tz":`),
				toolDelta(1, "", "", `"CET"}`), toolDelta(0, "", "", `"Oslo"}`), finishDelta("tool_calls"),
			},
			wantEvents: []string{
				"message_start", "content_block_start", "content_block_start",
				"content_block_delta", "content_block_delta", "content_block_delta", "content_block_delta",
				"content_block_stop", "content_block_stop", "message_delta", "message_stop",
			},
			wantBlocks: []block{{0, "get_weather", `{"city":"Oslo"}`}, {1, "get_time", `{"tz":"CET"}`}},
			wantStop:   "tool_use",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names, data := translate(t, newMessagesStreamTranslator(), tc.chunks...)
			if !reflect.DeepEqual(names, tc.wantEvents) {
				t.Fatalf("events = %v\nwant     %v", names, tc.wantEvents)
			}
			var blocks []block
			byIndex := map[int]int{}
			stopped := map[int]bool{}
			for i, name := range names {
				ev := data[i]
				switch name {
				case "content_block_start":
					idx := int(ev["index"].(float64))
					cb := ev["content_block"].(map[string]any)
					kind := "text"
					if cb["type"] == "tool_use" {
						kind = cb["name"].(string)
						if cb["id"] == "" || cb["input"] == nil {
							t.Errorf("tool_use block = %v", cb)
						}
					}
					byIndex[idx] = len(blocks)
					blocks = append(blocks, block{Index: idx, Kind: kind})
				case "content_block_delta":
					idx := int(ev["index"].(float64))
					pos, ok := byIndex[idx]
					if !ok || stopped[idx] {
						t.Fatalf("delta for unopened or stopped block %d", idx)
					}
					delta := ev["delta"].(map[string]any)
					if delta["type"] == "input_json_delta" {
						blocks[pos].Body += delta["partial_json"].(string)
					} else {
						blocks[pos].Body += delta["text"].(string)
					}
				case "content_block_stop":
					stopped[int(ev["index"].(float64))] = true
				case "message_delta":
					if got := ev["delta"].(map[string]any)["stop_reason"]; got != tc.wantStop {
						t.Errorf("stop_reason = %v, want %s", got, tc.wantStop)
					}
				}
			}
			if !reflect.DeepEqual(blocks, tc.wantBlocks) {
				t.Fatalf("blocks = %+v, want %+v", blocks, tc.wantBlocks)
			}
			for _, b := range blocks {
				if !stopped[b.Index] {
					t.Errorf("block %d never stopped", b.Index)
				}
			}
		})
	}
}

// SF2 end to end: a streaming /v1/responses request whose provider streams a
// tool call must surface the call to the client.
func TestCompatStreamingForwardsToolCalls(t *testing.T) {
	p := scriptedProvider{name: "test", model: "test-model", chunks: []provider.ChatResponseChunk{
		toolDelta(0, "call_1", "get_weather", `{"city":"Paris"}`), finishDelta("tool_calls"),
	}}
	h, _ := newHardeningHandler(t, nil, p)
	for _, tc := range []struct {
		path, body string
		fn         http.HandlerFunc
		want       []string
	}{
		{"/v1/responses", `{"model":"test-model","stream":true,"input":"weather?","tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}]}`, h.Responses,
			[]string{"event: response.output_item.added", `"name":"get_weather"`, "event: response.function_call_arguments.done", `"type":"function_call"`, "event: response.completed"}},
		{"/v1/messages", `{"model":"test-model","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"weather?"}]}`, h.Messages,
			[]string{`"type":"tool_use"`, `"name":"get_weather"`, `"type":"input_json_delta"`, `"stop_reason":"tool_use"`, "event: message_stop"}},
	} {
		rec := doRequest(tc.fn, tc.path, tc.body, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body = %s", tc.path, rec.Code, rec.Body.String())
		}
		for _, want := range tc.want {
			if !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("%s body missing %q: %s", tc.path, want, rec.Body.String())
			}
		}
	}
}
