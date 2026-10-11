// Command mock-upstream is a deterministic fake LLM provider used by the
// OmniSwitch competitive benchmark harness (see bench/README.md). It speaks
// the OpenAI chat-completions/embeddings shapes and the Anthropic Messages
// shape, with configurable delay, streaming chunk cadence, and fault
// injection, so that gateway overhead can be isolated from real provider
// latency and variance.
//
// It intentionally has zero third-party dependencies: the harness needs a
// reproducible, auditable mock, not a feature-rich one.
package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	addr := ":" + envOr("MOCK_PORT", "4000")

	cfg := defaultConfig()
	if v := os.Getenv("MOCK_BASE_DELAY_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.BaseDelay = time.Duration(n) * time.Millisecond
		}
	}
	if v := os.Getenv("MOCK_STREAM_CHUNKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.StreamChunks = n
		}
	}
	if v := os.Getenv("MOCK_STREAM_CHUNK_INTERVAL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.StreamChunkInterval = time.Duration(n) * time.Millisecond
		}
	}

	srv := newServer(cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", srv.handleHealthz)
	mux.HandleFunc("/stats", srv.handleStats)
	mux.HandleFunc("/admin/fault", srv.handleAdminFault)
	mux.HandleFunc("/admin/reset", srv.handleAdminReset)
	mux.HandleFunc("/v1/chat/completions", srv.handleChatCompletions)
	mux.HandleFunc("/v1/embeddings", srv.handleEmbeddings)
	mux.HandleFunc("/v1/messages", srv.handleAnthropicMessages)

	log.Printf("mock-upstream listening on %s (base_delay=%s stream_chunks=%d chunk_interval=%s)",
		addr, cfg.BaseDelay, cfg.StreamChunks, cfg.StreamChunkInterval)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("mock-upstream: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
