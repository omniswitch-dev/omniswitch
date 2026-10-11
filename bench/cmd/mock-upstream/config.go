package main

import (
	"sync"
	"time"
)

// config holds the mock upstream's default response-shaping knobs. Every
// field can be overridden per request via headers (see request.go), which
// is how the harness selects payload size / delay / fault behavior per
// scenario without restarting the container.
type config struct {
	// BaseDelay is added before the first byte of every response
	// (non-streaming) or before the first SSE chunk (streaming).
	BaseDelay time.Duration
	// StreamChunks is the number of SSE chunks emitted for a streaming
	// response.
	StreamChunks int
	// StreamChunkInterval is the fixed delay between successive SSE
	// chunks, used to make TTFT and inter-chunk timing measurable and
	// reproducible (see bench/cmd/sse-probe).
	StreamChunkInterval time.Duration
	// SmallOutputTokens / MediumOutputTokens control non-streaming and
	// streaming response length for the "small" (64 token) and "medium"
	// (512 token) payload sizes referenced in the scenario matrix
	// (bench/scenarios).
	SmallOutputTokens  int
	MediumOutputTokens int
}

func defaultConfig() config {
	return config{
		BaseDelay:           0,
		StreamChunks:        40,
		StreamChunkInterval: 20 * time.Millisecond,
		SmallOutputTokens:   64,
		MediumOutputTokens:  512,
	}
}

// faultState is the mutable, mutex-guarded fault-injection window set via
// POST /admin/fault and cleared via POST /admin/reset or a "type":"none"
// update. It intentionally has process-wide scope (not per-connection):
// scenario S5 drives it from bench/scripts/run-cell.sh as scheduled windows
// ("30s of 100% 429", "30s of 50% 500", ...), exactly as specified in the
// benchmark plan section 3.8.
type faultState struct {
	mu    sync.Mutex
	Type  string // "", "429", "500", "503", "reset", "stall", "midstream-disconnect", "slow-first-byte"
	Rate  float64
	Until time.Time
}

func (f *faultState) set(t string, rate float64, until time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Type = t
	f.Rate = rate
	f.Until = until
}

func (f *faultState) clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Type = ""
	f.Rate = 0
	f.Until = time.Time{}
}

// active returns the current fault type and rate if a fault window is in
// effect right now, or ("", 0, false) otherwise.
func (f *faultState) active() (string, float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Type == "" || f.Type == "none" {
		return "", 0, false
	}
	if time.Now().After(f.Until) {
		return "", 0, false
	}
	return f.Type, f.Rate, true
}
