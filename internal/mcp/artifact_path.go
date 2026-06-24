package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// maxArtifactFileBytes caps content sourced via the `path` arg (the aihub server
// imposes no artifact-content size limit, so the guard is client-side).
const maxArtifactFileBytes = 1 << 20 // 1 MiB

// resolveArtifactContent returns the artifact content from EXACTLY ONE of the
// inline `content` arg or the `path` arg. When `path` is given the file is read
// from local disk (the polyforge MCP server is a local process) under wsRoot,
// with a size cap and UTF-8 validation.
func resolveArtifactContent(args map[string]any, wsRoot string) (string, error) {
	content := strArg(args, "content")
	path := strArg(args, "path")
	switch {
	case content != "" && path != "":
		return "", fmt.Errorf("provide content or path, not both")
	case content == "" && path == "":
		return "", fmt.Errorf("content or path required")
	case content != "":
		return content, nil
	default:
		return readArtifactPath(path, wsRoot)
	}
}

// readArtifactPath reads path (absolute, or relative to wsRoot), confining the
// symlink-resolved real path to wsRoot, capping size, and requiring valid UTF-8.
func readArtifactPath(path, wsRoot string) (string, error) {
	rootReal, err := filepath.EvalSymlinks(wsRoot)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(rootReal, abs)
	}
	cleaned := filepath.Clean(abs)
	// Pre-symlink escape check: if the lexically-cleaned path is already outside
	// wsRoot (e.g. via ".."), reject before attempting EvalSymlinks.
	rel0, err := filepath.Rel(rootReal, cleaned)
	if err != nil || rel0 == ".." || strings.HasPrefix(rel0, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}
	real, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("path not found or unreadable: %s", path)
	}
	rel, err := filepath.Rel(rootReal, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("path not found: %s", path)
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory, not a file: %s", path)
	}
	if info.Size() > maxArtifactFileBytes {
		return "", fmt.Errorf("file too large: %d bytes (max %d)", info.Size(), maxArtifactFileBytes)
	}
	b, err := os.ReadFile(real)
	if err != nil {
		return "", fmt.Errorf("read path %s: %w", path, err)
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("file is not valid UTF-8: %s", path)
	}
	return string(b), nil
}
