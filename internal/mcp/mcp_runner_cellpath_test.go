package mcp

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveCellPath_RootCwdResolvesRelativePath is the regression case for
// moody-good: when the runner's cwd is exactly the filesystem root ("/"),
// resolveCellPath's escape check compared every candidate against
// absRoot+separator == "//", a prefix no real absolute path has, so it
// rejected EVERY relative publish_paths/dest_path even when the path was
// legitimately under root. A path one level under "/" must resolve.
func TestResolveCellPath_RootCwdResolvesRelativePath(t *testing.T) {
	got, err := resolveCellPath("/", "artifact.json")
	require.NoError(t, err, "a relative path under a root cwd must resolve, not be rejected as escaping")
	assert.Equal(t, "/artifact.json", got)
}

// TestResolveCellPath_RootCwdResolvesNestedRelativePath proves the fix holds
// for a multi-segment relative path too, not just a single filename.
func TestResolveCellPath_RootCwdResolvesNestedRelativePath(t *testing.T) {
	got, err := resolveCellPath("/", "out/artifact.json")
	require.NoError(t, err)
	assert.Equal(t, filepath.Clean("/out/artifact.json"), got)
}

// TestResolveCellPath_RootCwdClampsClimbingPath proves the fix doesn't punch
// a hole in the boundary it's meant to enforce. There is no escaping "/" via
// ".." — filepath.Clean/Join clamp a climbing path at the filesystem root
// (POSIX semantics), so this resolves under root rather than erroring; the
// point of the assertion is that it can never land OUTSIDE root.
func TestResolveCellPath_RootCwdClampsClimbingPath(t *testing.T) {
	got, err := resolveCellPath("/", "../etc/passwd")
	require.NoError(t, err)
	assert.Equal(t, "/etc/passwd", got, "climbing above the filesystem root clamps at root, it cannot escape it")
}

// TestResolveCellPath_NonRootCwdResolvesRelativePath is the pre-existing,
// already-working case (non-root cwd) — kept here so the root-cwd fix's test
// file also documents the normal path, not just the edge case.
func TestResolveCellPath_NonRootCwdResolvesRelativePath(t *testing.T) {
	got, err := resolveCellPath("/home/agent/work", "artifact.json")
	require.NoError(t, err)
	assert.Equal(t, "/home/agent/work/artifact.json", got)
}

// TestResolveCellPath_NonRootCwdStillRejectsEscape is the pre-existing
// non-root escape-rejection case, unaffected by the root-cwd fix.
func TestResolveCellPath_NonRootCwdStillRejectsEscape(t *testing.T) {
	_, err := resolveCellPath("/home/agent/work", "../../etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes the working directory")
}

// TestResolveCellPath_RejectsAbsolutePath proves the absolute-path guard is
// untouched by the prefix-normalization fix.
func TestResolveCellPath_RejectsAbsolutePath(t *testing.T) {
	_, err := resolveCellPath("/home/agent/work", "/etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be relative")
}

// TestResolveCellPath_RejectsEmptyPath proves the empty-path guard is
// untouched.
func TestResolveCellPath_RejectsEmptyPath(t *testing.T) {
	_, err := resolveCellPath("/home/agent/work", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path is required")
}

// TestResolveCellPath_RootCwdSelfResolves proves the root-equals-root case
// (rel resolving to "." / the root itself) still short-circuits via the
// absClean != absRoot branch instead of relying on the prefix check.
func TestResolveCellPath_RootCwdSelfResolves(t *testing.T) {
	got, err := resolveCellPath("/", ".")
	require.NoError(t, err)
	assert.Equal(t, "/", got)
}
