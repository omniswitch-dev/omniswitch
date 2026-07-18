package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

// Moderations provides an OpenAI-compatible local moderation endpoint. It
// exposes the configured OmniSwitch guardrails without sending customer input
// to a third-party moderation API.
func (h *Handler) Moderations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxRequestBytes)
	var request struct {
		Model string `json:"model,omitempty"`
		Input any    `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	texts, err := moderationInputTexts(request.Input)
	if err != nil || len(texts) == 0 {
		if err == nil {
			err = fmt.Errorf("input is required")
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	requestID := newRequestID()
	traceID := requestHeaderOrNew(r, "x-omniswitch-trace-id", "trace")
	results := make([]map[string]any, 0, len(texts))
	for _, text := range texts {
		checks := []guardrail.Result(nil)
		if h.guardrails != nil {
			checks = h.guardrails.EvaluateInputContext(r.Context(), []provider.Message{{Role: "user", Content: text}})
			h.recordGuardrailResults(r.Context(), requestID, checks)
		}
		categories := map[string]bool{}
		scores := map[string]float64{}
		for _, check := range checks {
			if !check.Triggered {
				continue
			}
			category := normalizeModerationCategory(check.Type)
			categories[category] = true
			scores[category] = 1
		}
		results = append(results, map[string]any{
			"flagged":         len(categories) > 0,
			"categories":      categories,
			"category_scores": scores,
		})
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = "omniswitch-guardrails"
	}
	w.Header().Set("x-omniswitch-trace-id", traceID)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "modr_" + requestID,
		"model":   model,
		"results": results,
	})
}

func moderationInputTexts(input any) ([]string, error) {
	switch value := input.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("input is required")
		}
		return []string{value}, nil
	case []any:
		texts := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("input must be a string or an array of strings")
			}
			texts = append(texts, text)
		}
		return texts, nil
	default:
		return nil, fmt.Errorf("input must be a string or an array of strings")
	}
}

func normalizeModerationCategory(category string) string {
	category = strings.TrimSpace(strings.ToLower(category))
	if category == "" {
		return "other"
	}
	return category
}
