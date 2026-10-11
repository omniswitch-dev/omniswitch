package main

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

type openAIEmbeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// UnmarshalJSON allows Input to be a single string or an array of strings,
// matching the real OpenAI embeddings API.
func (r *openAIEmbeddingsRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		Model string `json:"model"`
		Input any    `json:"input"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Model = raw.Model
	switch v := raw.Input.(type) {
	case string:
		r.Input = []string{v}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				r.Input = append(r.Input, s)
			}
		}
	}
	return nil
}

const embeddingDims = 8

func (s *server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var req openAIEmbeddingsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}

	apiKey := bearerToken(r)
	s.stats.recordRequest("/v1/embeddings", req.Model, apiKey)

	if faultType, injected := s.resolveFault(r); injected {
		if delay := stallOrSlowDelay(faultType); delay > 0 {
			time.Sleep(delay)
		}
		if s.applyFault(w, faultType) {
			return
		}
	}
	time.Sleep(s.requestDelay(r))

	data := make([]map[string]any, 0, len(req.Input))
	totalTokens := 0
	for i, input := range req.Input {
		seed := requestSeed(req.Model, []byte(input))
		data = append(data, map[string]any{
			"object":    "embedding",
			"index":     i,
			"embedding": embeddingVector(seed, embeddingDims),
		})
		totalTokens += len(input)/4 + 1
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
		"model":  req.Model,
		"usage": map[string]int{
			"prompt_tokens": totalTokens,
			"total_tokens":  totalTokens,
		},
	})
}
