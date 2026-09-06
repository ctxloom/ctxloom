package remote

import (
	"context"
	"path"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A published TREE bundle is ONE listed item.
//
// Both listings harvest ".yaml at any depth", and a tree's items are .yaml —
// profiles/, mcp/, a skill's sidecars. So a listing that descends offers
// "<bundle>/profiles/<x>" as an installable name, and the ones that then fail
// to resolve are indistinguishable from a publisher's genuine bundles. The
// manifest itself is the same defect wearing a different name: "<bundle>/bundle"
// resolves to nothing either.
//
// The assertions are exact SETS. The failure ADDS names, so a Contains or a
// length would pass with the boundary deleted — the shape that already let this
// bug through twice.

// TestGitForgeVCS_ListItems_TreeBundleIsOneItem covers the forge-API listing:
// it has no filesystem, so its manifest evidence comes from the directory
// listing it already holds.
func TestGitForgeVCS_ListItems_TreeBundleIsOneItem(t *testing.T) {
	l := aSegmentedLayout(t)
	seg, err := l.Segment()
	require.NoError(t, err)

	root := RepoItemRoot(ItemTypeBundle)
	treeRoot := path.Join(root, seg, "agent-ensemble")
	mf := NewMockFetcher().
		WithDir(root, []DirEntry{
			{Name: seg, IsDir: true},
		}).
		WithDir(path.Join(root, seg), []DirEntry{
			{Name: "solo.yaml", IsDir: false},
			{Name: "agent-ensemble", IsDir: true},
		}).
		WithDir(treeRoot, []DirEntry{
			{Name: paths.BundleManifestName, IsDir: false},
			{Name: "profiles", IsDir: true},
			{Name: "SHA256SUMS", IsDir: false},
		}).
		WithDir(path.Join(treeRoot, "profiles"), []DirEntry{
			{Name: "coordinator.yaml", IsDir: false},
			{Name: "finder.yaml", IsDir: false},
		})

	vcs := &gitForgeVCS{fetcher: mf, owner: "owner", repo: "repo"}
	items, err := vcs.ListItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)

	require.Equal(t, []string{"agent-ensemble", "solo"}, items,
		"a tree bundle lists as itself; its profiles/*.yaml are its ITEMS, and %q is not a bundle anyone can install",
		"agent-ensemble/profiles/coordinator")
}

// TestFSVCS_ListItems_TreeBundleIsOneItem covers the filesystem listing — the
// ctxloom:local path, whose content is AUTO-TRUSTED, so a name it invents is a
// name that silently resolves to nothing with success reported.
func TestFSVCS_ListItems_TreeBundleIsOneItem(t *testing.T) {
	l := aSegmentedLayout(t)
	seg, err := l.Segment()
	require.NoError(t, err)

	fs := afero.NewMemMapFs()
	root := "/proj/.ctxloom/content"
	bundlesRoot := path.Join(root, ContentItemRoot(ItemTypeBundle))
	testsupport.SeedTree(t, fs, bundlesRoot, map[string]string{
		path.Join(seg, "solo.yaml"):                                  "version: 1.0.0\n",
		path.Join(seg, "agent-ensemble", paths.BundleManifestName):   "version: 1.0.0\n",
		path.Join(seg, "agent-ensemble", "profiles", "coord.yaml"):   "name: coord\n",
		path.Join(seg, "agent-ensemble", "skills", "s", "meta.yaml"): "kind: skill\n",
	})

	vcs := &fsVCS{fs: fs, root: root}
	items, err := vcs.ListItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)

	require.Equal(t, []string{"agent-ensemble", "solo"}, items,
		"nothing beneath a tree bundle is a bundle, at any depth")
}
