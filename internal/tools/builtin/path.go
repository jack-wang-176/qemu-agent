package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func resolveWorkspacePath(workspace, requested string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", fmt.Errorf("workspace is empty")
	}
	if strings.TrimSpace(requested) == "" {
		return "", fmt.Errorf("path is empty")
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	root = resolveExistingSymlinks(root)
	candidate = resolveExistingSymlinks(candidate)
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", fmt.Errorf("compare path with workspace: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside workspace %q", requested, root)
	}
	return filepath.Clean(candidate), nil
}

func resolveExistingSymlinks(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved
	}
	for parent := filepath.Dir(path); parent != path; parent = filepath.Dir(parent) {
		resolvedParent, parentErr := filepath.EvalSymlinks(parent)
		if parentErr == nil {
			relative, relativeErr := filepath.Rel(parent, path)
			if relativeErr == nil {
				return filepath.Join(resolvedParent, relative)
			}
		}
	}
	if _, err := os.Lstat(path); err == nil {
		return path
	}
	return filepath.Clean(path)
}
