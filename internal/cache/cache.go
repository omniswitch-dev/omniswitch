package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

type Cache interface {
	Get(key string) (*provider.ChatResponse, bool, error)
	Set(key string, val *provider.ChatResponse) error
}

var tokenPattern = regexp.MustCompile(`[a-z0-9]+`)

func Key(providerName string, req provider.ChatRequest) (string, error) {
	return KeyWithScope("", providerName, req)
}

// KeyWithScope derives an exact-cache key that is isolated to the supplied
// tenant scope. Keeping scope in the key prevents identical prompts from
// different workspaces or API keys sharing a cached completion.
//
// The key covers the request's full JSON encoding (via ChatRequest's own
// MarshalJSON, including passthrough fields captured in Extra) rather than a
// hand-picked subset of fields. Requests that differ only in, say,
// response_format, seed, or an unmodeled beta parameter must never collide
// on the same cached response.
func KeyWithScope(scope, providerName string, req provider.ChatRequest) (string, error) {
	req.Stream = false
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	payload := struct {
		Scope    string          `json:"scope,omitempty"`
		Provider string          `json:"provider"`
		Request  json.RawMessage `json:"request"`
	}{
		Scope:    scope,
		Provider: providerName,
		Request:  reqJSON,
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
