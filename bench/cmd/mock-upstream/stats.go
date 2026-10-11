package main

import "sync"

// stats tracks per-request counters the harness relies on to verify
// gateway behavior it cannot observe directly from the client side: how
// many requests actually reached the mock (retries, fallbacks, cache
// hits/misses all show up here), broken down by API key and model.
type stats struct {
	mu sync.Mutex

	TotalRequests    int64            `json:"total_requests"`
	ByPath           map[string]int64 `json:"by_path"`
	ByModel          map[string]int64 `json:"by_model"`
	ByAPIKey         map[string]int64 `json:"by_api_key"`
	FaultsInjected   map[string]int64 `json:"faults_injected"`
	StreamsServed    int64            `json:"streams_served"`
	NonStreamsServed int64            `json:"non_streams_served"`
}

func newStats() *stats {
	return &stats{
		ByPath:         make(map[string]int64),
		ByModel:        make(map[string]int64),
		ByAPIKey:       make(map[string]int64),
		FaultsInjected: make(map[string]int64),
	}
}

func (s *stats) recordRequest(path, model, apiKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalRequests++
	s.ByPath[path]++
	if model != "" {
		s.ByModel[model]++
	}
	if apiKey != "" {
		s.ByAPIKey[apiKey]++
	}
}

func (s *stats) recordFault(faultType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.FaultsInjected[faultType]++
}

// reset clears all counters in place (no struct copy, so the embedded
// mutex is never overwritten).
func (s *stats) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalRequests = 0
	s.StreamsServed = 0
	s.NonStreamsServed = 0
	s.ByPath = make(map[string]int64)
	s.ByModel = make(map[string]int64)
	s.ByAPIKey = make(map[string]int64)
	s.FaultsInjected = make(map[string]int64)
}

func (s *stats) recordStream(streaming bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if streaming {
		s.StreamsServed++
	} else {
		s.NonStreamsServed++
	}
}

// statsSnapshot is the JSON-marshalable, lock-free copy returned by
// snapshot(); it deliberately does not embed sync.Mutex.
type statsSnapshot struct {
	TotalRequests    int64            `json:"total_requests"`
	ByPath           map[string]int64 `json:"by_path"`
	ByModel          map[string]int64 `json:"by_model"`
	ByAPIKey         map[string]int64 `json:"by_api_key"`
	FaultsInjected   map[string]int64 `json:"faults_injected"`
	StreamsServed    int64            `json:"streams_served"`
	NonStreamsServed int64            `json:"non_streams_served"`
}

// snapshot returns a deep-enough copy for JSON marshaling without holding
// the lock during encoding.
func (s *stats) snapshot() statsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := statsSnapshot{
		TotalRequests:    s.TotalRequests,
		StreamsServed:    s.StreamsServed,
		NonStreamsServed: s.NonStreamsServed,
		ByPath:           make(map[string]int64, len(s.ByPath)),
		ByModel:          make(map[string]int64, len(s.ByModel)),
		ByAPIKey:         make(map[string]int64, len(s.ByAPIKey)),
		FaultsInjected:   make(map[string]int64, len(s.FaultsInjected)),
	}
	for k, v := range s.ByPath {
		out.ByPath[k] = v
	}
	for k, v := range s.ByModel {
		out.ByModel[k] = v
	}
	for k, v := range s.ByAPIKey {
		out.ByAPIKey[k] = v
	}
	for k, v := range s.FaultsInjected {
		out.FaultsInjected[k] = v
	}
	return out
}
