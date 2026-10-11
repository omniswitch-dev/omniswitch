package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMCPPolicyPathEmbeddedFallbackWhenUnset(t *testing.T) {
	settings := runtimeSettings{}
	path, source, err := resolveMCPPolicyPath(settings)
	if err != nil {
		t.Fatalf("resolveMCPPolicyPath() error = %v", err)
	}
	defer os.Remove(path)
	if source != mcpPolicySourceEmbedded {
		t.Fatalf("source = %v, want %v", source, mcpPolicySourceEmbedded)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading resolved policy: %v", err)
	}
	if string(got) != string(embeddedMCPPolicy) {
		t.Fatalf("resolved policy content does not match the embedded policy")
	}
}

func TestResolveMCPPolicyPathAbsoluteExplicitMissingErrors(t *testing.T) {
	settings := runtimeSettings{
		mcpPolicyPath:     filepath.Join(t.TempDir(), "does-not-exist.yaml"),
		mcpPolicyExplicit: true,
	}
	_, _, err := resolveMCPPolicyPath(settings)
	if err == nil {
		t.Fatal("resolveMCPPolicyPath() error = nil, want a clear resolution error")
	}
}

func TestResolveMCPPolicyPathAbsoluteImplicitMissingFallsBack(t *testing.T) {
	settings := runtimeSettings{
		mcpPolicyPath:     filepath.Join(t.TempDir(), "does-not-exist.yaml"),
		mcpPolicyExplicit: false,
	}
	path, source, err := resolveMCPPolicyPath(settings)
	if err != nil {
		t.Fatalf("resolveMCPPolicyPath() error = %v, want embedded fallback", err)
	}
	defer os.Remove(path)
	if source != mcpPolicySourceEmbedded {
		t.Fatalf("source = %v, want %v", source, mcpPolicySourceEmbedded)
	}
}

func TestResolveMCPPolicyPathRelativeToConfigDir(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policyPath, []byte("rules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := runtimeSettings{
		mcpPolicyPath:     "policy.yaml",
		mcpPolicyExplicit: true,
		configPath:        filepath.Join(dir, "gateway.yaml"),
	}
	path, source, err := resolveMCPPolicyPath(settings)
	if err != nil {
		t.Fatalf("resolveMCPPolicyPath() error = %v", err)
	}
	if source != mcpPolicySourceResolved {
		t.Fatalf("source = %v, want %v", source, mcpPolicySourceResolved)
	}
	if path != policyPath {
		t.Fatalf("path = %q, want %q", path, policyPath)
	}
}

func TestResolveMCPPolicyPathRelativeExplicitMissingErrors(t *testing.T) {
	settings := runtimeSettings{
		mcpPolicyPath:     "does-not-exist-anywhere.yaml",
		mcpPolicyExplicit: true,
		configPath:        filepath.Join(t.TempDir(), "gateway.yaml"),
	}
	_, _, err := resolveMCPPolicyPath(settings)
	if err == nil {
		t.Fatal("resolveMCPPolicyPath() error = nil, want a clear resolution error")
	}
}

// TestEmbeddedMCPPolicyMatchesSource guards against the embedded copy
// (cmd/gateway/embedded/production-delete.yaml) drifting from its source of
// truth (policies/production-delete.yaml). go:embed cannot reference paths
// outside the package directory, so the embedded copy must be kept in sync
// by hand; this test catches drift in CI.
func TestEmbeddedMCPPolicyMatchesSource(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "policies", "production-delete.yaml"))
	if err != nil {
		t.Fatalf("reading source policy: %v", err)
	}
	if string(source) != string(embeddedMCPPolicy) {
		t.Fatalf("cmd/gateway/embedded/production-delete.yaml is out of sync with policies/production-delete.yaml; copy the source file over the embedded one")
	}
}
