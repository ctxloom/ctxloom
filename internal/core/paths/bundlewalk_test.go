package paths

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The classifier is the whole of the bundles-root boundary, so these pin both
// answers it gives — the name AND the stop — for every entry shape a walk meets.
// The callers' own tests observe the effect; these observe the rule, which is
// what makes a wrong answer here name the defect instead of a symptom of it.

func TestClassifyBundleWalkEntry_TreeRootIsOneBundleAndStopsTheWalk(t *testing.T) {
	step := ClassifyBundleWalkEntry("agent-ensemble", true, true)
	require.True(t, step.IsBundle)
	require.True(t, step.IsTree)
	assert.Equal(t, "agent-ensemble", step.Name)
	assert.False(t, step.Descend(), "a tree bundle owns everything beneath it")
	assert.Equal(t, filepath.SkipDir, step.WalkSkip(),
		"the walk must be told to skip the subtree, not merely told the entry was a bundle")
}

func TestClassifyBundleWalkEntry_TreeItemsAreNotBundles(t *testing.T) {
	// These are exactly the paths a walk sees INSIDE a tree it failed to stop
	// at. None of them is a bundle in its own right — but only the stop keeps
	// them from being reached at all, which is why both halves are tested.
	for _, rel := range []string{
		"agent-ensemble/" + BundleManifestName,
		"agent-ensemble/profiles",
		"agent-ensemble/skills/humanize",
	} {
		isDir := !filepath.IsAbs(rel) && filepath.Ext(rel) == ""
		step := ClassifyBundleWalkEntry(rel, isDir, false)
		assert.False(t, step.IsBundle, "%s is a tree's own content, not a bundle", rel)
	}
}

func TestClassifyBundleWalkEntry_SingleFileBundleKeepsItsPathRelativeName(t *testing.T) {
	step := ClassifyBundleWalkEntry("personal/foo.yaml", false, false)
	require.True(t, step.IsBundle)
	assert.False(t, step.IsTree)
	assert.Equal(t, "personal/foo", step.Name, "authored depth is a legitimate part of the name")
	assert.NoError(t, step.WalkSkip(),
		"SkipDir from a FILE callback abandons the rest of the directory, hiding every sibling bundle")
	assert.True(t, step.Descend())
}

func TestClassifyBundleWalkEntry_PlainDirectoryIsWalkedThrough(t *testing.T) {
	step := ClassifyBundleWalkEntry("personal", true, false)
	assert.False(t, step.IsBundle, "a directory with no manifest is just a path segment")
	assert.True(t, step.Descend(), "refusing to descend here would delete nested authored bundles")
	assert.NoError(t, step.WalkSkip())
}

func TestClassifyBundleWalkEntry_RootItselfIsNeverABundle(t *testing.T) {
	for _, rel := range []string{".", ""} {
		step := ClassifyBundleWalkEntry(rel, true, true)
		assert.False(t, step.IsBundle,
			"the walked root is the parent bundles sit under; naming it yields %q, which resolves to nothing", rel)
		assert.True(t, step.Descend(), "stopping at the root would list no bundles at all")
	}
}

func TestClassifyBundleWalkEntry_NonYAMLFilesAreIgnored(t *testing.T) {
	for _, rel := range []string{"README.md", "SHA256SUMS", "fragments/delegation.md"} {
		assert.False(t, ClassifyBundleWalkEntry(rel, false, false).IsBundle, rel)
	}
}

func TestBundleManifestPath_ComposesTheProbedAndReadFile(t *testing.T) {
	assert.Equal(t, filepath.Join("some", "dir", BundleManifestName),
		BundleManifestPath(filepath.Join("some", "dir")))
}
