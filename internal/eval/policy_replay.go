package eval

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxReplayPolicyFiles bounds how many policy files one replay request may
// load, so a single call cannot make the server open and compile an
// unbounded number of files.
const maxReplayPolicyFiles = 64

// errPolicyNotAllowed is returned for any policy_paths entry that cannot be
// served from the trusted policy root. Its message never includes file
// content and never says why the path was rejected beyond the generic rule,
// so callers cannot probe the server filesystem.
var errPolicyNotAllowed = errors.New("policy path must name a .cel, .yaml or .yml file inside the configured policy directory")

// SetPolicyRoot sets the operator-trusted directory that policy replay
// requests resolve policy_paths against. An empty root disables file-based
// policy replay.
func (h *Handler) SetPolicyRoot(root string) {
	h.policyRoot = strings.TrimSpace(root)
}

// resolveReplayPolicyPaths maps caller-supplied policy names onto regular
// files under root. Caller input is never passed to the filesystem as-is:
// absolute paths, ".." segments, unknown extensions, and anything that
// resolves (through symlinks) outside root or to a non-regular file are
// rejected. The returned paths are fully resolved and inside root.
func resolveReplayPolicyPaths(root string, names []string) ([]string, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("policy replay is disabled: no policy directory is configured")
	}
	if len(names) > maxReplayPolicyFiles {
		return nil, fmt.Errorf("at most %d policy_paths are allowed", maxReplayPolicyFiles)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, errors.New("policy directory is unavailable")
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, errors.New("policy directory is unavailable")
	}
	resolved := make([]string, 0, len(names))
	for _, name := range names {
		path, err := resolveReplayPolicyPath(realRoot, name)
		if err != nil {
			return nil, fmt.Errorf("policy_paths %q: %w", name, err)
		}
		resolved = append(resolved, path)
	}
	return resolved, nil
}

func resolveReplayPolicyPath(realRoot, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsRune(name, 0) {
		return "", errPolicyNotAllowed
	}
	// Accept both separators so a Windows-style name cannot sneak a ".."
	// segment past the check on any platform.
	slashed := strings.ReplaceAll(name, `\`, "/")
	if filepath.IsAbs(name) || strings.HasPrefix(slashed, "/") || filepath.VolumeName(name) != "" {
		return "", errPolicyNotAllowed
	}
	for _, segment := range strings.Split(slashed, "/") {
		if segment == ".." {
			return "", errPolicyNotAllowed
		}
	}
	switch strings.ToLower(filepath.Ext(slashed)) {
	case ".cel", ".yaml", ".yml":
	default:
		return "", errPolicyNotAllowed
	}
	relative := filepath.Clean(filepath.FromSlash(slashed))
	candidates := []string{relative}
	// Documentation and older clients name policies relative to the working
	// directory (for example "policies/production-delete.yaml"). Accept that
	// form when its first segment is the policy directory's own name.
	if first, rest, ok := strings.Cut(filepath.ToSlash(relative), "/"); ok && first == filepath.Base(realRoot) && rest != "" {
		candidates = append(candidates, filepath.FromSlash(rest))
	}
	for _, candidate := range candidates {
		path, ok := regularFileUnderRoot(realRoot, candidate)
		if ok {
			return path, nil
		}
	}
	return "", errPolicyNotAllowed
}

// regularFileUnderRoot reports the fully resolved path of root/relative when
// it is a regular file that stays inside root after resolving symlinks.
func regularFileUnderRoot(realRoot, relative string) (string, bool) {
	realPath, err := filepath.EvalSymlinks(filepath.Join(realRoot, relative))
	if err != nil {
		return "", false
	}
	within, err := filepath.Rel(realRoot, realPath)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) || filepath.IsAbs(within) {
		return "", false
	}
	info, err := os.Lstat(realPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	switch strings.ToLower(filepath.Ext(realPath)) {
	case ".cel", ".yaml", ".yml":
	default:
		return "", false
	}
	return realPath, true
}
