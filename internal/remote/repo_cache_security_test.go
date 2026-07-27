package remote

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRepoCache_safeRepoPath_StaysInsideBaseDir pins the path-traversal guard
// in safeRepoPath. The returned dir is later RemoveAll'd and cloned into, so
// a crafted repo URL with ".." segments must never escape the cache root —
// AND must never resolve to the cache root itself (U095-F01: "equals
// baseDir" is exactly as dangerous as "escapes baseDir" once the caller
// RemoveAll's it). We assert both the exact sanitized path (so the ".." drop
// is load-bearing, not the Join-collapse that filepath.Join would do anyway)
// and the containment invariant via filepath.Rel.
func TestRepoCache_safeRepoPath_StaysInsideBaseDir(t *testing.T) {
	base := "/tmp/cache"
	c := NewRepoCache(base, AuthConfig{})

	cases := []struct {
		name  string
		parts []string
		want  string
	}{
		{
			// If the ".." drop is removed, Join collapses "github.com/../../etc"
			// to /tmp/etc and the Rel fallback rewrites it to baseDir — a
			// different result, so this pins the drop.
			name:  "dotdot_between_segments_is_dropped",
			parts: []string{"github.com", "../../etc"},
			want:  "/tmp/cache/github.com/etc",
		},
		{
			name:  "leading_dotdot_dropped",
			parts: []string{"..", "..", "secrets"},
			want:  "/tmp/cache/secrets",
		},
		{
			name:  "embedded_dotdot_chain_dropped",
			parts: []string{"github.com/owner/../../../../root"},
			want:  "/tmp/cache/github.com/owner/root",
		},
		{
			name:  "current_dir_segments_dropped",
			parts: []string{"github.com", "owner/./repo"},
			want:  "/tmp/cache/github.com/owner/repo",
		},
		{
			name:  "absolute_path_part_is_reparented_under_base",
			parts: []string{"/etc/passwd"},
			want:  "/tmp/cache/etc/passwd",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.safeRepoPath(tc.parts...)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)

			rel, relErr := filepath.Rel(base, got)
			require.NoError(t, relErr)
			assert.False(t,
				rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)),
				"safeRepoPath(%v) = %q escaped baseDir %q", tc.parts, got, base)
		})
	}
}

// TestRepoCache_safeRepoPath_OnlyTraversalIsRefused pins U095-F01: when every
// part is traversal-only ("..", ".", "") there are no surviving segments, and
// the OLD behavior silently returned baseDir itself as though it were a safe,
// contained answer. That is exactly as dangerous as escaping baseDir, because
// ensureCloneLocked's very next move is `os.RemoveAll(repoDir)` — a degenerate
// repo URL must never resolve to a path whose deletion means wiping the
// entire clone cache. It must be a refusal (an error), never a fallback.
func TestRepoCache_safeRepoPath_OnlyTraversalIsRefused(t *testing.T) {
	c := NewRepoCache("/tmp/cache", AuthConfig{})

	cases := [][]string{
		{"..", ".", ".."},
		{""},
		{"", "", ""},
		{},
	}
	for _, parts := range cases {
		got, err := c.safeRepoPath(parts...)
		require.Error(t, err, "safeRepoPath(%v) must be refused, not silently resolved to the cache root", parts)
		assert.Empty(t, got)
	}
}

// TestRepoCache_repoDirForURL_TraversalContained ties the guard to the public
// URL entry point: a clone URL whose path tries to climb out of the cache must
// still resolve inside baseDir.
func TestRepoCache_repoDirForURL_TraversalContained(t *testing.T) {
	base := "/tmp/cache"
	c := NewRepoCache(base, AuthConfig{})

	got, err := c.RepoDirForURL("https://github.com/../../../../etc/passwd")
	require.NoError(t, err)
	rel, relErr := filepath.Rel(base, got)
	require.NoError(t, relErr)
	assert.False(t,
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)),
		"RepoDirForURL produced %q which escapes baseDir %q", got, base)
}

// TestRepoCache_repoDirForURL_DegenerateURLIsRefused pins U095-F01/U086-F07 at
// the public entry point: a degenerate remote URL (empty, a bare scheme, only
// traversal) must be refused with an error, never silently resolved to the
// cache root — the caller's next move (os.RemoveAll) would otherwise wipe
// every cached clone.
func TestRepoCache_repoDirForURL_DegenerateURLIsRefused(t *testing.T) {
	base := "/tmp/cache"
	c := NewRepoCache(base, AuthConfig{})

	degenerate := []string{"", "https://", "../..", "."}
	for _, u := range degenerate {
		got, err := c.RepoDirForURL(u)
		require.Error(t, err, "RepoDirForURL(%q) must be refused, not resolved to the cache root", u)
		assert.Empty(t, got)
	}
}
