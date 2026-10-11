package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

// seededTokens deterministically generates n whitespace-separated tokens
// from a request-derived seed, so that repeated identical requests (used by
// the cache scenario, S6, to check hit-path correctness) produce byte-
// identical mock responses, while different requests produce different
// text (so a cache incorrectly matching different prompts is visible).
//
// This is intentionally not meant to look like real language -- it exists
// so the harness can assert "same seed => same output" and "different
// seed => different output" without shipping a language model.
func seededTokens(seed []byte, n int) string {
	h := sha256.Sum256(seed)
	state := binary.BigEndian.Uint64(h[:8])

	words := benchWordlist
	tokens := make([]string, n)
	for i := 0; i < n; i++ {
		// xorshift64*, cheap and deterministic.
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		tokens[i] = words[state%uint64(len(words))]
	}
	return strings.Join(tokens, " ")
}

// requestSeed builds a stable seed from the fields that should determine
// mock output content: the model name and the raw message/input bytes.
// Metadata like timestamps or request IDs must never be part of the seed.
func requestSeed(model string, body []byte) []byte {
	return []byte(fmt.Sprintf("%s|%x", model, sha256.Sum256(body)))
}

// benchWordlist is a small fixed vocabulary; content is irrelevant, only
// determinism and stable length matter for the harness.
var benchWordlist = []string{
	"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel",
	"india", "juliet", "kilo", "lima", "mike", "november", "oscar", "papa",
	"quebec", "romeo", "sierra", "tango", "uniform", "victor", "whiskey",
	"xray", "yankee", "zulu", "gateway", "latency", "throughput", "bench",
	"mock", "upstream", "stream", "token", "chunk", "cache", "retry",
}

// embeddingVector deterministically derives a fixed-length float vector
// from the seed, standing in for a real embeddings response.
func embeddingVector(seed []byte, dims int) []float64 {
	h := sha256.Sum256(seed)
	state := binary.BigEndian.Uint64(h[:8])
	out := make([]float64, dims)
	for i := 0; i < dims; i++ {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		// Map to [-1, 1).
		out[i] = float64(int64(state%2000000)-1000000) / 1000000.0
	}
	return out
}
