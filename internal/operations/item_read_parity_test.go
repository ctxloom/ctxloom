package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedRemoteFragmentFixture is seedRemoteFixture's fragment-carrying sibling: a
// real git repo holding one bundle with one fragment, locked into a fresh
// appDir's lockfile, plus a remotes.yaml alias so the bundle is addressable BOTH
// canonically and as the short "<alias>/<bundle>" form a user actually types.
func seedRemoteFragmentFixture(t *testing.T) (cfg *config.Config, canonicalRef, shortRef string) {
	t.Helper()
	testsupport.Isolate(t)

	repoDir := filepath.Join(t.TempDir(), "source")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".ctxloom", "content", "bundles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".ctxloom", "content", "bundles", "tools.yaml"),
		[]byte("version: 1.0.0\ndescription: remote tools bundle\nfragments:\n  helper:\n    content: the remote body\n    no_distill: true\n"), 0o644))
	_, err = wt.Add(".ctxloom/content/bundles/tools.yaml")
	require.NoError(t, err)
	commit, err := wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)
	repoURL := "file://" + repoDir

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	lm := remote.NewLockfileManager(appDir)
	lock, err := lm.Load()
	require.NoError(t, err)
	canonicalRef = repoURL + "@bundles/tools"
	lock.AddEntry(remote.ItemTypeBundle, canonicalRef, remote.LockEntry{
		SHA: commit.String(), URL: repoURL, FetchedAt: time.Now().UTC(),
	})
	require.NoError(t, lm.Save(lock))

	reg, err := remote.NewRegistry(paths.RemotesPath(appDir))
	require.NoError(t, err)
	require.NoError(t, reg.Add("tooling", repoURL))

	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}}), canonicalRef, "tooling/tools"
}

// TestItemRead_BundleResolutionParity is the parity test, run across
// BOTH implementations of "load a bundle, then read one item out of it": the CLI
// flow's loadBundleForItem + itemDisplayContent (which resolves the bundle
// through GetBundle) and GetItemContent (which resolved it through the raw
// seeded loader). GetBundle's own doc says it "must use the seeded loader like
// ListBundles/GetItemContent" — the two were supposed to agree.
//
// They did not, and the divergence is the defect the DUPLICATE label hid:
// GetBundle canonicalizes a short "<remote>/<bundle>" argument through the
// remotes registry, GetItemContent did not. So `ctxloom fragment show
// tooling/tools#fragments/helper` resolved while `ctxloom fragment edit` on the
// SAME ref reported the bundle did not exist.
func TestItemRead_BundleResolutionParity(t *testing.T) {
	cfg, canonicalRef, shortRef := seedRemoteFragmentFixture(t)

	for _, ref := range []string{canonicalRef, shortRef} {
		// What the CLI's showItem path does: GetBundle, then read the item.
		bundle, err := GetBundle(cfg, ref)
		require.NoErrorf(t, err, "GetBundle must resolve %q", ref)
		frag, ok := bundle.Fragments["helper"]
		require.Truef(t, ok, "%q: the bundle carries the fragment", ref)

		// What the CLI's editItem path does: GetItemContent.
		got, err := GetItemContent(context.Background(), cfg, GetItemRequest{
			Bundle: ref, Kind: ItemKindFragment, Name: "helper",
		})
		require.NoErrorf(t, err, "GetItemContent must resolve the same %q the show path accepts", ref)
		assert.Equal(t, frag.Content, got.Content, "%q: both paths must deliver the same bytes", ref)
	}
}
