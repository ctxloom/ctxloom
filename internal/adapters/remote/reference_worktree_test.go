package remote

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTreeRepoPath_SingleLayoutInvariant is the CHECK behind
// Reference.TreeRepoPath taking the first candidate root.
//
// The fetch PROBES every root BundleTreeRoots names and installs at whichever
// ANSWERS; a reader cannot probe a repository it never opens, so it computes
// the root instead. The two agree only while there is exactly ONE root — which
// is true of a single live bundle format and stops being true the moment a
// format migration adds the next one. That is precisely when a checkout would
// land at the root the probe found while every reader looked at the other, so
// this goes red at the migration rather than at the silent mis-resolution.
func TestTreeRepoPath_SingleLayoutInvariant(t *testing.T) {
	ref, err := ParseReference("https://github.com/trent/atelier@bundles/atelier")
	require.NoError(t, err)

	roots := BundleTreeRoots(ref.BuildFilePath(ItemTypeBundle))

	require.Len(t, roots, 1,
		"a second bundle format root is live: Reference.TreeRepoPath now has to resolve WHICH root holds each bundle "+
			"(record it with the pin, or probe the worktree), because the fetch probe and the reader can no longer be assumed to agree")
	assert.Equal(t, roots[0], ref.TreeRepoPath())
}

// TestLocalTreePath_NestsTheBundleInsideItsWorktree pins the layout the whole
// cache depends on: a sparse checkout lays the bundle out at its REPOSITORY
// path, so the bundle directory is nested inside the worktree rather than being
// the worktree root.
//
// The last segment must remain the bundle id — content.validateBundleID takes a
// single segment, and config.treeBundleReader roots its tree at the PARENT and
// names the bundle by the base. A path whose base is not the bundle id resolves
// to a bundle nobody asked for, or to nothing, without any error saying so.
func TestLocalTreePath_NestsTheBundleInsideItsWorktree(t *testing.T) {
	ref, err := ParseReference("https://github.com/trent/atelier@bundles/atelier")
	require.NoError(t, err)

	worktree := ref.LocalWorktreePath("/proj/.ctxloom")
	tree := ref.LocalTreePath("/proj/.ctxloom")

	assert.Equal(t, filepath.Join(worktree, filepath.FromSlash(ref.TreeRepoPath())), tree,
		"the bundle sits at its repository path inside the worktree")
	assert.Equal(t, "atelier", filepath.Base(tree),
		"the last segment is the bundle id, which is what the reader names the bundle by")

	rel, rerr := filepath.Rel(worktree, tree)
	require.NoError(t, rerr)
	assert.NotEqual(t, ".", rel, "the bundle directory is never the worktree root itself")
	assert.NotContains(t, rel, "..", "the bundle directory is inside its own worktree")
}

// TestLocalWorktreePath_IsNotNamedForTheBundle. The worktree root and the bundle
// nested inside it would otherwise both be named for the bundle, and anything
// searching the cache for a directory bearing that name finds the root first —
// an empty directory that reads as an installed bundle.
func TestLocalWorktreePath_IsNotNamedForTheBundle(t *testing.T) {
	ref, err := ParseReference("https://github.com/trent/atelier@bundles/atelier")
	require.NoError(t, err)

	assert.NotEqual(t, "atelier", filepath.Base(ref.LocalWorktreePath("/proj/.ctxloom")),
		"a worktree root sharing the bundle's name shadows the bundle in any name-based search of the cache")
}
