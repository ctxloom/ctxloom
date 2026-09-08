package remote

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// worktreeFixture builds a real git repository holding two bundles and returns
// the cache rooted at its parent plus the URL that resolves to it.
//
// A REAL repository, not a mock: every property these tests exist to pin —
// that a sparse checkout narrows to one path, that a detached worktree carries
// its own commit, that a file deleted upstream leaves the tree — is a property
// of git, and a fake that reproduced them would be pinning the fake.
type worktreeFixture struct {
	t     *testing.T
	cache *RepoCache
	url   string
	repo  string
}

func newWorktreeFixture(t *testing.T) *worktreeFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is required to exercise worktrees")
	}
	base := t.TempDir()
	// RepoDirForURL renders the cache path from the URL, so the fixture repo
	// has to sit exactly where the cache expects a clone of that URL to be.
	cache := NewRepoCache(base, AuthConfig{})
	url := "https://example.test/trent/atelier"
	repo, err := cache.RepoDirForURL(url)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(repo, 0o755))
	f := &worktreeFixture{t: t, cache: cache, url: url, repo: repo}
	f.git("init", "-q", ".")
	return f
}

func (f *worktreeFixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	require.NoErrorf(f.t, err, "git %v: %s", args, out)
	return string(out)
}

// write puts a file in the repo working tree at a repo-relative path.
func (f *worktreeFixture) write(rel, body string) {
	f.t.Helper()
	full := filepath.Join(f.repo, filepath.FromSlash(rel))
	require.NoError(f.t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(f.t, os.WriteFile(full, []byte(body), 0o644))
}

func (f *worktreeFixture) remove(rel string) {
	f.t.Helper()
	require.NoError(f.t, os.Remove(filepath.Join(f.repo, filepath.FromSlash(rel))))
}

// commit stages everything and returns the new commit SHA.
func (f *worktreeFixture) commit(msg string) string {
	f.t.Helper()
	f.git("add", "-A")
	f.git("commit", "-q", "-m", msg)
	sha := f.git("rev-parse", "HEAD")
	return sha[:len(sha)-1] // strip the trailing newline
}

// TestEnsureSparseWorktree_ChecksOutOnlyTheBundleAtItsRepositoryPath pins the
// two facts every reader depends on: the checkout is NARROWED to the one
// bundle, and the bundle lands at its repository path inside the worktree —
// which is what Reference.LocalTreePath's nesting resolves to.
func TestEnsureSparseWorktree_ChecksOutOnlyTheBundleAtItsRepositoryPath(t *testing.T) {
	f := newWorktreeFixture(t)
	f.write("bundles/v2/atelier/bundle.yaml", "version: \"1.0.0\"\n")
	f.write("bundles/v2/other/bundle.yaml", "version: \"9.9.9\"\n")
	f.write("README.md", "unrelated\n")
	sha := f.commit("one")

	wt := filepath.Join(t.TempDir(), "atelier.worktree")
	dir, err := f.cache.EnsureSparseWorktree(t.Context(), f.url, sha, "bundles/v2/atelier", wt)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(wt, "bundles", "v2", "atelier"), dir,
		"the bundle lands at its REPOSITORY path inside the worktree")
	body, rerr := os.ReadFile(filepath.Join(dir, "bundle.yaml"))
	require.NoError(t, rerr)
	assert.Equal(t, "version: \"1.0.0\"\n", string(body))

	_, err = os.Stat(filepath.Join(wt, "bundles", "v2", "other"))
	assert.True(t, os.IsNotExist(err), "a sibling bundle must not be checked out by a narrowed worktree")
	_, err = os.Stat(filepath.Join(wt, "README.md"))
	assert.True(t, os.IsNotExist(err), "the rest of the repository must not be checked out")
}

// TestEnsureSparseWorktree_TwoBundlesOfOneRepoHoldDifferentCommits is the
// property that forces a worktree PER BUNDLE rather than one shared checkout of
// the clone. A single checkout can only be at one commit, so it cannot represent
// two bundles pinned apart — and the version skew would be silent.
func TestEnsureSparseWorktree_TwoBundlesOfOneRepoHoldDifferentCommits(t *testing.T) {
	f := newWorktreeFixture(t)
	f.write("bundles/v2/atelier/bundle.yaml", "version: \"1.0.0\"\n")
	f.write("bundles/v2/other/bundle.yaml", "version: \"1.0.0\"\n")
	first := f.commit("one")
	f.write("bundles/v2/atelier/bundle.yaml", "version: \"2.0.0\"\n")
	f.write("bundles/v2/other/bundle.yaml", "version: \"2.0.0\"\n")
	second := f.commit("two")

	root := t.TempDir()
	held, err := f.cache.EnsureSparseWorktree(t.Context(), f.url, first, "bundles/v2/atelier", filepath.Join(root, "atelier.worktree"))
	require.NoError(t, err)
	moved, err := f.cache.EnsureSparseWorktree(t.Context(), f.url, second, "bundles/v2/other", filepath.Join(root, "other.worktree"))
	require.NoError(t, err)

	heldBody, err := os.ReadFile(filepath.Join(held, "bundle.yaml"))
	require.NoError(t, err)
	movedBody, err := os.ReadFile(filepath.Join(moved, "bundle.yaml"))
	require.NoError(t, err)

	assert.Equal(t, "version: \"1.0.0\"\n", string(heldBody),
		"a bundle pinned at the older commit must keep the older commit's bytes")
	assert.Equal(t, "version: \"2.0.0\"\n", string(movedBody))
}

// TestEnsureSparseWorktree_MovingThePinReplacesRatherThanMerges. A merge would
// leave a file the publisher DELETED upstream sitting in the consumer's tree
// forever, still enumerated by every walk that reads the bundle — and, for
// hooks and MCP servers, still applied.
func TestEnsureSparseWorktree_MovingThePinReplacesRatherThanMerges(t *testing.T) {
	f := newWorktreeFixture(t)
	f.write("bundles/v2/atelier/bundle.yaml", "version: \"1.0.0\"\n")
	f.write("bundles/v2/atelier/hooks/gone.yaml", "type: command\n")
	first := f.commit("one")
	f.remove("bundles/v2/atelier/hooks/gone.yaml")
	f.write("bundles/v2/atelier/bundle.yaml", "version: \"2.0.0\"\n")
	second := f.commit("two")

	wt := filepath.Join(t.TempDir(), "atelier.worktree")
	dir, err := f.cache.EnsureSparseWorktree(t.Context(), f.url, first, "bundles/v2/atelier", wt)
	require.NoError(t, err)
	stale := filepath.Join(dir, "hooks", "gone.yaml")
	require.FileExists(t, stale)

	dir, err = f.cache.EnsureSparseWorktree(t.Context(), f.url, second, "bundles/v2/atelier", wt)
	require.NoError(t, err)

	_, serr := os.Stat(stale)
	assert.True(t, os.IsNotExist(serr), "a file removed upstream outlived the pin move that removed it")
	body, rerr := os.ReadFile(filepath.Join(dir, "bundle.yaml"))
	require.NoError(t, rerr)
	assert.Equal(t, "version: \"2.0.0\"\n", string(body), "moving the pin did not move the bytes")
}

// TestEnsureSparseWorktree_ForcesPastALocalEditOfTheCache. The cache is DERIVED:
// re-ensuring is how a caller says "make this match the pin again", and a local
// edit must not be able to strand a worktree off its pin.
func TestEnsureSparseWorktree_ForcesPastALocalEditOfTheCache(t *testing.T) {
	f := newWorktreeFixture(t)
	f.write("bundles/v2/atelier/bundle.yaml", "version: \"1.0.0\"\n")
	sha := f.commit("one")

	wt := filepath.Join(t.TempDir(), "atelier.worktree")
	dir, err := f.cache.EnsureSparseWorktree(t.Context(), f.url, sha, "bundles/v2/atelier", wt)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bundle.yaml"), []byte("tampered\n"), 0o644))

	dir, err = f.cache.EnsureSparseWorktree(t.Context(), f.url, sha, "bundles/v2/atelier", wt)
	require.NoError(t, err)
	body, rerr := os.ReadFile(filepath.Join(dir, "bundle.yaml"))
	require.NoError(t, rerr)
	assert.Equal(t, "version: \"1.0.0\"\n", string(body), "re-ensuring must restore the pinned bytes over a local edit")
}

// TestEnsureSparseWorktree_RefusesACommitThatDoesNotHoldTheBundle. A sparse
// checkout of a path the commit does not contain SUCCEEDS and writes nothing,
// which would hand a reader an empty directory and call it installed.
func TestEnsureSparseWorktree_RefusesACommitThatDoesNotHoldTheBundle(t *testing.T) {
	f := newWorktreeFixture(t)
	f.write("bundles/v2/other/bundle.yaml", "version: \"1.0.0\"\n")
	sha := f.commit("one")

	wt := filepath.Join(t.TempDir(), "atelier.worktree")
	_, err := f.cache.EnsureSparseWorktree(t.Context(), f.url, sha, "bundles/v2/atelier", wt)

	require.Error(t, err, "an empty checkout must not be reported as an install")
	assert.Contains(t, err.Error(), "bundles/v2/atelier")
}

// TestEnsureSparseWorktree_RefusesAPathEscapingTheWorktree. The subpath is
// joined onto the worktree root to name the directory a reader is then pointed
// at, so a traversal would hand back a directory outside the worktree entirely.
func TestEnsureSparseWorktree_RefusesAPathEscapingTheWorktree(t *testing.T) {
	f := newWorktreeFixture(t)
	f.write("bundles/v2/atelier/bundle.yaml", "version: \"1.0.0\"\n")
	sha := f.commit("one")

	wt := filepath.Join(t.TempDir(), "atelier.worktree")
	for _, bad := range []string{"../outside", "bundles/../../outside", "/etc", ""} {
		_, err := f.cache.EnsureSparseWorktree(t.Context(), f.url, sha, bad, wt)
		assert.Errorf(t, err, "subpath %q must be refused", bad)
	}
}

// TestEnsureSparseWorktree_RequiresAClone names the one precondition a caller
// has to satisfy, rather than failing deep inside git.
func TestEnsureSparseWorktree_RequiresAClone(t *testing.T) {
	base := t.TempDir()
	cache := NewRepoCache(base, AuthConfig{})

	_, err := cache.EnsureSparseWorktree(t.Context(), "https://example.test/nobody/nothing", "deadbeef", "bundles/v2/x", filepath.Join(base, "wt"))

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoCloneForWorktree)
}

// TestEnsureSparseWorktree_RecoversFromADirectoryDeletedBehindGitsBack. A wiped
// cache or an interrupted run leaves a registration with no directory, and
// `worktree add` then refuses the path as already registered — so the pull that
// exists to repair the cache would be the one command that cannot.
func TestEnsureSparseWorktree_RecoversFromADirectoryDeletedBehindGitsBack(t *testing.T) {
	f := newWorktreeFixture(t)
	f.write("bundles/v2/atelier/bundle.yaml", "version: \"1.0.0\"\n")
	sha := f.commit("one")

	wt := filepath.Join(t.TempDir(), "atelier.worktree")
	_, err := f.cache.EnsureSparseWorktree(t.Context(), f.url, sha, "bundles/v2/atelier", wt)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(wt))

	dir, err := f.cache.EnsureSparseWorktree(t.Context(), f.url, sha, "bundles/v2/atelier", wt)
	require.NoError(t, err, "a stale registration must not make the cache unrepairable")
	assert.FileExists(t, filepath.Join(dir, "bundle.yaml"))
}
