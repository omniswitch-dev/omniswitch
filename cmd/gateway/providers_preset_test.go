package main

import (
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

func TestPresetDoesNotReplaceConfigAccount(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "preset-key")
	t.Setenv("DEEPSEEK_EU_KEY", "account-key")
	accounts := []gatewayconfig.ProviderAccount{{
		Name:      "deepseek-eu",
		Type:      "deepseek",
		BaseURL:   "https://deepseek-proxy.example.eu/v1",
		APIKeyEnv: "DEEPSEEK_EU_KEY",
	}}
	registry := provider.NewRegistry()
	for _, account := range accounts {
		registerProviderAccount(registry, account)
	}
	registerOpenAICompatiblePresets(registry, accounts)

	if _, err := registry.Get("deepseek"); err == nil {
		t.Fatal("deepseek preset was registered although a deepseek config account exists")
	}
	p, err := registry.ResolveModel("deepseek-chat")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "deepseek-eu" {
		t.Fatalf("deepseek-chat resolves to %q, want config account deepseek-eu", p.Name())
	}
}

func TestPresetSkippedWhenNameTaken(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "preset-key")
	t.Setenv("MY_MISTRAL_KEY", "account-key")
	accounts := []gatewayconfig.ProviderAccount{{
		Name:      "mistral",
		Type:      "custom",
		BaseURL:   "https://mistral-proxy.internal/v1",
		APIKeyEnv: "MY_MISTRAL_KEY",
		Models:    []string{"mistral-large-latest"},
	}}
	registry := provider.NewRegistry()
	registerProviderAccount(registry, accounts[0])
	before, err := registry.Get("mistral")
	if err != nil {
		t.Fatal(err)
	}
	registerOpenAICompatiblePresets(registry, accounts)
	after, _ := registry.Get("mistral")
	if before != after {
		t.Fatal("mistral preset replaced the configured mistral account")
	}
}

func TestPresetRegistersWithoutConflict(t *testing.T) {
	t.Setenv("XAI_API_KEY", "preset-key")
	registry := provider.NewRegistry()
	registerOpenAICompatiblePresets(registry, nil)
	if _, err := registry.Get("xai"); err != nil {
		t.Fatalf("xai preset not registered: %v", err)
	}
}
