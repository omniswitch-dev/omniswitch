package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// embeddedMCPPolicy is a copy of policies/production-delete.yaml baked into
// the binary. It lets a fresh binary with MCP explicitly enabled start
// without shipping or locating a policy file on disk. Keep this in sync with
// policies/production-delete.yaml (enforced by TestEmbeddedMCPPolicyMatchesSource).
//
//go:embed embedded/production-delete.yaml
var embeddedMCPPolicy []byte

// mcpPolicySource describes where a resolved MCP policy file came from, for
// logging.
type mcpPolicySource string

const (
	mcpPolicySourceConfigured mcpPolicySource = "configured path"
	mcpPolicySourceResolved   mcpPolicySource = "resolved relative path"
	mcpPolicySourceEmbedded   mcpPolicySource = "embedded default policy"
)

// resolveMCPPolicyPath finds the MCP policy file to load. When the operator
// explicitly configured a path (via OMNISWITCH_MCP_POLICY or mcp.policy in
// the config file), that path must resolve to a real file or resolution
// fails with a clear, actionable error. When no explicit path was given,
// OmniSwitch tries a small set of conventional locations for the built-in
// default policy and, if none exist, falls back to an embedded copy so a
// fresh binary with MCP enabled can still start.
//
// Resolution order for a relative path: absolute path as-is, then relative
// to the config file's directory, then relative to the running binary's
// directory, then relative to the current working directory.
func resolveMCPPolicyPath(settings runtimeSettings) (path string, source mcpPolicySource, err error) {
	candidate := settings.mcpPolicyPath
	if candidate == "" {
		return writeEmbeddedMCPPolicy()
	}

	if filepath.IsAbs(candidate) {
		if fileExists(candidate) {
			return candidate, mcpPolicySourceConfigured, nil
		}
		if settings.mcpPolicyExplicit {
			return "", "", fmt.Errorf("MCP policy path %q (from OMNISWITCH_MCP_POLICY or mcp.policy) does not exist", candidate)
		}
		return writeEmbeddedMCPPolicy()
	}

	for _, dir := range mcpPolicySearchDirs(settings.configPath) {
		full := filepath.Join(dir, candidate)
		if fileExists(full) {
			return full, mcpPolicySourceResolved, nil
		}
	}

	if settings.mcpPolicyExplicit {
		return "", "", fmt.Errorf("MCP policy path %q (from OMNISWITCH_MCP_POLICY or mcp.policy) was not found relative to the config file directory, the executable directory, or the current directory", candidate)
	}
	return writeEmbeddedMCPPolicy()
}

// mcpPolicySearchDirs returns, in priority order, the directories checked for
// a relative MCP policy path.
func mcpPolicySearchDirs(configPath string) []string {
	var dirs []string
	if configPath != "" {
		dirs = append(dirs, filepath.Dir(configPath))
	}
	if execDir, err := executableDir(); err == nil && execDir != "" {
		dirs = append(dirs, execDir)
	}
	dirs = append(dirs, ".")
	return dirs
}

func executableDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	return filepath.Dir(resolved), nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// writeEmbeddedMCPPolicy materializes the embedded default MCP policy to a
// temporary file so it can be loaded through the same file-based policy
// loader used for on-disk policies.
func writeEmbeddedMCPPolicy() (string, mcpPolicySource, error) {
	file, err := os.CreateTemp("", "omniswitch-mcp-policy-*.yaml")
	if err != nil {
		return "", "", fmt.Errorf("write embedded MCP policy: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(embeddedMCPPolicy); err != nil {
		return "", "", fmt.Errorf("write embedded MCP policy: %w", err)
	}
	return file.Name(), mcpPolicySourceEmbedded, nil
}
