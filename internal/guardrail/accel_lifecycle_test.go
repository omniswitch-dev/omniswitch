package guardrail

import (
	"context"
	"testing"

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
