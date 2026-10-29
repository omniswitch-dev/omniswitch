package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnv(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		value    string
		fallback string
		want     string
	}{
		{name: "uses fallback", key: "OMNISWITCH_TEST_EMPTY", fallback: ":8080", want: ":8080"},
		{name: "uses value", key: "OMNISWITCH_TEST_VALUE", value: ":9090", fallback: ":8080", want: ":9090"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if got := env(tt.key, tt.fallback); got != tt.want {
				t.Fatalf("env() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripIdentityHeaders(t *testing.T) {
	var seen http.Header
	handler := stripIdentityHeaders(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-Omniswitch-Key-Id", "admin")
	req.Header.Set("x-omniswitch-role", "owner")
	req.Header.Set("X-OMNISWITCH-AUTH-METHOD", "oidc")
	req.Header.Set("Mcp-Session-Id", "s1")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	for key := range seen {
		if strings.HasPrefix(strings.ToLower(key), "x-omniswitch-") {
			t.Fatalf("identity header %q reached the handler", key)
		}
	}
	if seen.Get("Mcp-Session-Id") != "s1" {
		t.Fatalf("unrelated header was stripped")
	}
}

func TestNewServerDefaults(t *testing.T) {
	server := newServer(defaultListenAddr, http.NotFoundHandler())
	if !strings.HasPrefix(server.Addr, "127.0.0.1:") {
		t.Fatalf("default listen address %q is not loopback", server.Addr)
	}
	if server.ReadHeaderTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("server timeouts not set: %+v", server)
	}
}
