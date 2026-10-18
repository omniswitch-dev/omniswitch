package guardrail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/accel"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

func triggeredTypes(results []Result) map[string]bool {
	types := map[string]bool{}
	for _, r := range results {
		types[r.Type] = true
	}
	return types
}

func TestNewlinePatternDoesNotShiftRuleIndexes(t *testing.T) {
	e := NewEngineWithConfig(Config{Rules: []Rule{
		{Name: "nl", Pattern: "zzz\n", Stage: "both", Action: "warn"},
		{Name: "second", Pattern: "qqq", Stage: "both"},
		{Name: "crlf", Pattern: "a\r\nb", Stage: "both"},
		{Name: "third", Pattern: "www", Stage: "input"},
	}})
	defer e.Close()

	got := triggeredTypes(e.EvaluateInputContext(context.Background(), []provider.Message{{Role: "user", Content: "qqq www"}}))
	if !got["second"] || !got["third"] || got["nl"] || got["crlf"] {
		t.Fatalf("triggered = %v, want second and third only", got)
	}
	got = triggeredTypes(e.EvaluateOutputContext(context.Background(), "line zzz\nnext"))
	if !got["nl"] || got["second"] {
		t.Fatalf("triggered = %v, want nl via the Go path", got)
	}
}

func TestEngineCloseIsIdempotentAndKeepsEvaluating(t *testing.T) {
	e := NewEngineWithConfig(Config{Rules: []Rule{
		{Name: "secret", Pattern: `sk-[a-z0-9]{8}`, Stage: "both"},
		{Name: "outonly", Pattern: `forbidden`, Stage: "output"},
	}})
	if accel.Available() && e.accelInput == nil {
		t.Fatal("expected accelerator scanners to be built")
	}
	before := triggeredTypes(e.EvaluateOutputContext(context.Background(), "token sk-abcdef12 forbidden"))
	if err := e.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	after := triggeredTypes(e.EvaluateOutputContext(context.Background(), "token sk-abcdef12 forbidden"))
	for _, name := range []string{"secret", "outonly"} {
		if !before[name] || !after[name] {
			t.Fatalf("rule %q before=%v after=%v; want it to trigger before and after Close", name, before, after)
		}
	}
	if got := triggeredTypes(e.EvaluateInputContext(context.Background(), []provider.Message{{Role: "user", Content: "forbidden"}})); got["outonly"] {
		t.Fatalf("output-only rule fired on input after Close: %v", got)
	}
	var nilEngine *Engine
	if err := nilEngine.Close(); err != nil {
		t.Fatalf("nil Close() error = %v", err)
	}
	if err := NewEngine().Close(); err != nil {
		t.Fatalf("Close() without rules error = %v", err)
	}
}

func TestRedactTransportError(t *testing.T) {
	secretURL := "https://hooks.example.com/services/T000/B000/SECRETTOKEN?key=abc"
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "plain", err: errors.New("unexpected status 502 Bad Gateway"), want: "unexpected status 502 Bad Gateway"},
		{name: "dial", err: &url.Error{Op: "Post", URL: secretURL, Err: errors.New("dial tcp: connection refused")}, want: "request failed: dial tcp: connection refused"},
		{name: "inner repeats url", err: &url.Error{Op: "Post", URL: secretURL, Err: fmt.Errorf("bad %s", secretURL)}, want: "request failed: bad [redacted]"},
		{name: "canceled", err: &url.Error{Op: "Post", URL: secretURL, Err: context.Canceled}, want: "request canceled"},
		{name: "nil", err: nil, want: ""},
	}
	for _, tt := range tests {
		if got := redactTransportError(tt.err); got != tt.want {
			t.Errorf("%s: redactTransportError() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestWebhookTransportErrorDetailsHideURL(t *testing.T) {
	// A closed local port gives a fast transport error.
	secretURL := "http://127.0.0.1:1/hook/SECRETTOKEN?api_key=topsecret"
	e := NewEngineWithConfig(Config{Webhooks: []Webhook{{Name: "external", URL: secretURL, Stage: "input", Timeout: 2 * time.Second}}})
	defer e.Close()
	e.webhooks[0].client = &http.Client{Timeout: 2 * time.Second}
	results := e.EvaluateInputContext(context.Background(), []provider.Message{{Role: "user", Content: "hello"}})
	var found bool
	for _, r := range results {
		if r.Type != "webhook:external" {
			continue
		}
		found = true
		if strings.Contains(r.Details, "SECRETTOKEN") || strings.Contains(r.Details, "topsecret") || strings.Contains(r.Details, "/hook/") {
			t.Fatalf("Details leaks the webhook URL: %q", r.Details)
		}
		if r.Details == "" {
			t.Fatal("Details is empty, want a redacted reason")
		}
	}
	if !found {
		t.Fatalf("results = %+v, want a fail-closed webhook result", results)
	}
}
