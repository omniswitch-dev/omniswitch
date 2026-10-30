package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
)

func TestProfilePersistenceAndConfigHaveSeparateCredentials(t *testing.T) {
	dir := t.TempDir()
	profile, err := NewProfile(Connection{Provider: "openai", Key: "provider-private", Model: "test-model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if profile.AppKey == profile.GatewayKey || profile.VaultKey == profile.AppKey {
		t.Fatal("credential separation failed")
	}
	if err := Save(dir, profile); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil || loaded.AppKey != profile.AppKey || loaded.Connection.Key != "provider-private" {
		t.Fatalf("profile: %+v/%v", loaded, err)
	}
	path, err := WriteConfig(dir, profile, "127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{profile.GatewayKey, profile.AppKey, profile.VaultKey, "provider-private"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("credential leaked into config")
		}
	}
	cfg, err := gatewayconfig.LoadFile(path)
	if err != nil || cfg.Gateway.Auth == nil || !*cfg.Gateway.Auth || cfg.Routes["default"].Variants[0].Model != "test-model" {
		t.Fatalf("config: %+v/%v", cfg, err)
	}
	changed, err := NewProfile(Connection{Provider: "demo", Model: "demo-model"}, loaded)
	if err != nil || changed.GatewayKey != profile.GatewayKey || changed.AppKey != profile.AppKey || changed.VaultKey != profile.VaultKey {
		t.Fatal("reconnecting rotated local identities")
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{"profile.json", "gateway.json"} {
			info, _ := os.Stat(filepath.Join(dir, name))
			if info.Mode().Perm() != 0600 {
				t.Fatalf("%s permissions %v", name, info.Mode())
			}
		}
	}
}

func TestSaveRefusesSymlinkAndLeavesOtherFileUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privilege varies on Windows")
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "profile.json")); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, Profile{}); err == nil {
		t.Fatal("followed profile symlink")
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("loaded symlink")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "untouched" {
		t.Fatal("changed another file")
	}
}

func TestSetupGuardsAndCredentialHandling(t *testing.T) {
	const origin = "http://127.0.0.1:8080"
	dir := t.TempDir()
	wizard, err := NewServer(dir, origin, nil, func(name string) string {
		if name == "OPENAI_API_KEY" {
			return "detected-private"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int32
	wizard.Check = func(_ context.Context, c Connection) (Connection, error) {
		checks.Add(1)
		if c.Key != "detected-private" {
			t.Errorf("wrong key: %q", c.Key)
		}
		c.Model = "available-model"
		return c, nil
	}
	call := func(method, path, token, originHeader, host, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		req.Host = host
		req.Header.Set("Authorization", "Bearer "+token)
		if originHeader != "" {
			req.Header.Set("Origin", originHeader)
		}
		rec := httptest.NewRecorder()
		wizard.ServeHTTP(rec, req)
		return rec
	}
	for _, tc := range []struct {
		method, path, token, originHeader, host, body string
		want                                          int
	}{
		{"GET", "/api/setup", "wrong", "", "127.0.0.1:8080", "", 401},
		{"GET", "/api/setup", wizard.Token, "", "attacker.example", "", 403},
		{"POST", "/api/connect", wizard.Token, "https://attacker.example", "127.0.0.1:8080", `{"provider":"openai"}`, 403},
		{"POST", "/api/connect", wizard.Token, "", "127.0.0.1:8080", `{"provider":"openai"}`, 403},
		{"POST", "/api/connect", wizard.Token, origin, "127.0.0.1:8080", `{"provider":"unknown"}`, 400},
		{"POST", "/api/connect", wizard.Token, origin, "127.0.0.1:8080", `{"provider":"openai","unexpected":true}`, 400},
		{"POST", "/api/connect", wizard.Token, origin, "127.0.0.1:8080", strings.Repeat("x", 20000), 400},
		{"GET", "/profile.json", "", "", "127.0.0.1:8080", "", 404},
		{"GET", "/api/other", wizard.Token, "", "127.0.0.1:8080", "", 404},
	} {
		if rec := call(tc.method, tc.path, tc.token, tc.originHeader, tc.host, tc.body); rec.Code != tc.want {
			t.Fatalf("%+v: %d/%s", tc, rec.Code, rec.Body)
		}
	}
	if checks.Load() != 0 {
		t.Fatal("invalid request reached checker")
	}
	rec := call("GET", "/api/setup", wizard.Token, "", "127.0.0.1:8080", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "detected-private") || !strings.Contains(rec.Body.String(), `"detected":true`) {
		t.Fatalf("status exposed secret: %d/%s", rec.Code, rec.Body)
	}
	rec = call("POST", "/api/connect", wizard.Token, origin, "127.0.0.1:8080", `{"provider":"openai","use_env":true}`)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "detected-private") {
		t.Fatalf("connection: %d/%s", rec.Code, rec.Body)
	}
	var ready map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ready); err != nil {
		t.Fatal(err)
	}
	if ready["app_key"] == ready["gateway_key"] {
		t.Fatal("application got owner key")
	}

	select {
	case p := <-wizard.Done:
		if p.Connection.Key != "detected-private" {
			t.Fatal("profile lost provider")
		}
	default:
		t.Fatal("setup did not finish")
	}
	if rec := call("POST", "/api/connect", wizard.Token, origin, "127.0.0.1:8080", `{"provider":"openai","use_env":true}`); rec.Code != 409 {
		t.Fatal("repeated write accepted")
	}
	loaded, err := Load(dir)
	if err != nil || loaded.Listen != "127.0.0.1:8080" {
		t.Fatalf("saved profile: %+v/%v", loaded, err)
	}
}

func TestProbeDiscoversModelsWithoutInferenceOrSecretErrors(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/models" {
			posts.Add(1)
			http.Error(w, "unexpected", 500)
			return
		}
		if r.Header.Get("Authorization") != "Bearer local-key" {
			http.Error(w, "echoed-private-key", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
	}))
	defer server.Close()
	c, err := Probe(context.Background(), Connection{Provider: "local", Key: "local-key", BaseURL: server.URL + "/v1"})
	if err != nil || c.Model != "local-model" || posts.Load() != 0 {
		t.Fatalf("probe: %+v/%v", c, err)
	}
	_, err = Probe(context.Background(), Connection{Provider: "local", Key: "wrong", BaseURL: server.URL + "/v1"})
	if err == nil || strings.Contains(err.Error(), "echoed-private-key") {
		t.Fatal("upstream secret was returned")
	}
	_, err = Probe(context.Background(), Connection{Provider: "local", Key: "local-key", BaseURL: server.URL + "/v1", Model: "missing"})
	if err == nil {
		t.Fatal("unknown requested model accepted")
	}
	for _, base := range []string{"file:///etc/passwd", "https://user:secret@example.com", "http://example.com?key=secret"} {
		if _, err := Probe(context.Background(), Connection{Provider: "local", BaseURL: base}); err == nil {
			t.Fatalf("invalid base %s", base)
		}
	}
	demo, err := Probe(context.Background(), Connection{Provider: "demo", Key: "discard-me"})
	if err != nil || demo.Key != "" || demo.Model != "demo-model" {
		t.Fatal("invalid demo")
	}
}

func TestProbeDoesNotFollowRedirects(t *testing.T) {
	var destination atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destination.Store(true) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	if _, err := Probe(context.Background(), Connection{Provider: "local", Key: "private", BaseURL: source.URL}); err == nil || destination.Load() {
		t.Fatal("followed a credential-bearing redirect")
	}
}
