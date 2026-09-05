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

// Every listing walks the root that CONTAINS the layouts, so the path it
// harvests for an item stored under a layout carries that layout's segment.
// A layout-qualified name resolves to NOTHING — it is not what the publisher
// published, not what a lockfile pins, and not what a consumer asks for — so
// each listing reduces its harvested paths with RepoItemName.
//
// These tests seed a bundle under a REAL layout segment and assert the bare
// name comes back. They are the only observers of that reduction: a listing
// test that seeds nothing under a segment passes just as happily with the
// reduction deleted, which is exactly the state this file was written to end.
//
// The segment is read from paths rather than spelled here, so these keep
// testing the live layout instead of a copy of it.

// segmentedName is a bundle name as a listing would harvest it for a bundle
// stored under l, and the bare name that same listing must return.
func segmentedName(t *testing.T, l paths.BundleLayout, bare string) (listed, want string) {
	t.Helper()
	seg, err := l.Segment()
	require.NoError(t, err)
	if seg == "" {
		return bare, bare
	}
	return path.Join(seg, bare), bare
}

// aSegmentedLayout is a layout whose segment is non-empty — the only kind that
// can witness the reduction. LayoutV1's segment is empty today, so a test
// driven by it would assert nothing at all; skipping loudly is better than a
// green that proves nothing.
func aSegmentedLayout(t *testing.T) paths.BundleLayout {
	t.Helper()
	for _, l := range []paths.BundleLayout{paths.LayoutV2, paths.LayoutV1} {
		if seg, err := l.Segment(); err == nil && seg != "" {
			return l
		}
	}
	t.Skip("no layout has a non-empty segment; the reduction cannot be witnessed")
	return paths.LayoutUnknown
}

// TestGitForgeVCS_ListItems_ReducesLayoutQualifiedNames covers the forge-API
// listing: a bundle stored under a layout must list under its BARE name.
func TestGitForgeVCS_ListItems_ReducesLayoutQualifiedNames(t *testing.T) {
	l := aSegmentedLayout(t)
	seg, err := l.Segment()
	require.NoError(t, err)

	root := RepoItemRoot(ItemTypeBundle)
	mf := NewMockFetcher().
		WithDir(root, []DirEntry{
			{Name: "security.yaml", IsDir: false},
			{Name: seg, IsDir: true},
		}).
		WithDir(path.Join(root, seg), []DirEntry{
			{Name: "atelier.yaml", IsDir: false},
		})

	vcs := &gitForgeVCS{fetcher: mf, owner: "owner", repo: "repo"}
	items, err := vcs.ListItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)

	_, want := segmentedName(t, l, "atelier")
	require.Contains(t, items, want,
		"a bundle stored under the %s layout must list under its bare name; a listing that returns %q resolves to nothing", l, path.Join(seg, "atelier"))
	require.NotContains(t, items, path.Join(seg, "atelier"),
		"the layout segment must not survive into the listed name")
	require.Contains(t, items, "security", "an unsegmented bundle still lists")
}

// TestFSVCS_ListItems_ReducesLayoutQualifiedNames covers the filesystem
// listing — the ctxloom:local path, whose content is AUTO-TRUSTED, so a name it
// gets wrong is a name that silently resolves to nothing with success reported.
func TestFSVCS_ListItems_ReducesLayoutQualifiedNames(t *testing.T) {
	l := aSegmentedLayout(t)
	seg, err := l.Segment()
	require.NoError(t, err)

	fs := afero.NewMemMapFs()
	root := "/proj/.ctxloom/content"
	bundles := path.Join(root, ContentItemRoot(ItemTypeBundle))
	testsupport.SeedTree(t, fs, bundles, map[string]string{
		"foo.yaml":                     "x",
		path.Join(seg, "atelier.yaml"): "x",
	})

	vcs := &fsVCS{fs: fs, root: root}
	items, err := vcs.ListItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)

	require.Contains(t, items, "atelier",
		"a bundle stored under the %s layout must list under its bare name", l)
	require.NotContains(t, items, path.Join(seg, "atelier"),
		"the layout segment must not survive into the listed name")
	require.Contains(t, items, "foo", "an unsegmented bundle still lists")
}

// TestGitCloneFetcher_ListDeletedItems_ReducesLayoutQualifiedNames covers the
// history listing. Its output feeds a PRUNE, so a name it gets wrong is not a
// cosmetic defect: a bare name that comes back layout-qualified never matches
// anything installed, and the deletion is either missed or applied to nothing.
func TestGitCloneFetcher_ListDeletedItems_ReducesLayoutQualifiedNames(t *testing.T) {
	l := aSegmentedLayout(t)
	seg, err := l.Segment()
	require.NoError(t, err)

	root := RepoItemRoot(ItemTypeBundle)
	stored := path.Join(root, seg, "atelier.yaml")

	dir := newTestRepo(t)
	writeCommit(t, dir, "publish atelier under the layout", map[string]string{stored: "x"}, nil)
	writeCommit(t, dir, "retract atelier", nil, []string{stored})

	f, err := NewGitCloneFetcher(dir, "https://github.com/o/r", ForgeGitHub, nil)
	require.NoError(t, err)

	deleted, err := f.ListDeletedItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	require.Equal(t, []string{"atelier"}, deleted,
		"a bundle retracted from the %s layout must be reported under its bare name; %q matches nothing a consumer installed", l, path.Join(seg, "atelier"))
}

// TestGitCloneFetcher_ListDeletedItems_ALayoutMoveIsNotADeletion is the
// property the reduction buys, and the reason it is applied to BOTH the HEAD
// baseline and the history union rather than only to the output.
//
// A bundle that changes layout upstream is still published — it moved. Keyed by
// segmented path it looks like a deletion plus an unrelated addition, and the
// deletion half feeds a prune that removes a bundle the publisher still ships.
// Keyed by bare name the two sides cancel, which is the truth.
func TestGitCloneFetcher_ListDeletedItems_ALayoutMoveIsNotADeletion(t *testing.T) {
	l := aSegmentedLayout(t)
	seg, err := l.Segment()
	require.NoError(t, err)

	root := RepoItemRoot(ItemTypeBundle)
	before := path.Join(root, "atelier.yaml")
	after := path.Join(root, seg, "atelier.yaml")

	dir := newTestRepo(t)
	writeCommit(t, dir, "publish atelier at the bare root", map[string]string{before: "x"}, nil)
	writeCommit(t, dir, "relocate atelier into the layout", map[string]string{after: "x"}, []string{before})

	f, err := NewGitCloneFetcher(dir, "https://github.com/o/r", ForgeGitHub, nil)
	require.NoError(t, err)

	deleted, err := f.ListDeletedItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	require.Empty(t, deleted,
		"atelier MOVED into the %s layout and is still published at HEAD; reporting it deleted would prune a live bundle", l)
}
