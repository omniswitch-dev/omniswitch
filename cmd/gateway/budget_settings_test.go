package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBudgetSettings(t *testing.T) {
	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.budgetFailOpen || settings.budgetRefreshInterval != 5*time.Second {
		t.Fatalf("defaults = %v/%v, want fail closed and 5s", settings.budgetFailOpen, settings.budgetRefreshInterval)
	}

	path := filepath.Join(t.TempDir(), "omniswitch.yaml")
	if err := os.WriteFile(path, []byte("gateway:\n  budget_fail_open: true\n  budget_refresh_interval: 2s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OMNISWITCH_CONFIG", path)
	settings, err = loadRuntimeSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !settings.budgetFailOpen || settings.budgetRefreshInterval != 2*time.Second {
		t.Fatalf("config = %v/%v, want fail open and 2s", settings.budgetFailOpen, settings.budgetRefreshInterval)
	}

	t.Setenv("OMNISWITCH_BUDGET_FAIL_OPEN", "false")
	t.Setenv("OMNISWITCH_BUDGET_REFRESH_INTERVAL", "750ms")
	settings, err = loadRuntimeSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.budgetFailOpen || settings.budgetRefreshInterval != 750*time.Millisecond {
		t.Fatalf("env = %v/%v, want env to override the file", settings.budgetFailOpen, settings.budgetRefreshInterval)
	}

	for key, value := range map[string]string{"OMNISWITCH_BUDGET_FAIL_OPEN": "maybe", "OMNISWITCH_BUDGET_REFRESH_INTERVAL": "1h"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := loadRuntimeSettings(); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("loadRuntimeSettings() error = %v, want %s rejected", err, key)
			}
		})
	}
}
