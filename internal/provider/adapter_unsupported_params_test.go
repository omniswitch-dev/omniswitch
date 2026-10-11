package provider

import "testing"

func TestToAnthropicRequestTranslatesResponseFormatAndMaxCompletionTokens(t *testing.T) {
	req := ChatRequest{
		Model:               "claude-sonnet-4-5",
		Messages:            []Message{{Role: "user", Content: "hi"}},
		MaxTokens:           intPtr(10),
		MaxCompletionTokens: intPtr(256),
		ResponseFormat: &ResponseFormat{
			Type: "json_schema",
			JSONSchema: &JSONSchemaSpec{
				Name:   "extract",
				Schema: map[string]any{"type": "object"},
			},
		},
		ParallelToolCalls: boolPtr(false),
	}
	anthReq, err := toAnthropicRequest(req, false)
	if err != nil {
		t.Fatalf("toAnthropicRequest() error = %v", err)
	}
	if anthReq.MaxTokens != 256 {
		t.Fatalf("MaxTokens = %d, want max_completion_tokens (256) to win over max_tokens", anthReq.MaxTokens)
	}
	if anthReq.OutputConfig == nil || anthReq.OutputConfig.Format.Type != "json_schema" {
		t.Fatalf("OutputConfig = %+v, want translated json_schema format", anthReq.OutputConfig)
	}
	if anthReq.ToolChoice == nil || anthReq.ToolChoice.DisableParallelToolUse == nil || !*anthReq.ToolChoice.DisableParallelToolUse {
		t.Fatalf("ToolChoice = %+v, want disable_parallel_tool_use=true", anthReq.ToolChoice)
	}
}

func TestToAnthropicRequestRejectsUnsupportedParameters(t *testing.T) {
	cases := []struct {
		name string
		req  ChatRequest
		want string
	}{
		{"n>1", ChatRequest{Model: "claude", N: intPtr(2)}, "n"},
		{"seed", ChatRequest{Model: "claude", Seed: intPtr(1)}, "seed"},
		{"logit_bias", ChatRequest{Model: "claude", LogitBias: map[string]int{"1": 1}}, "logit_bias"},
		{"presence_penalty", ChatRequest{Model: "claude", PresencePenalty: float64Ptr(0.5)}, "presence_penalty"},
		{"frequency_penalty", ChatRequest{Model: "claude", FrequencyPenalty: float64Ptr(0.5)}, "frequency_penalty"},
		{"reasoning_effort", ChatRequest{Model: "claude", ReasoningEffort: "high"}, "reasoning_effort"},
		{"service_tier", ChatRequest{Model: "claude", ServiceTier: "priority"}, "service_tier"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := toAnthropicRequest(tc.req, false)
			var upErr *UnsupportedParameterError
			if err == nil {
				t.Fatalf("toAnthropicRequest() error = nil, want UnsupportedParameterError for %q", tc.want)
			}
			if !asUnsupportedParam(err, &upErr) {
				t.Fatalf("error = %v, want *UnsupportedParameterError", err)
			}
			if upErr.Param != tc.want {
				t.Fatalf("Param = %q, want %q", upErr.Param, tc.want)
			}
		})
	}
}

func TestToGeminiRequestTranslatesResponseFormatAndMaxCompletionTokens(t *testing.T) {
	req := ChatRequest{
		Model:               "gemini-2.5-flash",
		Messages:            []Message{{Role: "user", Content: "hi"}},
		MaxCompletionTokens: intPtr(128),
		ResponseFormat: &ResponseFormat{
			Type: "json_object",
		},
	}
	gemReq, err := toGeminiRequest(req)
	if err != nil {
		t.Fatalf("toGeminiRequest() error = %v", err)
	}
	if gemReq.GenerationConfig == nil || gemReq.GenerationConfig.MaxTokens == nil || *gemReq.GenerationConfig.MaxTokens != 128 {
		t.Fatalf("GenerationConfig = %+v, want maxOutputTokens=128", gemReq.GenerationConfig)
	}
	if gemReq.GenerationConfig.ResponseMimeType != "application/json" {
		t.Fatalf("ResponseMimeType = %q, want application/json", gemReq.GenerationConfig.ResponseMimeType)
	}
}

func TestToGeminiRequestRejectsUnsupportedParameters(t *testing.T) {
	cases := []struct {
		name string
		req  ChatRequest
		want string
	}{
		{"n>1", ChatRequest{Model: "gemini", N: intPtr(2)}, "n"},
		{"seed", ChatRequest{Model: "gemini", Seed: intPtr(1)}, "seed"},
		{"parallel_tool_calls", ChatRequest{Model: "gemini", ParallelToolCalls: boolPtr(true)}, "parallel_tool_calls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := toGeminiRequest(tc.req)
			var upErr *UnsupportedParameterError
			if err == nil || !asUnsupportedParam(err, &upErr) {
				t.Fatalf("toGeminiRequest() error = %v, want UnsupportedParameterError for %q", err, tc.want)
			}
			if upErr.Param != tc.want {
				t.Fatalf("Param = %q, want %q", upErr.Param, tc.want)
			}
		})
	}
}

func asUnsupportedParam(err error, target **UnsupportedParameterError) bool {
	upErr, ok := err.(*UnsupportedParameterError)
	if !ok {
		return false
	}
	*target = upErr
	return true
}

func TestToConverseRequestTranslatesMaxCompletionTokens(t *testing.T) {
	b := NewBedrock("bedrock", "us-east-1", "ak", "sk", "", nil)
	maxCompletion := 321
	out, err := b.toConverseRequest(ChatRequest{
		Model:               "anthropic.claude-3-5-haiku-20241022-v1:0",
		Messages:            []Message{{Role: "user", Content: "hi"}},
		MaxTokens:           intPtr(100),
		MaxCompletionTokens: &maxCompletion,
	})
	if err != nil {
		t.Fatalf("toConverseRequest() error = %v", err)
	}
	if out.InferenceConfig == nil || out.InferenceConfig.MaxTokens == nil || *out.InferenceConfig.MaxTokens != maxCompletion {
		t.Fatalf("InferenceConfig = %+v, want MaxTokens=%d (max_completion_tokens wins)", out.InferenceConfig, maxCompletion)
	}
}

func TestToConverseRequestRejectsUnsupportedParameters(t *testing.T) {
	b := NewBedrock("bedrock", "us-east-1", "ak", "sk", "", nil)
	base := ChatRequest{Model: "anthropic.claude-3-5-haiku-20241022-v1:0", Messages: []Message{{Role: "user", Content: "hi"}}}

	cases := []struct {
		name   string
		mutate func(*ChatRequest)
		want   string
	}{
		{"n", func(r *ChatRequest) { n := 2; r.N = &n }, "n"},
		{"seed", func(r *ChatRequest) { s := 7; r.Seed = &s }, "seed"},
		{"logit_bias", func(r *ChatRequest) { r.LogitBias = map[string]int{"123": 1} }, "logit_bias"},
		{"presence_penalty", func(r *ChatRequest) { p := 0.5; r.PresencePenalty = &p }, "presence_penalty"},
		{"frequency_penalty", func(r *ChatRequest) { p := 0.5; r.FrequencyPenalty = &p }, "frequency_penalty"},
		{"parallel_tool_calls", func(r *ChatRequest) { v := false; r.ParallelToolCalls = &v }, "parallel_tool_calls"},
		{"reasoning_effort", func(r *ChatRequest) { r.ReasoningEffort = "high" }, "reasoning_effort"},
		{"service_tier", func(r *ChatRequest) { r.ServiceTier = "priority" }, "service_tier"},
		{"response_format", func(r *ChatRequest) { r.ResponseFormat = &ResponseFormat{Type: "json_object"} }, "response_format"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mutate(&req)
			_, err := b.toConverseRequest(req)
			var upErr *UnsupportedParameterError
			if err == nil || !asUnsupportedParam(err, &upErr) {
				t.Fatalf("toConverseRequest() error = %v, want UnsupportedParameterError for %q", err, tc.want)
			}
			if upErr.Param != tc.want {
				t.Fatalf("UnsupportedParameterError.Param = %q, want %q", upErr.Param, tc.want)
			}
		})
	}
}
