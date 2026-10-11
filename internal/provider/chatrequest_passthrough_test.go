package provider

import (
	"encoding/json"
	"testing"
)

// TestChatRequestExtraRoundTrip verifies that top-level request keys not
// modeled by ChatRequest (future/beta OpenAI parameters) survive a decode +
// re-encode cycle unchanged, so OpenAI-compatible upstreams still receive
// them verbatim.
func TestChatRequestExtraRoundTrip(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hi"}],
		"prediction": {"type": "content", "content": "hi"},
		"modalities": ["text", "audio"],
		"audio": {"voice": "alloy", "format": "wav"}
	}`)

	var req ChatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(req.Extra) != 3 {
		t.Fatalf("Extra = %+v, want 3 unmodeled keys", req.Extra)
	}
	for _, key := range []string{"prediction", "modalities", "audio"} {
		if _, ok := req.Extra[key]; !ok {
			t.Fatalf("Extra missing key %q: %+v", key, req.Extra)
		}
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var roundTripped map[string]json.RawMessage
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("re-unmarshal error = %v", err)
	}
	for _, key := range []string{"prediction", "modalities", "audio", "model", "messages"} {
		if _, ok := roundTripped[key]; !ok {
			t.Fatalf("re-encoded body missing key %q: %s", key, data)
		}
	}
}

// TestChatRequestKnownFieldsNotDuplicatedInExtra ensures fields modeled
// explicitly are never also captured in Extra (which would risk conflicting
// values winning depending on map iteration order).
func TestChatRequestKnownFieldsNotDuplicatedInExtra(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hi"}],
		"response_format": {"type": "json_object"},
		"max_completion_tokens": 100,
		"n": 2,
		"seed": 7,
		"presence_penalty": 0.1,
		"frequency_penalty": 0.2,
		"logit_bias": {"1234": -100},
		"user": "user-1",
		"stream_options": {"include_usage": true},
		"parallel_tool_calls": false,
		"reasoning_effort": "high",
		"service_tier": "priority",
		"store": true,
		"metadata": {"env": "prod"}
	}`)
	var req ChatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(req.Extra) != 0 {
		t.Fatalf("Extra = %+v, want empty (all keys modeled)", req.Extra)
	}
	if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" {
		t.Fatalf("ResponseFormat = %+v", req.ResponseFormat)
	}
	if req.MaxCompletionTokens == nil || *req.MaxCompletionTokens != 100 {
		t.Fatalf("MaxCompletionTokens = %v", req.MaxCompletionTokens)
	}
	if req.N == nil || *req.N != 2 {
		t.Fatalf("N = %v", req.N)
	}
	if req.Seed == nil || *req.Seed != 7 {
		t.Fatalf("Seed = %v", req.Seed)
	}
	if req.PresencePenalty == nil || *req.PresencePenalty != 0.1 {
		t.Fatalf("PresencePenalty = %v", req.PresencePenalty)
	}
	if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.2 {
		t.Fatalf("FrequencyPenalty = %v", req.FrequencyPenalty)
	}
	if req.LogitBias["1234"] != -100 {
		t.Fatalf("LogitBias = %v", req.LogitBias)
	}
	if req.User != "user-1" {
		t.Fatalf("User = %v", req.User)
	}
	if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
		t.Fatalf("StreamOptions = %+v", req.StreamOptions)
	}
	if req.ParallelToolCalls == nil || *req.ParallelToolCalls {
		t.Fatalf("ParallelToolCalls = %v", req.ParallelToolCalls)
	}
	if req.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %v", req.ReasoningEffort)
	}
	if req.ServiceTier != "priority" {
		t.Fatalf("ServiceTier = %v", req.ServiceTier)
	}
	if req.Store == nil || !*req.Store {
		t.Fatalf("Store = %v", req.Store)
	}
	if req.Metadata["env"] != "prod" {
		t.Fatalf("Metadata = %v", req.Metadata)
	}
}

// TestMessageNameRoundTrip verifies the OpenAI message.name field is
// preserved instead of being dropped.
func TestMessageNameRoundTrip(t *testing.T) {
	raw := []byte(`{"role":"user","name":"alice","content":"hi"}`)
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if msg.Name != "alice" {
		t.Fatalf("Name = %q, want alice", msg.Name)
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("re-unmarshal error = %v", err)
	}
	if out["name"] != "alice" {
		t.Fatalf("re-encoded name = %v, want alice", out["name"])
	}
}

func float64Ptr(v float64) *float64 { return &v }
func intPtr(v int) *int             { return &v }
func boolPtr(v bool) *bool          { return &v }
