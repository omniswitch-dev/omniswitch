package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamOutlivesNonStreamingTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "data: {\"id\":\"c\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"t%d\"}}]}\n\n", i)
			flusher.Flush()
			time.Sleep(60 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	// The overall timeout is shorter than the stream (5 x 60ms).
	p := NewCustom("local", server.URL, "", WithCustomTimeout(100*time.Millisecond))
	chunks, _, err := p.ChatCompletionStream(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error after %q: %v", text.String(), chunk.Err)
		}
		for _, choice := range chunk.Choices {
			text.WriteString(choice.Delta.Content)
		}
	}
	if text.String() != "t0t1t2t3t4" {
		t.Fatalf("streamed text = %q, want t0..t4", text.String())
	}
}

func TestNonStreamingRequestKeepsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer server.Close()
	p := NewCustom("local", server.URL, "", WithCustomTimeout(50*time.Millisecond))
	if _, _, err := p.ChatCompletion(context.Background(), ChatRequest{Model: "m"}); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestUpstreamErrorTypedAndBounded(t *testing.T) {
	huge := strings.Repeat("x", 1<<20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, huge)
	}))
	defer server.Close()

	o := NewOpenAI("k")
	o.baseURL = server.URL
	_, meta, err := o.ChatCompletion(context.Background(), ChatRequest{Model: "gpt-4o-mini"})
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("error %T %v is not *UpstreamError", err, err)
	}
	if upstream.StatusCode != 429 || upstream.RetryAfter != 7*time.Second || upstream.Provider != "openai" {
		t.Fatalf("upstream = %+v", upstream)
	}
	if !strings.HasPrefix(err.Error(), "openai error (status 429): xxx") {
		t.Fatalf("error text changed: %.60s", err.Error())
	}
	if !upstream.Truncated || len(upstream.Body) > maxErrorBodyBytes+32 || len(meta.Error) > maxErrorBodyBytes+32 {
		t.Fatalf("error body not bounded: body=%d meta=%d", len(upstream.Body), len(meta.Error))
	}
}

func TestStreamUpstreamErrorTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After-Ms", "1500")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"busy"}`)
	}))
	defer server.Close()
	p := NewCustom("local", server.URL, "")
	_, _, err := p.ChatCompletionStream(context.Background(), ChatRequest{Model: "m"})
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.StatusCode != 503 || upstream.RetryAfter != 1500*time.Millisecond {
		t.Fatalf("err = %v (%+v)", err, upstream)
	}
	if err.Error() != `local stream error (status 503): {"error":"busy"}` {
		t.Fatalf("error text = %q", err.Error())
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{"": 0, "3": 3 * time.Second, "-1": 0, "bogus": 0,
		now.Add(10 * time.Second).Format(http.TimeFormat): 10 * time.Second}
	for value, want := range cases {
		h := http.Header{}
		if value != "" {
			h.Set("Retry-After", value)
		}
		if got := parseRetryAfter(h, now); got != want {
			t.Errorf("Retry-After %q = %v, want %v", value, got, want)
		}
	}
}

func TestReadLimitedRejectsOversizedBody(t *testing.T) {
	if _, err := readLimited(strings.NewReader("abcdef"), 5); !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if data, err := readLimited(strings.NewReader("abcde"), 5); err != nil || string(data) != "abcde" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}
