package router

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

// blockingProvider blocks until the call context ends.
type blockingProvider struct {
	name  string
	model string
}

func (b *blockingProvider) Name() string { return b.name }
func (b *blockingProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: b.model, Provider: b.name}}
}
func (b *blockingProvider) ChatCompletion(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	<-ctx.Done()
	return provider.ChatResponse{}, provider.ProviderMeta{Provider: b.name}, ctx.Err()
}
func (b *blockingProvider) ChatCompletionStream(ctx context.Context, _ provider.ChatRequest) (<-chan provider.ChatResponseChunk, provider.ProviderMeta, error) {
	<-ctx.Done()
	return nil, provider.ProviderMeta{Provider: b.name}, ctx.Err()
}
func (b *blockingProvider) Embeddings(ctx context.Context, _ provider.EmbeddingRequest) (provider.EmbeddingResponse, provider.ProviderMeta, error) {
	<-ctx.Done()
	return provider.EmbeddingResponse{}, provider.ProviderMeta{Provider: b.name}, ctx.Err()
}

// scriptedStreamer emits chunks with a delay before each one. If the call
// context ends it closes the channel, like a body read failing on cancel.
type scriptedStreamer struct {
	name       string
	model      string
	firstDelay time.Duration
	delay      time.Duration
	chunks     int
	// hangAfter stops emitting (but keeps the channel open) after that
	// many chunks when > 0.
	hangAfter int
	errFirst  error
	calls     atomic.Int32
	finished  chan struct{}
}

func (s *scriptedStreamer) Name() string { return s.name }
func (s *scriptedStreamer) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: s.model, Provider: s.name}}
}
func (s *scriptedStreamer) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	return provider.ChatResponse{}, provider.ProviderMeta{}, errors.New("not used")
}
func (s *scriptedStreamer) ChatCompletionStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatResponseChunk, provider.ProviderMeta, error) {
	s.calls.Add(1)
	ch := make(chan provider.ChatResponseChunk)
	go func() {
		defer func() {
			close(ch)
			if s.finished != nil {
				close(s.finished)
			}
		}()
		if s.errFirst != nil {
			ch <- provider.ChatResponseChunk{Err: s.errFirst}
			return
		}
		for i := 0; i < s.chunks; i++ {
			delay := s.delay
			if i == 0 {
				delay = s.firstDelay
			}
			if s.hangAfter > 0 && i >= s.hangAfter {
				delay = time.Hour
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			// Deliberately unguarded send: the router must drain.
			ch <- provider.ChatResponseChunk{Model: req.Model, Choices: []provider.ChunkChoice{{Delta: provider.Message{Content: "x"}}}}
		}
	}()
	return ch, provider.ProviderMeta{Provider: s.name, Model: req.Model}, nil
}

func collect(t *testing.T, chunks <-chan provider.ChatResponseChunk) (int, error) {
	t.Helper()
	n := 0
	var streamErr error
	timeout := time.After(5 * time.Second)
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				return n, streamErr
			}
			if chunk.Err != nil {
				streamErr = chunk.Err
				continue
			}
			n++
		case <-timeout:
			t.Fatal("stream did not finish")
		}
	}
}

func TestExecuteNonRetryableErrorFallsBack(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		route Route
	}{
		{
			name:  "unsupported parameter",
			err:   &provider.UnsupportedParameterError{Provider: "primary", Param: "seed"},
			route: Route{Provider: "primary", Fallbacks: []string{"fallback"}, MaxRetries: 2},
		},
		{
			name:  "status not in retry codes",
			err:   fmt.Errorf("openai error (status 500): boom"),
			route: Route{Provider: "primary", Fallbacks: []string{"fallback"}, RetryCodes: []int{429}, MaxRetries: 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			primaryCalls := 0
			registry := provider.NewRegistry()
			registry.Register(&fakeProvider{name: "primary", model: "test-model", err: tt.err, calls: &primaryCalls})
			registry.Register(&fakeProvider{name: "fallback", model: "fb"})
			rtr := New(registry)
			rtr.SetRoute("test-model", tt.route)
			_, meta, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "test-model"}, "")
			if err != nil {
				t.Fatalf("Execute() error = %v, want fallback success", err)
			}
			if meta.Provider != "fallback" || !meta.Fallback {
				t.Fatalf("meta = %+v, want fallback provider", meta)
			}
			if primaryCalls != 1 {
				t.Fatalf("primary calls = %d, want 1 (non-retryable)", primaryCalls)
			}
		})
	}
}

func TestExecuteReturnsWhenContextCancelled(t *testing.T) {
	fallbackCalls := 0
	registry := provider.NewRegistry()
	registry.Register(&blockingProvider{name: "primary", model: "test-model"})
	registry.Register(&fakeProvider{name: "fallback", model: "fb", calls: &fallbackCalls})
	rtr := New(registry)
	rtr.SetRoute("test-model", Route{Provider: "primary", Fallbacks: []string{"fallback"}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err := rtr.Execute(ctx, provider.ChatRequest{Model: "test-model"}, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v, want context deadline", err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("fallback calls = %d, want 0 after the request context ended", fallbackCalls)
	}
}

func TestClientCancellationDoesNotTripBreaker(t *testing.T) {
	paths := map[string]func(r *Router, ctx context.Context){
		"chat": func(r *Router, ctx context.Context) {
			_, _, _ = r.Execute(ctx, provider.ChatRequest{Model: "m"}, "p")
		},
		"stream": func(r *Router, ctx context.Context) {
			_, _, _ = r.ExecuteStream(ctx, provider.ChatRequest{Model: "m"}, "p")
		},
		"embeddings": func(r *Router, ctx context.Context) {
			_, _, _ = r.Embeddings(ctx, provider.EmbeddingRequest{Model: "m"}, "p")
		},
	}
	for name, call := range paths {
		t.Run(name, func(t *testing.T) {
			registry := provider.NewRegistry()
			registry.Register(&blockingProvider{name: "p", model: "m"})
			rtr := New(registry)
			rtr.SetCircuitBreaker(NewCircuitBreaker(2, time.Hour))
			for i := 0; i < 4; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				go func() { time.Sleep(2 * time.Millisecond); cancel() }()
				call(rtr, ctx)
				cancel()
			}
			if !rtr.CircuitBreaker().Available("p") {
				t.Fatal("client cancellations opened the circuit breaker")
			}
		})
	}
}

func TestRouteTimeoutStillCountsAsFailure(t *testing.T) {
	registry := provider.NewRegistry()
	registry.Register(&blockingProvider{name: "p", model: "m"})
	rtr := New(registry)
	rtr.SetCircuitBreaker(NewCircuitBreaker(1, time.Hour))
	rtr.SetRoute("m", Route{Provider: "p", Timeout: "5ms"})
	if _, _, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "m"}, ""); err == nil {
		t.Fatal("Execute() error = nil, want timeout")
	}
	if rtr.CircuitBreaker().Available("p") {
		t.Fatal("route timeout should count as a provider failure")
	}
}

func TestExecuteStreamRouteTimeoutOnlyLimitsFirstChunk(t *testing.T) {
	finished := make(chan struct{})
	streamer := &scriptedStreamer{name: "s", model: "m", firstDelay: 5 * time.Millisecond, delay: 20 * time.Millisecond, chunks: 10, finished: finished}
	registry := provider.NewRegistry()
	registry.Register(streamer)
	rtr := New(registry)
	rtr.SetRoute("m", Route{Provider: "s", Timeout: "70ms"})
	chunks, _, err := rtr.ExecuteStream(context.Background(), provider.ChatRequest{Model: "m"}, "")
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	n, streamErr := collect(t, chunks)
	if n != 10 || streamErr != nil {
		t.Fatalf("got %d chunks, err %v; want 10 chunks and no error", n, streamErr)
	}
	<-finished
}

func TestExecuteStreamFirstChunkTimeoutFallsBack(t *testing.T) {
	slow := &scriptedStreamer{name: "slow", model: "m", firstDelay: time.Hour, chunks: 1, finished: make(chan struct{})}
	fast := &scriptedStreamer{name: "fast", model: "m", chunks: 3}
	registry := provider.NewRegistry()
	registry.Register(slow)
	registry.Register(fast)
	rtr := New(registry)
	rtr.SetCircuitBreaker(NewCircuitBreaker(1, time.Hour))
	rtr.SetRoute("m", Route{Provider: "slow", Fallbacks: []string{"fast"}, Timeout: "20ms"})
	chunks, meta, err := rtr.ExecuteStream(context.Background(), provider.ChatRequest{Model: "m"}, "")
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if meta.Provider != "fast" || !meta.Fallback {
		t.Fatalf("meta = %+v, want fallback to fast", meta)
	}
	if n, streamErr := collect(t, chunks); n != 3 || streamErr != nil {
		t.Fatalf("got %d chunks, err %v; want 3 chunks", n, streamErr)
	}
	select {
	case <-slow.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("slow provider stream was not cancelled")
	}
	if rtr.CircuitBreaker().Available("slow") {
		t.Fatal("first-chunk timeout should count as a provider failure")
	}
}

func TestExecuteStreamFirstChunkErrorFallsBack(t *testing.T) {
	broken := &scriptedStreamer{name: "broken", model: "m", errFirst: errors.New("upstream error event")}
	fast := &scriptedStreamer{name: "fast", model: "m", chunks: 2}
	registry := provider.NewRegistry()
	registry.Register(broken)
	registry.Register(fast)
	rtr := New(registry)
	rtr.SetRoute("m", Route{Provider: "broken", Fallbacks: []string{"fast"}})
	chunks, meta, err := rtr.ExecuteStream(context.Background(), provider.ChatRequest{Model: "m"}, "")
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if meta.Provider != "fast" {
		t.Fatalf("meta = %+v, want fast", meta)
	}
	if n, streamErr := collect(t, chunks); n != 2 || streamErr != nil {
		t.Fatalf("got %d chunks, err %v", n, streamErr)
	}
}

// SF3: a stream that closes without any chunk is a failed attempt, not an
// empty success, and the router falls back like for a first-chunk error.
func TestExecuteStreamEmptyStreamFails(t *testing.T) {
	empty := &scriptedStreamer{name: "empty", model: "m"}
	registry := provider.NewRegistry()
	registry.Register(empty)
	rtr := New(registry)
	rtr.SetRoute("m", Route{Provider: "empty"})
	_, _, err := rtr.ExecuteStream(context.Background(), provider.ChatRequest{Model: "m"}, "")
	if !errors.Is(err, provider.ErrStreamTruncated) {
		t.Fatalf("ExecuteStream() error = %v, want ErrStreamTruncated", err)
	}

	fast := &scriptedStreamer{name: "fast", model: "m", chunks: 2}
	registry.Register(fast)
	rtr.SetRoute("m", Route{Provider: "empty", Fallbacks: []string{"fast"}})
	chunks, meta, err := rtr.ExecuteStream(context.Background(), provider.ChatRequest{Model: "m"}, "")
	if err != nil {
		t.Fatalf("ExecuteStream() with fallback error = %v", err)
	}
	if meta.Provider != "fast" {
		t.Fatalf("meta = %+v, want fast", meta)
	}
	if n, streamErr := collect(t, chunks); n != 2 || streamErr != nil {
		t.Fatalf("got %d chunks, err %v", n, streamErr)
	}
}

func TestExecuteStreamIdleTimeoutEndsWithError(t *testing.T) {
	streamer := &scriptedStreamer{name: "s", model: "m", chunks: 5, hangAfter: 2, finished: make(chan struct{})}
	registry := provider.NewRegistry()
	registry.Register(streamer)
	rtr := New(registry)
	rtr.SetRoute("m", Route{Provider: "s", Timeout: "30ms"})
	chunks, _, err := rtr.ExecuteStream(context.Background(), provider.ChatRequest{Model: "m"}, "")
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	n, streamErr := collect(t, chunks)
	if n != 2 || !errors.Is(streamErr, errStreamIdleTimeout) {
		t.Fatalf("got %d chunks, err %v; want 2 chunks then idle timeout", n, streamErr)
	}
	select {
	case <-streamer.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream stream was not cancelled after idle timeout")
	}
}

func TestExecuteStreamContextCancelSendsErrorAndReleasesUpstream(t *testing.T) {
	streamer := &scriptedStreamer{name: "s", model: "m", chunks: 100, delay: 5 * time.Millisecond, finished: make(chan struct{})}
	registry := provider.NewRegistry()
	registry.Register(streamer)
	rtr := New(registry)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chunks, _, err := rtr.ExecuteStream(ctx, provider.ChatRequest{Model: "m"}, "s")
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	first := <-chunks
	if first.Err != nil {
		t.Fatalf("first chunk error = %v", first.Err)
	}
	cancel()
	var streamErr error
	for chunk := range chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if !errors.Is(streamErr, context.Canceled) {
		t.Fatalf("terminal error = %v, want context.Canceled", streamErr)
	}
	select {
	case <-streamer.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream goroutine leaked after cancellation")
	}
}

func TestExecuteStreamAbandonedConsumerDoesNotLeak(t *testing.T) {
	streamer := &scriptedStreamer{name: "s", model: "m", chunks: 100, finished: make(chan struct{})}
	registry := provider.NewRegistry()
	registry.Register(streamer)
	rtr := New(registry)
	ctx, cancel := context.WithCancel(context.Background())
	if _, _, err := rtr.ExecuteStream(ctx, provider.ChatRequest{Model: "m"}, "s"); err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	// The consumer never reads; cancelling the request must still release
	// the relay and the upstream producer.
	cancel()
	select {
	case <-streamer.finished:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream goroutine leaked when the consumer stopped reading")
	}
}

func TestConditionalVariantsCompileOnce(t *testing.T) {
	registry := provider.NewRegistry()
	registry.Register(&fakeProvider{name: "code", model: "code-model"})
	registry.Register(&fakeProvider{name: "chat", model: "chat-model"})
	rtr := New(registry)
	rtr.SetRoute("logical", Route{Variants: []Variant{
		{Name: "broken", Provider: "code", Model: "code-model", Weight: 1, Condition: "prompt.contains("},
		{Name: "code", Provider: "code", Model: "code-model", Weight: 1, Condition: `prompt.contains("func")`},
		{Name: "chat", Provider: "chat", Model: "chat-model", Weight: 1, Condition: `!prompt.contains("func")`},
	}})
	if len(rtr.conditions) != 3 {
		t.Fatalf("compiled conditions = %d, want 3 compiled at route load", len(rtr.conditions))
	}
	if compiled := rtr.conditions["prompt.contains("]; compiled.err == nil || compiled.program != nil {
		t.Fatalf("invalid condition compiled = %+v, want error", compiled)
	}
	for i := 0; i < 5; i++ {
		_, meta, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "logical", Messages: []provider.Message{{Role: "user", Content: "write func main"}}}, "")
		if err != nil || meta.Provider != "code" {
			t.Fatalf("Execute() = %+v, %v; want code variant", meta, err)
		}
		_, meta, err = rtr.Execute(context.Background(), provider.ChatRequest{Model: "logical", Messages: []provider.Message{{Role: "user", Content: "hello"}}}, "")
		if err != nil || meta.Provider != "chat" {
			t.Fatalf("Execute() = %+v, %v; want chat variant", meta, err)
		}
	}

	cloned := rtr.CloneWithRoutes(map[string]Route{"other": {Variants: []Variant{{Name: "x", Provider: "chat", Weight: 1, Condition: `model == "other"`}}}})
	if len(cloned.conditions) != 4 {
		t.Fatalf("cloned conditions = %d, want 4", len(cloned.conditions))
	}
	rtr.ReplaceRoutes(map[string]Route{})
	if len(rtr.conditions) != 0 {
		t.Fatalf("conditions after ReplaceRoutes = %d, want 0", len(rtr.conditions))
	}
}

func TestRouteTimeoutParsing(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"": 0, "   ": 0, "bogus": 0, "-1s": 0, "0s": 0, "250ms": 250 * time.Millisecond, " 2s ": 2 * time.Second,
	} {
		if got := routeTimeout(value); got != want {
			t.Errorf("routeTimeout(%q) = %v, want %v", value, got, want)
		}
	}
}
