package remote

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRepoCache_safeRepoPath_StaysInsideBaseDir pins the path-traversal guard
// in safeRepoPath (repo_cache.go:233/241). The returned dir is later RemoveAll'd
// and cloned into, so a crafted repo URL with ".." segments must never escape
// the cache root. We assert both the exact sanitized path (so the ".." drop is
// load-bearing, not the Join-collapse that filepath.Join would do anyway) and
// the containment invariant via filepath.Rel.
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
			name:  "only_traversal_resolves_to_base",
			parts: []string{"..", ".", ".."},
			want:  "/tmp/cache",
		},
		{
			name:  "absolute_path_part_is_reparented_under_base",
			parts: []string{"/etc/passwd"},
			want:  "/tmp/cache/etc/passwd",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.safeRepoPath(tc.parts...)
			assert.Equal(t, tc.want, got)

			rel, err := filepath.Rel(base, got)
			require.NoError(t, err)
			assert.False(t,
				rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)),
				"safeRepoPath(%v) = %q escaped baseDir %q", tc.parts, got, base)
		})
	}
}

// TestRepoCache_repoDirForURL_TraversalContained ties the guard to the public
// URL entry point: a clone URL whose path tries to climb out of the cache must
// still resolve inside baseDir.
func TestRepoCache_repoDirForURL_TraversalContained(t *testing.T) {
	base := "/tmp/cache"
	c := NewRepoCache(base, AuthConfig{})

	got := c.repoDirForURL("https://github.com/../../../../etc/passwd")
	rel, err := filepath.Rel(base, got)
	require.NoError(t, err)
	assert.False(t,
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)),
		"repoDirForURL produced %q which escapes baseDir %q", got, base)
}
