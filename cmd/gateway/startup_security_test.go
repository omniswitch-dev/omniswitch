package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnauthenticatedGatewayDefaultsToLoopback(t *testing.T) {
	clearGatewayEnv(t)
	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.authEnabled || settings.listenAddr != "127.0.0.1:8080" {
		t.Fatalf("auth/listen = %v/%q, want false/127.0.0.1:8080", settings.authEnabled, settings.listenAddr)
	}
	if warning := insecureListenWarning(settings); warning != "" {
		t.Fatalf("unexpected warning on loopback: %s", warning)
	}
}

func TestAuthenticatedGatewayKeepsAllInterfacesDefault(t *testing.T) {
	clearGatewayEnv(t)
	t.Setenv("OMNISWITCH_AUTH", "true")
	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.listenAddr != ":8080" {
		t.Fatalf("listen = %q, want :8080", settings.listenAddr)
	}
}

func TestExplicitListenWithoutAuthWarns(t *testing.T) {
	clearGatewayEnv(t)
	t.Setenv("OMNISWITCH_LISTEN", ":9090")
	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.listenAddr != ":9090" {
		t.Fatalf("listen = %q, want :9090", settings.listenAddr)
	}
	if warning := insecureListenWarning(settings); !strings.Contains(warning, "WARNING") || !strings.Contains(warning, ":9090") {
		t.Fatalf("warning = %q", warning)
	}
}

func TestConfigListenCountsAsExplicit(t *testing.T) {
	clearGatewayEnv(t)
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte("gateway:\n  listen: \"0.0.0.0:7070\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OMNISWITCH_CONFIG", path)
	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.listenAddr != "0.0.0.0:7070" || insecureListenWarning(settings) == "" {
		t.Fatalf("listen = %q, warning %q", settings.listenAddr, insecureListenWarning(settings))
	}
}

func TestDisplayBaseURL(t *testing.T) {
	for addr, want := range map[string]string{
		":8080":          "http://localhost:8080",
		"0.0.0.0:8080":   "http://localhost:8080",
		"127.0.0.1:8080": "http://127.0.0.1:8080",
		"[::]:8080":      "http://localhost:8080",
		"gw.local:443":   "http://gw.local:443",
		"[::1]:8080":     "http://[::1]:8080",
	} {
		if got := displayBaseURL(addr); got != want {
			t.Errorf("displayBaseURL(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestPublicMetricsBypassesAuthOnlyWhenEnabled(t *testing.T) {
	private := newSecurityTestServer(t, nil)
	if rec := doRequest(private, http.MethodGet, "/metrics", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("private /metrics without key = %d, want 401", rec.Code)
	}
	public := newSecurityTestServer(t, func(s *runtimeSettings) { s.metricsPublic = true })
	if rec := doRequest(public, http.MethodGet, "/metrics", "", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("public /metrics = %d, want 200", rec.Code)
	}
	if rec := doRequest(public, http.MethodGet, "/api/logs", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("/api/logs with public metrics = %d, want 401", rec.Code)
	}
}

func TestMetricsPublicEnv(t *testing.T) {
	clearGatewayEnv(t)
	t.Setenv("OMNISWITCH_METRICS_PUBLIC", "yes")
	settings, err := loadRuntimeSettings()
	if err != nil || !settings.metricsPublic {
		t.Fatalf("metricsPublic = %v, err %v", settings.metricsPublic, err)
	}
}

func TestResolveStorageDSN(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		settings runtimeSettings
		want     string
		wantErr  string
	}{
		{name: "sqlite default"},
		{name: "env postgres", env: "postgres://u@db/x", want: "postgres://u@db/x"},
		{name: "env wins", env: "postgresql://a/b", settings: runtimeSettings{storageURL: "postgres://c/d"}, want: "postgresql://a/b"},
		{name: "config url", settings: runtimeSettings{storageURL: "postgres://c/d"}, want: "postgres://c/d"},
		{name: "typo env", env: "postgress://db/x", wantErr: "OMNISWITCH_DATABASE_URL"},
		{name: "libpq form", env: "host=db dbname=x", wantErr: "OMNISWITCH_DATABASE_URL"},
		{name: "postgres without dsn", settings: runtimeSettings{storageBackend: "postgres"}, wantErr: "gateway.storage"},
		{name: "sqlite backend", settings: runtimeSettings{storageBackend: "sqlite"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveStorageDSN(tc.env, tc.settings)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want mention of %s", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "dbname") {
					t.Fatalf("error echoes the DSN: %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
