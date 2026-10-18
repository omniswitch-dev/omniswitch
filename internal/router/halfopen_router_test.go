package router

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

// recoveringProvider fails while failing is set. Otherwise each call
// signals entered and then waits for release (or for its context to end).
type recoveringProvider struct {
	name    string
	failing atomic.Bool
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func newRecoveringProvider(name string) *recoveringProvider {
	p := &recoveringProvider{name: name, entered: make(chan struct{}, 16), release: make(chan struct{})}
	p.failing.Store(true)
	return p
}

func (p *recoveringProvider) Name() string { return p.name }

func (p *recoveringProvider) Models() []provider.ModelInfo {
	return []provider.ModelInfo{{ID: "m", Object: "model", OwnedBy: p.name, Provider: p.name}}
}

func (p *recoveringProvider) wait(ctx context.Context) error {
	p.calls.Add(1)
	if p.failing.Load() {
		return errors.New("upstream unavailable")
	}
	p.entered <- struct{}{}
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *recoveringProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, provider.ProviderMeta, error) {
	meta := provider.ProviderMeta{Provider: p.name, Model: req.Model}
	if err := p.wait(ctx); err != nil {
		return provider.ChatResponse{}, meta, err
	}
	return provider.ChatResponse{Model: req.Model, Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "ok"}}}}, meta, nil
}

func (p *recoveringProvider) ChatCompletionStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatResponseChunk, provider.ProviderMeta, error) {
	meta := provider.ProviderMeta{Provider: p.name, Model: req.Model}
	out := make(chan provider.ChatResponseChunk, 1)
	go func() {
		defer close(out)
		if err := p.wait(ctx); err != nil {
			out <- provider.ChatResponseChunk{Err: err}
			return
		}
		out <- provider.ChatResponseChunk{Model: req.Model, Choices: []provider.ChunkChoice{{Delta: provider.Message{Content: "ok"}}}}
	}()
	return out, meta, nil
}

func waitEntered(t *testing.T, p *recoveringProvider) {
	t.Helper()
	select {
	case <-p.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("probe request did not reach the recovering provider")
	}
}

func halfOpenRouter(t *testing.T) (*Router, *recoveringProvider, *fakeClock) {
	t.Helper()
	primary := newRecoveringProvider("a")
	registry := provider.NewRegistry()
	registry.Register(primary)
	registry.Register(&fakeProvider{name: "b", model: "m"})
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	breaker := NewCircuitBreaker(1, time.Minute)
	breaker.now = clock.Now
	rtr := New(registry)
	rtr.SetCircuitBreaker(breaker)
	rtr.SetRoute("m", Route{Provider: "a", Fallbacks: []string{"b"}})
	// The first request fails on a and opens its circuit.
	if _, meta, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "m"}, ""); err != nil || meta.Provider != "b" {
		t.Fatalf("opening request: provider=%q err=%v, want fallback to b", meta.Provider, err)
	}
	if state := breaker.State("a"); state != CircuitOpen {
		t.Fatalf("state = %s, want open", state)
	}
	primary.failing.Store(false)
	clock.Advance(time.Minute)
	return rtr, primary, clock
}

func TestRouterHalfOpenSendsOneProbeAndFallsBackForOthers(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			rtr, primary, _ := halfOpenRouter(t)
			callsBefore := primary.calls.Load()
			run := func() (string, error) {
				if stream {
					chunks, meta, err := rtr.ExecuteStream(context.Background(), provider.ChatRequest{Model: "m"}, "")
					if err != nil {
						return "", err
					}
					for range chunks {
					}
					return meta.Provider, nil
				}
				_, meta, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "m"}, "")
				return meta.Provider, err
			}

			probeDone := make(chan string, 1)
			go func() {
				prov, err := run()
				if err != nil {
					prov = "error: " + err.Error()
				}
				probeDone <- prov
			}()
			waitEntered(t, primary)

			const concurrent = 20
			var wg sync.WaitGroup
			var fellBack atomic.Int32
			for i := 0; i < concurrent; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if prov, err := run(); err == nil && prov == "b" {
						fellBack.Add(1)
					}
				}()
			}
			wg.Wait()
			if got := fellBack.Load(); got != concurrent {
				t.Fatalf("%d of %d concurrent requests fell back to b, want all", got, concurrent)
			}
			if got := primary.calls.Load() - callsBefore; got != 1 {
				t.Fatalf("recovering provider got %d calls while the probe was in flight, want 1", got)
			}

			close(primary.release)
			if prov := <-probeDone; prov != "a" {
				t.Fatalf("probe served by %q, want a", prov)
			}
			if state := rtr.CircuitBreaker().State("a"); state != CircuitClosed {
				t.Fatalf("state = %s after a successful probe, want closed", state)
			}
		})
	}
}

func TestRouterCancelledProbeDoesNotBlockProvider(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			rtr, primary, _ := halfOpenRouter(t)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				var err error
				if stream {
					_, _, err = rtr.ExecuteStream(ctx, provider.ChatRequest{Model: "m"}, "")
				} else {
					_, _, err = rtr.Execute(ctx, provider.ChatRequest{Model: "m"}, "")
				}
				done <- err
			}()
			waitEntered(t, primary)
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled probe error = %v, want context.Canceled", err)
			}
			breaker := rtr.CircuitBreaker()
			if state := breaker.State("a"); state != CircuitHalfOpen {
				t.Fatalf("state = %s after a cancelled probe, want half-open (unchanged)", state)
			}
			if !breaker.Available("a") {
				t.Fatal("a cancelled probe left the provider blocked")
			}

			// The next request becomes the probe and closes the circuit.
			close(primary.release)
			_, meta, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "m"}, "")
			if err != nil || meta.Provider != "a" {
				t.Fatalf("next request: provider=%q err=%v, want a", meta.Provider, err)
			}
			if state := breaker.State("a"); state != CircuitClosed {
				t.Fatalf("state = %s, want closed", state)
			}
		})
	}
}

func TestRouterRetriesStopWhenProbeFails(t *testing.T) {
	rtr, primary, _ := halfOpenRouter(t)
	primary.failing.Store(true)
	rtr.SetRoute("m", Route{Provider: "a", Fallbacks: []string{"b"}, MaxRetries: 3, RetryBackoff: "1ms"})
	before := primary.calls.Load()
	_, meta, err := rtr.Execute(context.Background(), provider.ChatRequest{Model: "m"}, "")
	if err != nil || meta.Provider != "b" {
		t.Fatalf("provider=%q err=%v, want fallback to b", meta.Provider, err)
	}
	if got := primary.calls.Load() - before; got != 1 {
		t.Fatalf("failed probe was retried: %d calls, want 1", got)
	}
	if state := rtr.CircuitBreaker().State("a"); state != CircuitOpen {
		t.Fatalf("state = %s, want open after the failed probe", state)
	}
}
