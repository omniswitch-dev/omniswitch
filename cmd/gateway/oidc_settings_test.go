package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadRuntimeSettingsOIDCStaleIfError(t *testing.T) {
	clearGatewayEnv(t)
	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatalf("loadRuntimeSettings() error = %v", err)
	}
	if settings.oidcStaleIfError != 24*time.Hour {
		t.Fatalf("default oidcStaleIfError = %s, want 24h", settings.oidcStaleIfError)
	}

	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte("identity:\n  oidc:\n    stale_if_error: 2h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OMNISWITCH_CONFIG", path)
	settings, err = loadRuntimeSettings()
	if err != nil || settings.oidcStaleIfError != 2*time.Hour {
		t.Fatalf("config oidcStaleIfError = %s, %v; want 2h", settings.oidcStaleIfError, err)
	}

	for value, want := range map[string]time.Duration{"0": 0, "0s": 0, "30m": 30 * time.Minute, "168h": 168 * time.Hour} {
		t.Setenv("OMNISWITCH_OIDC_STALE_IF_ERROR", value)
		settings, err = loadRuntimeSettings()
		if err != nil || settings.oidcStaleIfError != want {
			t.Errorf("OMNISWITCH_OIDC_STALE_IF_ERROR=%s: %s, %v; want %s", value, settings.oidcStaleIfError, err, want)
		}
	}
	for _, value := range []string{"-1m", "169h", "soon"} {
		t.Setenv("OMNISWITCH_OIDC_STALE_IF_ERROR", value)
		if _, err := loadRuntimeSettings(); err == nil || !strings.Contains(err.Error(), "OMNISWITCH_OIDC_STALE_IF_ERROR") {
			t.Errorf("OMNISWITCH_OIDC_STALE_IF_ERROR=%s: error = %v, want startup error", value, err)
		}
	}
}
