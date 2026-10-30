package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/onboarding"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

func TestDefaultCommandPreservesExplicitServerConfig(t *testing.T) {
	action, code := dispatch(nil, nil, nil, func(name string) string {
		if name == "OMNISWITCH_CONFIG" {
			return "production.yaml"
		}
		return ""
	})
	if action != actionServe || code != 0 {
		t.Fatal("explicit config did not keep direct server behavior")
	}
}

func TestManagedAppKeyHasOnlyDefaultModelAndIsStable(t *testing.T) {
	st := newTestStore(t)
	t.Setenv("OMNISWITCH_SETUP_APP_KEY", "app-private")
	settings := runtimeSettings{authEnabled: true}
	for i := 0; i < 2; i++ {
		if err := ensureManagedAppKey(context.Background(), st, settings); err != nil {
			t.Fatal(err)
		}
	}
	hash := sha256.Sum256([]byte("app-private"))
	key, err := st.GetAPIKeyByHash(context.Background(), hex.EncodeToString(hash[:]))
	if err != nil || key.Role != "viewer" || len(key.AllowedModels) != 1 || key.AllowedModels[0] != "default" {
		t.Fatalf("app identity: %+v/%v", key, err)
	}
	keys, err := st.ListAPIKeys(context.Background())
	if err != nil || len(keys) != 1 {
		t.Fatal("restart duplicated app key")
	}
}

func TestManagedDemoConfigRegistersAndRoutes(t *testing.T) {
	profile, err := onboarding.NewProfile(onboarding.Connection{Provider: "demo", Model: "demo-model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path, err := onboarding.WriteConfig(t.TempDir(), profile, "127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	config, err := gatewayconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	registry := provider.NewRegistry()
	registerProviderAccount(registry, config.Providers[0])
	p, err := registry.Get("@primary")
	if err != nil {
		t.Fatal(err)
	}
	response, meta, err := p.ChatCompletion(context.Background(), provider.ChatRequest{Model: "demo-model", Messages: []provider.Message{{Role: "user", Content: "hello"}}})
	if err != nil || meta.Cost != 0 || !strings.Contains(response.Choices[0].Message.Content, "simulated") {
		t.Fatalf("demo: %+v/%v", response, err)
	}
}

func TestPublicAppShellDoesNotExposeDataOrInference(t *testing.T) {
	server := newSecurityTestServer(t, nil)
	for _, path := range []string{"/", "/playground", "/keys"} {
		rec := doRequest(server, http.MethodGet, path, "", "", nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<html") {
			t.Fatalf("app shell %s: %d", path, rec.Code)
		}
	}
	for _, path := range []string{"/api/keys", "/api/config/raw", "/api/providers", "/api/logs", "/v1/models"} {
		if rec := doRequest(server, http.MethodGet, path, "", "", nil); rec.Code != 401 {
			t.Fatalf("data %s: %d", path, rec.Code)
		}
	}
	if rec := doRequest(server, http.MethodPost, "/v1/chat/completions", "", `{"model":"default","messages":[{"role":"user","content":"hi"}]}`, nil); rec.Code != 401 {
		t.Fatal("unauthenticated inference accepted")
	}
}
