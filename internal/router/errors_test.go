package router

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

// scriptedProvider returns the scripted errors in order (nil means success)
// and then succeeds.
type scriptedProvider struct {
	name  string
	model string

	mu     sync.Mutex
	script []error
	calls  int
}

func (s *scriptedProvider) Name() string { return s.name }

func (s *scriptedProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: s.model, Object: "model", OwnedBy: s.name, Provider: s.name}}
}

func (s *scriptedProvider) ChatCompletion(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	s.mu.Lock()
	var err error
	if s.calls < len(s.script) {
		err = s.script[s.calls]
	}
	s.calls++
	s.mu.Unlock()
	meta := provider.ProviderMeta{Provider: s.name, Model: req.Model, Latency: time.Millisecond}
	if err != nil {
		meta.Error = err.Error()
		return provider.ChatResponse{}, meta, err
	}
	return provider.ChatResponse{
		Model:   req.Model,
		Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
	}, meta, nil
}

func (s *scriptedProvider) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func upstreamErr(status int) error {
	return &provider.UpstreamError{Provider: "p", Op: "p error", StatusCode: status, Body: "{}"}
}

func TestUpstreamStatus(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		code   int
		hasErr bool
	}{
		{"nil", nil, 0, false},
		{"typed", upstreamErr(429), 429, true},
		{"wrapped typed", fmt.Errorf("all providers exhausted: %w", upstreamErr(503)), 503, true},
		// The typed status wins over status text echoed in the body.
		{"typed beats body text", &provider.UpstreamError{Op: "x", StatusCode: 400, Body: "status 500"}, 400, true},
		{"text", errors.New("custom error (status code 502)"), 502, true},
		{"no status", errors.New("dial tcp: connection refused"), 0, false},
	}
	for _, tt := range tests {
		code, ok := upstreamStatus(tt.err)
		if code != tt.code || ok != tt.hasErr {
			t.Errorf("%s: upstreamStatus() = %d, %v; want %d, %v", tt.name, code, ok, tt.code, tt.hasErr)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		codes []int
		want  bool
	}{
		{"nil", nil, nil, false},
		{"network error", errors.New("connection reset by peer"), nil, true},
		{"deadline", context.DeadlineExceeded, nil, true},
		{"bad request", upstreamErr(400), nil, false},
		{"unauthorized", upstreamErr(401), nil, false},
		{"not found", upstreamErr(404), nil, false},
		{"request timeout", upstreamErr(408), nil, true},
		{"throttled", upstreamErr(429), nil, true},
		{"server error", upstreamErr(503), nil, true},
		{"unsupported parameter", &provider.UnsupportedParameterError{Provider: "anthropic", Param: "seed"}, nil, false},
		{"listed code", upstreamErr(429), []int{429}, true},
		{"unlisted code", upstreamErr(500), []int{429}, false},
		{"listed text code", errors.New("upstream status 503"), []int{503}, true},
		{"no status with codes", errors.New("connection reset"), []int{503}, false},
	}
	for _, tt := range tests {
		if got := isRetryable(tt.err, tt.codes); got != tt.want {
			t.Errorf("%s: isRetryable() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestRetryWait(t *testing.T) {
	if delay, ok := retryWait("100ms", 0, errors.New("boom")); !ok || delay != 100*time.Millisecond {
		t.Fatalf("plain error: retryWait() = %v, %v; want 100ms, true", delay, ok)
	}
	hinted := &provider.UpstreamError{Op: "x", StatusCode: 429, RetryAfter: 2 * time.Second}
	if delay, ok := retryWait("100ms", 0, hinted); !ok || delay != 2*time.Second {
		t.Fatalf("Retry-After 2s: retryWait() = %v, %v; want 2s, true", delay, ok)
	}
	short := &provider.UpstreamError{Op: "x", StatusCode: 429, RetryAfter: time.Millisecond}
	if delay, ok := retryWait("100ms", 1, short); !ok || delay != 200*time.Millisecond {
		t.Fatalf("short Retry-After: retryWait() = %v, %v; want the 200ms backoff, true", delay, ok)
	}
	long := &provider.UpstreamError{Op: "x", StatusCode: 429, RetryAfter: time.Minute}
	if _, ok := retryWait("100ms", 0, fmt.Errorf("wrapped: %w", long)); ok {
		t.Fatal("Retry-After 1m: retryWait() ok = true, want false so the next provider is tried")
	}
}

func TestCountsAsProviderFailure(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	type failureCase struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}
	tests := []failureCase{
		{"nil error", context.Background(), nil, false},
		{"plain upstream error", context.Background(), errors.New("status 500"), true},
		{"deadline", context.Background(), context.DeadlineExceeded, true},
		{"cancelled error", context.Background(), fmt.Errorf("read: %w", context.Canceled), false},
		{"parent done", cancelled, errors.New("status 500"), false},
		{"unsupported parameter", context.Background(), &provider.UnsupportedParameterError{Provider: "p", Param: "seed"}, false},
	}
	// Client errors must not trip the breaker; timeouts, throttling and
	// server errors must.
	for _, code := range []int{400, 401, 403, 404, 409, 413, 422} {
		tests = append(tests, failureCase{fmt.Sprintf("status %d", code), context.Background(), upstreamErr(code), false})
	}
	for _, code := range []int{408, 429, 500, 502, 503, 529} {
		tests = append(tests, failureCase{fmt.Sprintf("status %d", code), context.Background(), upstreamErr(code), true})
	}
	for _, tt := range tests {
		if got := countsAsProviderFailure(tt.ctx, tt.err); got != tt.want {
			t.Errorf("%s: countsAsProviderFailure() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestClientErrorsDoNotOpenCircuit checks that a caller cannot open a
// provider's circuit for everyone by sending requests the provider rejects.
func TestClientErrorsDoNotOpenCircuit(t *testing.T) {
	bad := make([]error, 10)
	for i := range bad {
		bad[i] = upstreamErr(400)
	}
	primary := &scriptedProvider{name: "primary", model: "m", script: bad}
	registry := provider.NewRegistry()
	registry.Register(primary)
	rtr := New(registry)
	rtr.SetCircuitBreaker(NewCircuitBreaker(2, time.Hour))
	rtr.SetRoute("m", Route{Provider: "primary", MaxRetries: 2, RetryBackoff: "1ms"})

	for i := 0; i < len(bad); i++ {
		if _, _, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "m"}, ""); err == nil {
			t.Fatalf("Execute(%d) error = nil, want the 400", i)
		}
	}
	// A 400 is not retried against the same provider.
	if got := primary.callCount(); got != len(bad) {
		t.Fatalf("primary calls = %d, want %d (one per request, no retries on 400)", got, len(bad))
	}
	if state := rtr.breakers.State("primary"); state != CircuitClosed {
		t.Fatalf("circuit state after client errors = %s, want closed", state)
	}
	if _, _, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "m"}, ""); err != nil {
		t.Fatalf("valid request after client errors failed: %v", err)
	}
}

func TestServerErrorsStillOpenCircuit(t *testing.T) {
	primary := &scriptedProvider{name: "primary", model: "m", script: []error{upstreamErr(503), upstreamErr(503), upstreamErr(503)}}
	registry := provider.NewRegistry()
	registry.Register(primary)
	rtr := New(registry)
	rtr.SetCircuitBreaker(NewCircuitBreaker(2, time.Hour))
	rtr.SetRoute("m", Route{Provider: "primary"})

	for i := 0; i < 2; i++ {
		if _, _, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "m"}, ""); err == nil {
			t.Fatalf("Execute(%d) error = nil, want the 503", i)
		}
	}
	if state := rtr.breakers.State("primary"); state != CircuitOpen {
		t.Fatalf("circuit state after server errors = %s, want open", state)
	}
}

func TestExecuteHonorsShortRetryAfter(t *testing.T) {
	hint := 60 * time.Millisecond
	primary := &scriptedProvider{name: "primary", model: "m", script: []error{
		&provider.UpstreamError{Op: "primary error", StatusCode: 429, RetryAfter: hint},
	}}
	registry := provider.NewRegistry()
	registry.Register(primary)
	rtr := New(registry)
	rtr.SetRoute("m", Route{Provider: "primary", MaxRetries: 1, RetryBackoff: "1ms"})

	start := time.Now()
	if _, _, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "m"}, ""); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed < hint {
		t.Fatalf("retry ran after %v, want at least the %v Retry-After hint", elapsed, hint)
	}
	if got := primary.callCount(); got != 2 {
		t.Fatalf("primary calls = %d, want 2", got)
	}
}

func TestExecuteLongRetryAfterFallsBack(t *testing.T) {
	primary := &scriptedProvider{name: "primary", model: "m", script: []error{
		&provider.UpstreamError{Op: "primary error", StatusCode: 429, RetryAfter: time.Hour},
	}}
	fallback := &scriptedProvider{name: "fallback", model: "m2"}
	registry := provider.NewRegistry()
	registry.Register(primary)
	registry.Register(fallback)
	rtr := New(registry)
	rtr.SetRoute("m", Route{Strategy: "fallback", Provider: "primary", Fallbacks: []string{"fallback"}, MaxRetries: 3, RetryBackoff: "1ms"})

	start := time.Now()
	_, meta, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "m"}, "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if meta.Provider != "fallback" {
		t.Fatalf("provider = %q, want fallback", meta.Provider)
	}
	if got := primary.callCount(); got != 1 {
		t.Fatalf("primary calls = %d, want 1 (no retry under a 1h Retry-After)", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("fallback took %v; the router must not wait for a long Retry-After", elapsed)
	}
}

func TestCloneForShadowIsolatesBreakerAndLatency(t *testing.T) {
	primary := &scriptedProvider{name: "primary", model: "m", script: []error{upstreamErr(503)}}
	registry := provider.NewRegistry()
	registry.Register(primary)
	rtr := New(registry)
	production := NewCircuitBreaker(1, time.Hour)
	rtr.SetCircuitBreaker(production)
	rtr.SetRoute("m", Route{Provider: "primary"})

	shadowBreaker := NewCircuitBreaker(1, time.Hour)
	shadow := rtr.CloneForShadow(shadowBreaker)
	if _, _, err := shadow.Execute(context.Background(), provider.ChatRequest{Model: "m"}, "primary"); err == nil {
		t.Fatal("shadow Execute() error = nil, want the scripted 503")
	}
	if state := shadowBreaker.State("primary"); state != CircuitOpen {
		t.Fatalf("shadow breaker = %s, want open", state)
	}
	if state := production.State("primary"); state != CircuitClosed {
		t.Fatalf("production breaker = %s, want closed: shadow failures must not count", state)
	}
	if _, _, err := shadow.Execute(context.Background(), provider.ChatRequest{Model: "m"}, "primary"); err == nil {
		t.Fatal("second shadow Execute() error = nil, want circuit open")
	}

	// A successful shadow call must not feed production latency stats.
	fresh := rtr.CloneForShadow(NewCircuitBreaker(5, time.Hour))
	if _, _, err := fresh.Execute(context.Background(), provider.ChatRequest{Model: "m"}, "primary"); err != nil {
		t.Fatalf("shadow Execute() error = %v", err)
	}
	if avg := rtr.latency.average("primary"); avg != 0 {
		t.Fatalf("production latency average = %v, want 0 after shadow-only traffic", avg)
	}
	if avg := fresh.latency.average("primary"); avg == 0 {
		t.Fatal("shadow latency average = 0, want the shadow call recorded on the clone")
	}
}
