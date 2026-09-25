package remote

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustWorktreePath is LocalWorktreePath for a reference the test knows is
// addressable.
func mustWorktreePath(t *testing.T, r *Reference, baseDir string) string {
	t.Helper()
	dir, err := r.LocalWorktreePath(baseDir)
	require.NoError(t, err)
	return dir
}

// mustTreePath is LocalTreePath for a reference the test knows is addressable.
func mustTreePath(t *testing.T, r *Reference, baseDir string) string {
	t.Helper()
	dir, err := r.LocalTreePath(baseDir)
	require.NoError(t, err)
	return dir
}

// The worktree a bundle is read from must be INJECTIVE in the bundle's
// identity. Two lock keys sharing one directory read one tree — whichever was
// pulled last — so a retracted repository's bytes would be served under
// another repository's unretracted key. The directory is compared
// case-insensitively because a case-folding filesystem (macOS, Windows)
// collapses names that differ only in case, while path case IS identity.
func TestLocalWorktreePath_DistinctIdentitiesNeverShareADirectory(t *testing.T) {
	pairs := [][2]Reference{
		{{URL: "file:///srv/x/r", Path: "kit"}, {URL: "file:///other/x/r", Path: "kit"}},
		{{URL: "https://github.com/O/R", Path: "kit"}, {URL: "https://github.com/o/r", Path: "kit"}},
		{{URL: "https://github.com/o/r", Path: "Kit"}, {URL: "https://github.com/o/r", Path: "kit"}},
		{{URL: "https://a.example/o/r", Path: "kit"}, {URL: "file:///a.example/o/r", Path: "kit"}},
	}
	for _, p := range pairs {
		a, b := p[0], p[1]
		da, db := mustWorktreePath(t, &a, "/proj/.ctxloom"), mustWorktreePath(t, &b, "/proj/.ctxloom")
		assert.False(t, strings.EqualFold(da, db),
			"%s %s and %s %s share cache dir %q / %q", a.URL, a.Path, b.URL, b.Path, da, db)
	}
}

// Spellings of ONE identity must share the directory: the pull installs under
// the reference as typed, and a reader finds the tree again from the lock key.
func TestLocalWorktreePath_OneIdentityOneDirectory(t *testing.T) {
	spellings := []string{
		"https://github.com/o/r",
		"https://GitHub.com/o/r/",
		"git@github.com:o/r",
		"http://github.com/o/r",
	}
	want := mustWorktreePath(t, &Reference{URL: spellings[0], Path: "kit"}, "/proj/.ctxloom")
	for _, s := range spellings[1:] {
		got := mustWorktreePath(t, &Reference{URL: s, Path: "kit"}, "/proj/.ctxloom")
		assert.Equal(t, want, got, "spelling %q", s)
	}
}
