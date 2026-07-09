package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"

	"sentinel/internal/provider"
)

type Cache interface {
	Get(key string) (*provider.ChatResponse, bool, error)
	Set(key string, val *provider.ChatResponse) error
}

var tokenPattern = regexp.MustCompile(`[a-z0-9]+`)

func Key(providerName string, req provider.ChatRequest) (string, error) {
	req.Stream = false
	payload := struct {
		Provider    string             `json:"provider"`
		Model       string             `json:"model"`
		Messages    []provider.Message `json:"messages"`
		Temperature *float64           `json:"temperature,omitempty"`
		MaxTokens   *int               `json:"max_tokens,omitempty"`
		TopP        *float64           `json:"top_p,omitempty"`
		Stop        []string           `json:"stop,omitempty"`
	}{
		Provider:    providerName,
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		TopP:        req.TopP,
		Stop:        req.Stop,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func PromptText(messages []provider.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, strings.ToLower(message.Role)+": "+message.Text())
	}
	return strings.Join(parts, "\n")
}

func Vectorize(text string) map[string]float64 {
	tokens := tokenPattern.FindAllString(strings.ToLower(text), -1)
	vector := map[string]float64{}
	for _, token := range tokens {
		if isStopWord(token) {
			continue
		}
		vector[token]++
	}
	normalize(vector)
	return vector
}

func Similarity(left, right map[string]float64) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	if len(left) > len(right) {
		left, right = right, left
	}
	var dot float64
	for token, leftWeight := range left {
		dot += leftWeight * right[token]
	}
	return dot
}

func StableTokens(vector map[string]float64) []string {
	tokens := make([]string, 0, len(vector))
	for token := range vector {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	return tokens
}

func normalize(vector map[string]float64) {
	var magnitude float64
	for _, weight := range vector {
		magnitude += weight * weight
	}
	if magnitude == 0 {
		return
	}
	magnitude = math.Sqrt(magnitude)
	for token, weight := range vector {
		vector[token] = weight / magnitude
	}
}

func isStopWord(token string) bool {
	switch token {
	case "a", "an", "and", "are", "as", "at", "be", "by", "for", "from", "how", "i", "in", "is", "it", "of", "on", "or", "the", "to", "what", "with", "you":
		return true
	default:
		return false
	}
}
