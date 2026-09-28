package isolation

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
)

// linkedCheckout lays out a linked worktree the way `git worktree add`
// does: the checkout's .git is a pointer file naming its admin dir under the
// common dir, and the admin dir's gitdir file points back at the checkout.
func linkedCheckout(t *testing.T, pointer string) (dir, admin string) {
	t.Helper()
	root := t.TempDir()
	dir = filepath.Join(root, "wt")
	admin = filepath.Join(root, "repo", ".git", "worktrees", "wt")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.MkdirAll(admin, 0o755))
	if pointer == "" {
		pointer = admin
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+pointer+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(dir, ".git")+"\n"), 0o644))
	return dir, admin
}

// Under a mapper that renames paths, the checkout's pointer names a host
// path git in the container cannot open. A read-only pointer naming the
// MAPPED admin dir is mounted over it, and the admin dir's back-pointer is
// mapped the same way so an in-container `git worktree prune` does not take
// the checkout for gone.
func TestGitPointerMounts_RewritesBothPointers(t *testing.T) {
	dir, admin := linkedCheckout(t, "")
	rt := fakeRuntime{name: "docker", available: true} // maps under /ctr
	scratch := t.TempDir()

	mounts, err := gitPointerMounts(rt, dir, scratch)
	require.NoError(t, err)
	require.Len(t, mounts, 2)

	assert.Equal(t, "/ctr"+filepath.Join(dir, ".git"), mounts[0].Container)
	assert.True(t, mounts[0].ReadOnly)
	assert.Equal(t, "gitdir: /ctr"+admin+"\n", readFile(t, mounts[0].Host))

	assert.Equal(t, "/ctr"+filepath.Join(admin, "gitdir"), mounts[1].Container)
	assert.True(t, mounts[1].ReadOnly)
	assert.Equal(t, "/ctr"+filepath.Join(dir, ".git")+"\n", readFile(t, mounts[1].Host))

	for _, m := range mounts {
		assert.True(t, filepath.IsAbs(m.Host))
		assert.Equal(t, scratch, filepath.Dir(m.Host), "generated in the run's scratch, never in the checkout")
	}
	assert.Equal(t, "gitdir: "+admin+"\n", readFile(t, filepath.Join(dir, ".git")), "the host's own pointer is untouched")
}

// Where the pointer already resolves in the container there is nothing to
// rewrite: identity mapping, a relative pointer, a .git directory, no .git.
func TestGitPointerMounts_NothingToRewrite(t *testing.T) {
	identity := mapperRuntime{fakeRuntime: fakeRuntime{name: "docker", available: true}, m: identityMapper{}}
	dir, _ := linkedCheckout(t, "")
	mounts, err := gitPointerMounts(identity, dir, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, mounts, "identity: the host pointer already resolves in-container")

	rel, _ := linkedCheckout(t, "../repo/.git/worktrees/wt")
	mounts, err = gitPointerMounts(fakeRuntime{name: "docker"}, rel, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, mounts, "a relative pointer resolves under a prefix-preserving mapping")

	mainRepo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(mainRepo, ".git"), 0o755))
	mounts, err = gitPointerMounts(fakeRuntime{name: "docker"}, mainRepo, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, mounts)

	mounts, err = gitPointerMounts(fakeRuntime{name: "docker"}, t.TempDir(), t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, mounts)
}

// A .git file that is not a gitdir pointer is refused, not guessed at.
func TestGitPointerMounts_MalformedPointerFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("not a pointer\n"), 0o644))
	_, err := gitPointerMounts(fakeRuntime{name: "docker"}, dir, t.TempDir())
	require.Error(t, err)
}

// A pointer whose admin dir the runtime cannot route fails the workspace.
func TestGitPointerMounts_UnroutableAdminFails(t *testing.T) {
	dir, admin := linkedCheckout(t, "")
	rt := mapperRuntime{fakeRuntime: fakeRuntime{name: "docker"}, m: unroutableMapper{under: admin}}
	_, err := gitPointerMounts(rt, dir, t.TempDir())
	require.ErrorIs(t, err, errNoRoute)
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

// Both bases carry the rewritten pointers beside the common-dir mirror: the
// worktree base, whose cwd is always a linked checkout, and the host base
// when the live project is itself one. Building them and dropping them
// would leave git broken in-container with every unit test above green.
func TestMountBase_CarriesGitPointerMounts(t *testing.T) {
	ctx := context.Background()
	rt := fakeRuntime{name: "docker", binary: "docker", available: true}
	f := &git.Fake{}

	dir, admin := linkedCheckout(t, "")
	wtMounts, err := worktreeBase{wt: NewWorktree(f)}.mountBase(ctx, rt, t.TempDir(), dir, t.TempDir(), engineContainerSpec{}, f)
	require.NoError(t, err)
	assert.Contains(t, targetsOf(wtMounts), "/ctr"+filepath.Join(dir, ".git"))
	assert.Contains(t, targetsOf(wtMounts), "/ctr"+filepath.Join(admin, "gitdir"))

	proj, projAdmin := linkedCheckout(t, "")
	hostMounts, err := hostBase{}.mountBase(ctx, rt, proj, proj, t.TempDir(), engineContainerSpec{}, f)
	require.NoError(t, err)
	assert.Contains(t, targetsOf(hostMounts), "/ctr"+filepath.Join(proj, ".git"))
	assert.Contains(t, targetsOf(hostMounts), "/ctr"+filepath.Join(projAdmin, "gitdir"))
}
