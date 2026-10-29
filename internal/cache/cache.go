package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// PromptText renders a conversation as the text the semantic cache
// vectorises: one "role: text" line per message, in order. Every non-text
// content part (image, audio, file, ...) and every assistant tool call is
// rendered as a stable digest token such as "[image:<sha256>]", so two
// prompts that share a caption but carry different images can never be
// treated as the same prompt.
func PromptText(messages []provider.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, strings.ToLower(message.Role)+": "+messageText(message))
	}
	return strings.Join(parts, "\n")
}

func messageText(message provider.Message) string {
	var segments []string
	if len(message.ContentParts) > 0 {
		for _, part := range message.ContentParts {
			segments = append(segments, contentPartSegments(part)...)
		}
	} else if message.Content != "" {
		segments = append(segments, message.Content)
	}
	for _, call := range message.ToolCalls {
		segments = append(segments, digestToken("tool_call", call.Type, call.Function.Name, call.Function.Arguments))
	}
	return strings.Join(segments, "\n")
}

func contentPartSegments(part provider.ContentPart) []string {
	var segments []string
	if part.Text != "" {
		segments = append(segments, part.Text)
	}
	switch {
	case part.ImageURL != nil:
		segments = append(segments, digestToken("image", part.ImageURL.URL))
	case part.InputAudio != nil:
		segments = append(segments, digestToken("audio", part.InputAudio.Format, part.InputAudio.Data))
	case part.Type == "text" || part.Type == "input_text" || part.Text != "":
		// Plain text part, already added above.
	default:
		// Unknown non-text part: digest everything we know about it.
		data, _ := json.Marshal(part)
		segments = append(segments, digestToken("part", string(data)))
	}
	return segments
}

// digestToken returns "[kind:<sha256 hex>]" over the given fields.
func digestToken(kind string, fields ...string) string {
	hash := sha256.New()
	for _, field := range fields {
		// Length-prefix each field so field boundaries are unambiguous.
		_, _ = fmt.Fprintf(hash, "%d:%s;", len(field), field)
	}
	return "[" + kind + ":" + hex.EncodeToString(hash.Sum(nil)) + "]"
}

var (
	// digestPattern matches the digest tokens written by PromptText.
	digestPattern = regexp.MustCompile(`\[([a-z_]+):([0-9a-f]{64})\]`)
	// roleLinePattern matches the "role: " prefix PromptText writes at the
	// start of every message line.
	roleLinePattern = regexp.MustCompile(`^(system|developer|user|assistant|tool|function): `)
)

// Vectorize turns prompt text into a sparse, L2-normalised feature vector
// for cosine similarity.
//
// Features are role-aware ("user:transfer") and order-aware: besides
// unigrams it adds bigrams of adjacent non-stop-word tokens, so prompts
// that only differ in word order ("from alice to bob" vs "from bob to
// alice") no longer look identical. Digest tokens for non-text parts
// (see PromptText) are not treated as words; instead their combined digest
// namespaces every feature, so prompts with different images, audio or
// tool calls share no features at all and have similarity 0.
func Vectorize(text string) map[string]float64 {
	var digests []string
	text = digestPattern.ReplaceAllStringFunc(text, func(token string) string {
		digests = append(digests, token)
		return " "
	})
	namespace := ""
	if len(digests) > 0 {
		sum := sha256.Sum256([]byte(strings.Join(digests, "\n")))
		namespace = "#" + hex.EncodeToString(sum[:8]) + "|"
	}

	vector := map[string]float64{}
	role := ""
	var previous string
	for _, line := range strings.Split(text, "\n") {
		if match := roleLinePattern.FindStringSubmatch(line); match != nil {
			role = match[1]
			line = line[len(match[0]):]
			previous = ""
		}
		prefix := namespace
		if role != "" {
			prefix += role + ":"
		}
		for _, token := range tokenPattern.FindAllString(strings.ToLower(line), -1) {
			if isStopWord(token) {
				continue
			}
			vector[prefix+token]++
			if previous != "" {
				vector[prefix+previous+"_"+token]++
			}
			previous = token
		}
	}
	if len(vector) == 0 && namespace != "" {
		// A prompt made only of non-text parts still needs a feature so
		// identical media can match each other.
		vector[namespace]++
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
