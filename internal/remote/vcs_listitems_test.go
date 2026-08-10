package remote

import (
	"context"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// denyStatFs makes Stat fail with a chosen error for named paths, leaving
// every other operation to the wrapped fs — for simulating a permission-
// denied (or otherwise unreadable) directory without a real filesystem.
type denyStatFs struct {
	afero.Fs
	deny map[string]error
}

func (f denyStatFs) Stat(name string) (os.FileInfo, error) {
	if err, ok := f.deny[name]; ok {
		return nil, err
	}
	return f.Fs.Stat(name)
}

func TestGitForgeVCS_ListItems_NestedTreeWalk(t *testing.T) {
	// A repo tree with a top-level bundle, a nested path, and a non-yaml file
	// that must be skipped. Directories are descended recursively.
	mf := NewMockFetcher().
		WithDir(".ctxloom/content/bundles", []DirEntry{
			{Name: "security.yaml", IsDir: false},
			{Name: "README.md", IsDir: false},
			{Name: "lang", IsDir: true},
		}).
		WithDir(".ctxloom/content/bundles/lang", []DirEntry{
			{Name: "go", IsDir: true},
		}).
		WithDir(".ctxloom/content/bundles/lang/go", []DirEntry{
			{Name: "testing.yaml", IsDir: false},
		})

	vcs := &gitForgeVCS{fetcher: mf, owner: "owner", repo: "repo"}
	items, err := vcs.ListItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)

	// Paths are relative to .ctxloom/content/bundles/, suffix stripped, sorted;
	// the non-yaml README is skipped.
	assert.Equal(t, []string{"lang/go/testing", "security"}, items)
}

func TestGitForgeVCS_ListItems_MissingKindDirIsEmpty(t *testing.T) {
	// No .ctxloom/content/bundles dir at all → empty list, not an error (ListDir wraps
	// ErrRemoteContentNotFound, which ListItems treats as "nothing here").
	mf := NewMockFetcher()
	vcs := &gitForgeVCS{fetcher: mf, owner: "o", repo: "r"}

	items, err := vcs.ListItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestFSVCS_ListItems_WorkingSet(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/proj/.ctxloom/content"
	require.NoError(t, afero.WriteFile(fs, root+"/bundles/foo.yaml", []byte("x"), 0o644))
	require.NoError(t, afero.WriteFile(fs, root+"/bundles/team/standards.yaml", []byte("x"), 0o644))
	require.NoError(t, afero.WriteFile(fs, root+"/bundles/notes.txt", []byte("x"), 0o644))

	vcs := &fsVCS{fs: fs, root: root}

	bundles, err := vcs.ListItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	assert.Equal(t, []string{"foo", "team/standards"}, bundles)
}

// TestFSVCS_ListItems_UnreadableDirIsAnError pins that a permission-
// denied (or otherwise unreadable) local content directory must not report
// "no items" — that is indistinguishable from the ordinary, legitimate
// "nothing here yet" case (TestFSVCS_ListItems_MissingDirIsEmpty), but the two
// are very different facts. Local content is auto-trusted (ctxloom:local,
// step 3 of EffectiveTrust), so a broken listing here silently hides content
// that should have been found, with success reported regardless.
func TestFSVCS_ListItems_UnreadableDirIsAnError(t *testing.T) {
	base := afero.NewMemMapFs()
	root := "/proj/.ctxloom/content"
	require.NoError(t, afero.WriteFile(base, root+"/bundles/foo.yaml", []byte("x"), 0o644))

	fs := denyStatFs{Fs: base, deny: map[string]error{root + "/bundles": os.ErrPermission}}
	vcs := &fsVCS{fs: fs, root: root}

	items, err := vcs.ListItems(context.Background(), ItemTypeBundle)
	require.Error(t, err, "a stat failure on the kind directory must be reported, not read as \"no items\"")
	assert.Empty(t, items)
}

func TestFSVCS_ListItems_MissingDirIsEmpty(t *testing.T) {
	vcs := &fsVCS{fs: afero.NewMemMapFs(), root: "/nonexistent"}
	items, err := vcs.ListItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestRemoteRefFetcher_ListItems_CanonicalRefs(t *testing.T) {
	mf := NewMockFetcher().WithDir(".ctxloom/content/bundles", []DirEntry{
		{Name: "security.yaml", IsDir: false},
	})
	url := "https://github.com/alice/ctxloom"
	f := NewRemoteRefFetcher(
		func(loc string) (VCS, error) {
			require.Equal(t, url, loc)
			return &gitForgeVCS{fetcher: mf, owner: "alice", repo: "ctxloom"}, nil
		},
		WithRemoteSources([]string{url}),
	)

	refs, err := f.ListItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, url+"@bundles/security", refs[0].CanonicalString())
	assert.True(t, refs[0].IsCanonical())
}

func TestRemoteRefFetcher_ListItems_NoSourcesIsEmpty(t *testing.T) {
	f := NewRemoteRefFetcher(func(string) (VCS, error) { return &stubVCS{}, nil })
	refs, err := f.ListItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	assert.Empty(t, refs)
}

func TestRemoteRefFetcher_ListItems_NotMaterializedWarns(t *testing.T) {
	// One source opens, one fails to open (clone absent). The present one still
	// lists; the absent one surfaces ErrRemoteNotMaterialized with a next step.
	present := "https://github.com/alice/ctxloom"
	absent := "https://github.com/bob/ctxloom"
	mf := NewMockFetcher().WithDir(".ctxloom/content/bundles", []DirEntry{{Name: "a.yaml"}})

	f := NewRemoteRefFetcher(
		func(loc string) (VCS, error) {
			if loc == absent {
				return nil, assert.AnError
			}
			return &gitForgeVCS{fetcher: mf, owner: "alice", repo: "ctxloom"}, nil
		},
		WithRemoteSources([]string{present, absent}),
	)

	refs, err := f.ListItems(context.Background(), ItemTypeBundle)
	require.Len(t, refs, 1, "the materialized remote still lists")
	assert.Equal(t, present+"@bundles/a", refs[0].CanonicalString())

	require.Error(t, err, "the absent remote is surfaced, not silently dropped")
	assert.ErrorIs(t, err, errs.ErrRemoteNotMaterialized)
	assert.Contains(t, err.Error(), "ctxloom deps pull")
	assert.Contains(t, err.Error(), absent)
}

func TestLocalRefFetcher_ListItems_LocalRefs(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/proj/.ctxloom/content"
	require.NoError(t, afero.WriteFile(fs, root+"/bundles/foo.yaml", []byte("x"), 0o644))

	f := NewLocalRefFetcher(FSVCSFactory(fs), root)
	refs, err := f.ListItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.True(t, refs[0].IsLocal)
	assert.Equal(t, "ctxloom:local@bundles/foo", refs[0].CanonicalString())
}

func TestResolver_ListDeleted_RemoteScheme(t *testing.T) {
	url := "https://github.com/alice/ctxloom"
	// stubVCS implements Versioned, so the remote fetcher can surface deletions.
	deletedVCS := &stubVCS{deletedItems: []string{"old-bundle"}}

	resolver := NewResolver(
		NewRemoteRefFetcher(
			func(string) (VCS, error) { return deletedVCS, nil },
			WithRemoteSources([]string{url}),
		),
		// Local scheme has no history (fsVCS is not Versioned) and is skipped.
		NewLocalRefFetcher(FSVCSFactory(afero.NewMemMapFs()), "/proj/.ctxloom/content"),
	)

	refs, err := resolver.ListDeleted(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, url+"@bundles/old-bundle", refs[0].CanonicalString())
}

func TestResolver_List_FansOutAcrossSchemes(t *testing.T) {
	fs := afero.NewMemMapFs()
	localRoot := "/proj/.ctxloom/content"
	require.NoError(t, afero.WriteFile(fs, localRoot+"/bundles/localbun.yaml", []byte("x"), 0o644))

	url := "https://github.com/alice/ctxloom"
	mf := NewMockFetcher().WithDir(".ctxloom/content/bundles", []DirEntry{{Name: "remotebun.yaml"}})

	resolver := NewResolver(
		NewRemoteRefFetcher(
			func(string) (VCS, error) {
				return &gitForgeVCS{fetcher: mf, owner: "alice", repo: "ctxloom"}, nil
			},
			WithRemoteSources([]string{url}),
		),
		NewLocalRefFetcher(FSVCSFactory(fs), localRoot),
	)

	refs, err := resolver.List(context.Background(), ItemTypeBundle)
	require.NoError(t, err)

	got := map[string]bool{}
	for _, r := range refs {
		got[r.CanonicalString()] = true
	}
	assert.True(t, got[url+"@bundles/remotebun"], "remote bundle listed")
	assert.True(t, got["ctxloom:local@bundles/localbun"], "local bundle listed")
}
