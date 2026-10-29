package provider

import "strings"

// OpenAI finish_reason values. Every adapter maps its native stop reason to
// one of these so clients can switch on a closed set.
const (
	finishStop          = "stop"
	finishLength        = "length"
	finishToolCalls     = "tool_calls"
	finishContentFilter = "content_filter"
)

// mapAnthropicStopReason maps an Anthropic Messages stop_reason.
func mapAnthropicStopReason(reason string) string {
	switch reason {
	case "":
		return ""
	case "max_tokens", "model_context_window_exceeded":
		return finishLength
	case "tool_use":
		return finishToolCalls
	case "refusal":
		return finishContentFilter
	default: // end_turn, stop_sequence, pause_turn and future values
		return finishStop
	}
}

// mapBedrockStopReason maps a Bedrock Converse stopReason.
func mapBedrockStopReason(reason string) string {
	switch reason {
	case "":
		return ""
	case "max_tokens", "model_context_window_exceeded":
		return finishLength
	case "tool_use":
		return finishToolCalls
	case "guardrail_intervened", "content_filtered":
		return finishContentFilter
	default: // end_turn, stop_sequence and future values
		return finishStop
	}
}

// mapGeminiFinishReason maps a Gemini candidate finishReason. An empty or
// unspecified reason means the candidate is not finished yet.
func mapGeminiFinishReason(reason string) string {
	switch strings.ToUpper(reason) {
	case "", "FINISH_REASON_UNSPECIFIED":
		return ""
	case "MAX_TOKENS":
		return finishLength
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII",
		"IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION":
		return finishContentFilter
	default: // STOP, LANGUAGE, OTHER, MALFORMED_FUNCTION_CALL, ...
		return finishStop
	}
}
