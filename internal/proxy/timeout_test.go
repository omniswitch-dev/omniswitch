package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTargetTimeoutBoundsResponseHeaders(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(hung.Close)
	t.Cleanup(func() { close(release) })
	h, err := NewMultiHandler(defaultEngine(t), nil, []TargetConfig{{
		Name: "default", Upstream: hung.URL, Timeout: 100 * time.Millisecond,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	start := time.Now()
	rec := serve(h, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("call took %s, want the 100ms target timeout to stop it", elapsed)
	}
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "upstream sent no response headers within 100ms") {
		t.Fatalf("status %d body %s, want 502 with the header timeout", rec.Code, rec.Body.String())
	}

	// A notification gets the same bound and no JSON-RPC response id.
	rec = serve(h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "no response headers") {
		t.Fatalf("notification status %d body %s, want 502 with the header timeout", rec.Code, rec.Body.String())
	}
}

func TestTargetTimeoutDoesNotCutEventStreams(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"))
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"done\":true}}\n\n"))
	}))
	t.Cleanup(upstream.Close)
	h, err := NewMultiHandler(defaultEngine(t), nil, []TargetConfig{{
		Name: "default", Upstream: upstream.URL, Timeout: 100 * time.Millisecond,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	rec := serve(h, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"done":true`) {
		t.Fatalf("status %d body %s, want the whole event stream", rec.Code, rec.Body.String())
	}
}

func TestSharedTransportAllowsTheLongestTargetTimeout(t *testing.T) {
	for _, tc := range []struct {
		timeouts []time.Duration
		want     time.Duration
	}{
		{nil, defaultCallTimeout},
		{[]time.Duration{30 * time.Second}, defaultCallTimeout},
		{[]time.Duration{30 * time.Second, 10 * time.Minute}, 10 * time.Minute},
	} {
		configs := []TargetConfig{{Name: "default", Upstream: "http://127.0.0.1:1/mcp"}}
		for i, timeout := range tc.timeouts {
			configs = append(configs, TargetConfig{Name: "t" + string(rune('a'+i)), Upstream: "http://127.0.0.1:1/mcp", Timeout: timeout})
		}
		h, err := NewMultiHandler(defaultEngine(t), nil, configs)
		if err != nil {
			t.Fatal(err)
		}
		if got := h.client.Transport.(*http.Transport).ResponseHeaderTimeout; got != tc.want {
			t.Errorf("timeouts %v: ResponseHeaderTimeout = %s, want %s", tc.timeouts, got, tc.want)
		}
		h.Close()
	}
}
