package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	dir, admin := linkedCheckout(t, "")
	f := &git.Fake{CommonDirValue: filepath.Dir(filepath.Dir(admin))}
	wtMounts, err := worktreeBase{wt: NewWorktree(f)}.mountBase(ctx, rt, t.TempDir(), dir, t.TempDir(), engineContainerSpec{}, f)
	require.NoError(t, err)
	assert.Contains(t, targetsOf(wtMounts), "/ctr"+filepath.Join(dir, ".git"))
	assert.Contains(t, targetsOf(wtMounts), "/ctr"+filepath.Join(admin, "gitdir"))

	proj, projAdmin := linkedCheckout(t, "")
	f = &git.Fake{CommonDirValue: filepath.Dir(filepath.Dir(projAdmin))}
	hostMounts, err := hostBase{}.mountBase(ctx, rt, proj, proj, t.TempDir(), engineContainerSpec{}, f)
	require.NoError(t, err)
	assert.Contains(t, targetsOf(hostMounts), "/ctr"+filepath.Join(proj, ".git"))
	assert.Contains(t, targetsOf(hostMounts), "/ctr"+filepath.Join(projAdmin, "gitdir"))
}

// hostBehind answers where a container path lands on the host under a mount
// plan, the way the runtime stacks bind mounts: the mount whose target is the
// deepest ancestor of the path wins. ok is false when no mount reaches it.
func hostBehind(mounts []mount, containerPath string) (host string, readOnly, ok bool) {
	best := -1
	for i, m := range mounts {
		rel, err := filepath.Rel(m.Container, containerPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if best < 0 || len(m.Container) > len(mounts[best].Container) {
			best = i
		}
	}
	if best < 0 {
		return "", false, false
	}
	rel, _ := filepath.Rel(mounts[best].Container, containerPath)
	return filepath.Join(mounts[best].Host, rel), mounts[best].ReadOnly, true
}

// A container run must see only THIS checkout's git data. The shared common
// dir holds the worktrees/ registry of every other checkout, and an
// in-container `git worktree prune` (or gc's automatic prune) deletes any
// registration whose back-pointer does not resolve in there — which, where
// the runtime renames paths, is every other one. So the registry is not
// reachable from the container at all, on any mapping, while everything git
// needs to work in this checkout still resolves read-write to the host: its
// own admin dir, and the common dir's config, packed-refs, hooks, objects
// and refs (whose lock-and-rename updates need the common dir itself
// mounted, not its files one by one).
func TestMountBase_HidesOtherWorktreesRegistry(t *testing.T) {
	ctx := context.Background()
	runtimes := map[string]Runtime{
		"renaming": fakeRuntime{name: "docker", binary: "docker", available: true},
		"identity": mapperRuntime{fakeRuntime: fakeRuntime{name: "docker", binary: "docker", available: true}, m: identityMapper{}},
	}
	pointers := map[string]string{"absolute": "", "relative": filepath.Join("..", "repo", ".git", "worktrees", "wt")}
	for rtName, rt := range runtimes {
		for _, base := range []string{"worktree", "host"} {
			for ptrName, pointer := range pointers {
				t.Run(rtName+"/"+base+"/"+ptrName, func(t *testing.T) {
					dir, admin := linkedCheckout(t, pointer)
					registry := filepath.Dir(admin)
					common := filepath.Dir(registry)
					other := filepath.Join(registry, "other")
					require.NoError(t, os.MkdirAll(other, 0o755))
					require.NoError(t, os.WriteFile(filepath.Join(other, "gitdir"), []byte("/elsewhere/.git\n"), 0o644))
					for _, d := range []string{"objects", "refs", "hooks"} {
						require.NoError(t, os.MkdirAll(filepath.Join(common, d), 0o755))
					}
					for _, f := range []string{"config", "packed-refs", "HEAD"} {
						require.NoError(t, os.WriteFile(filepath.Join(common, f), nil, 0o644))
					}
					f := &git.Fake{CommonDirValue: common}

					var mounts []mount
					var err error
					if base == "worktree" {
						mounts, err = worktreeBase{wt: NewWorktree(f)}.mountBase(ctx, rt, t.TempDir(), dir, t.TempDir(), engineContainerSpec{}, f)
					} else {
						mounts, err = hostBase{}.mountBase(ctx, rt, dir, dir, t.TempDir(), engineContainerSpec{}, f)
					}
					require.NoError(t, err)

					_, ro, ok := hostBehind(mounts, mapped(t, rt, registry))
					require.True(t, ok)
					assert.True(t, ro, "the registry is read-only, so a new registration fails instead of vanishing into scratch")
					host, _, ok := hostBehind(mounts, mapped(t, rt, other))
					if ok {
						_, statErr := os.Stat(host)
						assert.ErrorIs(t, statErr, os.ErrNotExist,
							"another worktree's registration must not be reachable from the container (it resolved to %s)", host)
					}
					for _, need := range []string{admin, filepath.Join(admin, "HEAD"),
						filepath.Join(common, "config"), filepath.Join(common, "packed-refs"),
						filepath.Join(common, "hooks"), filepath.Join(common, "objects"), filepath.Join(common, "refs")} {
						host, ro, ok := hostBehind(mounts, mapped(t, rt, need))
						require.True(t, ok, "%s must be mounted", need)
						assert.Equal(t, need, host, "%s must resolve to the host's own copy", need)
						assert.False(t, ro, "%s must be writable: git updates it in place", need)
					}
				})
			}
		}
	}
}
