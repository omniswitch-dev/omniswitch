package main

import (
	"os"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/gateway"
	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

func TestApplyHotConfigSwapsRoutesAndGuardrails(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	registry := provider.NewRegistry()
	registerEnvProvider(registry, "openai", "OPENAI_API_KEY")
	rtr := router.New(registry)
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	baseRules := guardrail.Config{Actions: map[string]string{"pii": "redact"}}
	gr := guardrail.NewEngineWithConfig(baseRules)
	gw := gateway.New(registry, rtr, st, gr)

	base := runtimeSettings{
		routes: map[string]router.Route{
			"old-model": {Provider: "@openai"},
		},
		guardrailConfig: baseRules,
	}

	cfg := gatewayconfig.Config{
		Routes: map[string]router.Route{
			"new-model": {
				Provider: "@openai",
				Variants: []router.Variant{{Name: "v1", Provider: "@openai", Model: "@openai/gpt-4o", Weight: 100}},
			},
		},
	}
	cfg.Guardrails.Rules = []gatewayconfig.GuardrailRule{{
		Name: "block-secret", Stage: "both", Pattern: "(?i)top-secret", Action: "deny",
	}}

	// old-model was defined by the previous version of the file.
	prev := map[string]router.Route{"old-model": {Provider: "@openai"}}
	_, gr2 := applyHotConfig(cfg, rtr, gw, base, prev)

	if _, ok := rtr.RouteFor("new-model"); !ok {
		t.Fatalf("expected new-model route after reload")
	}
	if _, ok := rtr.RouteFor("old-model"); ok {
		t.Fatalf("expected old-model route removed after reload")
	}
	messages := []provider.Message{{Role: "user", Content: "this is top-secret material"}}
	results := gr2.EvaluateInput(messages)
	for _, r := range results {
		if r.Triggered && r.Type == "block-secret" {
			return
		}
	}
	t.Fatalf("expected reloaded guardrail to trigger on blocked pattern, got %+v", results)
}

func TestStartConfigReloaderAppliesFileChanges(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	dir := t.TempDir()
	path := filepath_join(dir, "gateway-config.yaml")

	yamlBody := `apiVersion: omniswitch.dev/v1
kind: GatewayConfig
routes:
  logical-a:
    provider: "@openai"
    variants:
      - name: v1
        provider: "@openai"
        model: "@openai/gpt-4o-mini"
        weight: 100
`
	if err := os.WriteFile(path, []byte(yamlBody), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	registry := provider.NewRegistry()
	rtr := router.New(registry)
	st, _ := store.New(dir)
	defer st.Close()
	gw := gateway.New(registry, rtr, st, nil)

	startConfigReloader(path, rtr, gw, settings)

	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := rtr.RouteFor("logical-a"); ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("reloader did not apply route within deadline")
}

func filepath_join(parts ...string) string {
	return parts[0] + string(os.PathSeparator) + parts[1]
}
