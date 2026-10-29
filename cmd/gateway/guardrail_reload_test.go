package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func newReloadTestGateway(t *testing.T, initial *guardrail.Engine) (*router.Router, *gateway.Handler) {
	t.Helper()
	registry := provider.NewRegistry()
	rtr := router.New(registry)
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return rtr, gateway.New(registry, rtr, st, initial)
}

// recordRetired replaces retireGuardrailEngine for one test.
func recordRetired(t *testing.T) func() []*guardrail.Engine {
	t.Helper()
	var mu sync.Mutex
	var retired []*guardrail.Engine
	previous := retireGuardrailEngine
	retireGuardrailEngine = func(engine *guardrail.Engine) {
		mu.Lock()
		defer mu.Unlock()
		retired = append(retired, engine)
	}
	t.Cleanup(func() { retireGuardrailEngine = previous })
	return func() []*guardrail.Engine {
		mu.Lock()
		defer mu.Unlock()
		return append([]*guardrail.Engine(nil), retired...)
	}
}

func TestApplyHotConfigRetiresReplacedEngine(t *testing.T) {
	retired := recordRetired(t)
	initial := guardrail.NewEngine()
	rtr, gw := newReloadTestGateway(t, initial)

	_, first := applyHotConfig(gatewayconfig.Config{}, rtr, gw, runtimeSettings{}, nil)
	_, second := applyHotConfig(gatewayconfig.Config{}, rtr, gw, runtimeSettings{}, nil)

	got := retired()
	if len(got) != 2 || got[0] != initial || got[1] != first {
		t.Fatalf("retired engines = %p, want [initial %p, first reload %p]", got, initial, first)
	}
	if second == first || second == initial {
		t.Fatal("each reload must install a new engine")
	}
}

// TestApplyHotConfigDropsRemovedGuardrails: a reload used to merge the new
// file over the startup guardrail settings, so a rule removed from the file
// kept running until restart whenever the file still set actions.
func TestApplyHotConfigDropsRemovedGuardrails(t *testing.T) {
	recordRetired(t)
	startup := guardrail.Config{
		Actions: map[string]string{"pii": "redact"},
		Rules:   []guardrail.Rule{{Name: "old-rule", Stage: "both", Pattern: "(?i)forbidden", Action: "deny"}},
	}
	rtr, gw := newReloadTestGateway(t, guardrail.NewEngineWithConfig(startup))

	var cfg gatewayconfig.Config
	cfg.Guardrails.Actions = map[string]string{"pii": "block"}
	_, reloaded := applyHotConfig(cfg, rtr, gw, runtimeSettings{guardrailConfig: startup}, nil)

	for _, result := range reloaded.EvaluateInput([]provider.Message{{Role: "user", Content: "this is forbidden"}}) {
		if result.Triggered && result.Type == "old-rule" {
			t.Fatalf("rule removed from the file still triggers after reload: %+v", result)
		}
	}
}

// TestStartupEngineEnforcesToolRulesOnly: the startup engine was built only
// when the file set actions or regex rules, so a file with only tool rules,
// webhooks or LLM judges ran with no guardrails until the first reload.
func TestStartupEngineEnforcesToolRulesOnly(t *testing.T) {
	clearGatewayEnv(t)
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	body := `apiVersion: omniswitch.dev/v1
kind: GatewayConfig
guardrails:
  tool_rules:
    - name: no-shell-rm
      tool_name: '^(shell|exec)$'
      arguments_pattern: 'rm\s+-rf'
      action: deny
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OMNISWITCH_CONFIG", path)
	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatalf("loadRuntimeSettings() error = %v", err)
	}
	engine := guardrail.NewEngineWithConfig(settings.guardrailConfig)
	defer engine.Close()

	message := provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{
		ID: "call_1", Type: "function",
		Function: provider.FunctionCall{Name: "shell", Arguments: `{"cmd":"rm -rf /"}`},
	}}}
	for _, result := range engine.EvaluateOutputMessageContext(context.Background(), message) {
		if result.Triggered && result.Type == "tool:no-shell-rm" {
			return
		}
	}
	t.Fatal("tool rule from a tool-rules-only config did not trigger at startup")
}
