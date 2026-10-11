package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDispatchNoArgsServes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	action, code := dispatch(nil, &stdout, &stderr, os.Getenv)
	if action != actionServe {
		t.Fatalf("dispatch(nil) action = %v, want actionServe", action)
	}
	if code != 0 {
		t.Fatalf("dispatch(nil) code = %d, want 0", code)
	}
}

func TestDispatchServe(t *testing.T) {
	var stdout, stderr bytes.Buffer
	action, code := dispatch([]string{"serve"}, &stdout, &stderr, os.Getenv)
	if action != actionServe || code != 0 {
		t.Fatalf("dispatch(serve) = (%v, %d), want (actionServe, 0)", action, code)
	}
}

func TestDispatchServeRejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	action, code := dispatch([]string{"serve", "extra"}, &stdout, &stderr, os.Getenv)
	if action != actionExit || code != 2 {
		t.Fatalf("dispatch(serve extra) = (%v, %d), want (actionExit, 2)", action, code)
	}
	if !strings.Contains(stderr.String(), "extra") {
		t.Fatalf("stderr = %q, want mention of the unexpected argument", stderr.String())
	}
}

func TestDispatchVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		var stdout, stderr bytes.Buffer
		action, code := dispatch(args, &stdout, &stderr, os.Getenv)
		if action != actionExit || code != 0 {
			t.Fatalf("dispatch(%v) = (%v, %d), want (actionExit, 0)", args, action, code)
		}
		if !strings.Contains(stdout.String(), "omniswitch version") {
			t.Fatalf("dispatch(%v) stdout = %q, want version string", args, stdout.String())
		}
	}
}

func TestDispatchHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		action, code := dispatch(args, &stdout, &stderr, os.Getenv)
		if action != actionExit || code != 0 {
			t.Fatalf("dispatch(%v) = (%v, %d), want (actionExit, 0)", args, action, code)
		}
		if !strings.Contains(stdout.String(), "Usage:") {
			t.Fatalf("dispatch(%v) stdout = %q, want usage text", args, stdout.String())
		}
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	action, code := dispatch([]string{"bogus"}, &stdout, &stderr, os.Getenv)
	if action != actionExit || code != 1 {
		t.Fatalf("dispatch(bogus) = (%v, %d), want (actionExit, 1)", action, code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q, want unknown command message", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want usage text appended", stderr.String())
	}
}

func TestDispatchValidateConfigMissingPath(t *testing.T) {
	env := func(string) string { return "" }
	var stdout, stderr bytes.Buffer
	action, code := dispatch([]string{"validate-config"}, &stdout, &stderr, env)
	if action != actionExit || code != 2 {
		t.Fatalf("dispatch(validate-config) = (%v, %d), want (actionExit, 2)", action, code)
	}
	if !strings.Contains(stderr.String(), "a config path is required") {
		t.Fatalf("stderr = %q, want a config path is required message", stderr.String())
	}
}

func TestDispatchValidateConfigFromArg(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: omniswitch.dev/v1\nkind: GatewayConfig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := func(string) string { return "" }
	var stdout, stderr bytes.Buffer
	action, code := dispatch([]string{"validate-config", path}, &stdout, &stderr, env)
	if action != actionExit || code != 0 {
		t.Fatalf("dispatch(validate-config %s) = (%v, %d), want (actionExit, 0); stderr=%s", path, action, code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "OK: "+path) {
		t.Fatalf("stdout = %q, want OK: %s", stdout.String(), path)
	}
}

func TestDispatchValidateConfigFromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: omniswitch.dev/v1\nkind: GatewayConfig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := func(key string) string {
		if key == "OMNISWITCH_CONFIG" {
			return path
		}
		return ""
	}
	var stdout, stderr bytes.Buffer
	action, code := dispatch([]string{"validate-config"}, &stdout, &stderr, env)
	if action != actionExit || code != 0 {
		t.Fatalf("dispatch(validate-config via env) = (%v, %d), want (actionExit, 0); stderr=%s", action, code, stderr.String())
	}
}

func TestDispatchValidateConfigInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	body := "apiVersion: omniswitch.dev/v1\nkind: GatewayConfig\nauthorization:\n  external:\n    url: https://example.com/authz\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	env := func(string) string { return "" }
	var stdout, stderr bytes.Buffer
	action, code := dispatch([]string{"validate-config", path}, &stdout, &stderr, env)
	if action != actionExit || code != 1 {
		t.Fatalf("dispatch(validate-config %s) = (%v, %d), want (actionExit, 1)", path, action, code)
	}
	if !strings.Contains(stderr.String(), "not implemented") {
		t.Fatalf("stderr = %q, want a not-implemented error for authorization.external", stderr.String())
	}
}

func TestVersionString(t *testing.T) {
	got := versionString()
	if !strings.HasPrefix(got, "omniswitch version ") {
		t.Fatalf("versionString() = %q, want prefix %q", got, "omniswitch version ")
	}
}
