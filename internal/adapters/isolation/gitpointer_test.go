package isolation

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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
	dir, admin, _ = linkedCheckoutIn(t, pointer)
	return dir, admin
}

// linkedCheckoutIn is linkedCheckout also returning the common dir.
func linkedCheckoutIn(t *testing.T, pointer string) (dir, admin, common string) {
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
	return dir, admin, filepath.Join(root, "repo", ".git")
}

// commonOf is the common dir linkedCheckout lays the admin dir under.
func commonOf(admin string) string { return filepath.Dir(filepath.Dir(admin)) }

// Under a mapper that renames paths, the checkout's pointer names a host
// path git in the container cannot open. A read-only pointer naming the
// MAPPED admin dir is mounted over it, and the admin dir's back-pointer is
// mapped the same way so an in-container `git worktree prune` does not take
// the checkout for gone.
func TestGitPointerMounts_RewritesBothPointers(t *testing.T) {
	dir, admin := linkedCheckout(t, "")
	rt := fakeRuntime{name: "docker", available: true} // maps under /ctr
	scratch := t.TempDir()

	mounts, err := gitPointerMounts(rt, dir, commonOf(admin), scratch)
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
	dir, admin := linkedCheckout(t, "")
	mounts, err := gitPointerMounts(identity, dir, commonOf(admin), t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, mounts, "identity: the host pointer already resolves in-container")

	rel, _, relCommon := linkedCheckoutIn(t, "../repo/.git/worktrees/wt")
	mounts, err = gitPointerMounts(fakeRuntime{name: "docker"}, rel, relCommon, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, mounts, "a relative pointer resolves under a prefix-preserving mapping")

	mainRepo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(mainRepo, ".git"), 0o755))
	mounts, err = gitPointerMounts(fakeRuntime{name: "docker"}, mainRepo, filepath.Join(mainRepo, ".git"), t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, mounts)

	none := t.TempDir()
	mounts, err = gitPointerMounts(fakeRuntime{name: "docker"}, none, none, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, mounts)
}

// A .git file that is not a gitdir pointer is refused, not guessed at.
func TestGitPointerMounts_MalformedPointerFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("not a pointer\n"), 0o644))
	_, err := gitPointerMounts(fakeRuntime{name: "docker"}, dir, dir, t.TempDir())
	require.Error(t, err)
}

// A pointer whose admin dir the child cannot name fails the workspace: the
// admin dir is named through the common dir's mount, so a common dir the
// policy cannot route leaves it unnamed.
func TestGitPointerMounts_UnroutableAdminFails(t *testing.T) {
	dir, admin := linkedCheckout(t, "")
	rt := mapperRuntime{fakeRuntime: fakeRuntime{name: "docker"}, m: unroutableMapper{under: commonOf(admin)}}
	_, err := gitPointerMounts(rt, dir, commonOf(admin), t.TempDir())
	require.ErrorIs(t, err, errNoRoute)
}

// A placement that moves the checkout and the common dir to unrelated places
// still yields pointers the child can follow: each is named through the
// mount that makes it visible, not placed on its own.
func TestGitPointerMounts_NamesEachSideThroughItsOwnMount(t *testing.T) {
	dir, admin := linkedCheckout(t, "")
	common := commonOf(admin)
	rt := mapperRuntime{fakeRuntime: fakeRuntime{name: "docker"}, m: rootsMapper{dir: "/agent/work", common: "/agent/git"}.with(dir, common)}
	mounts, err := gitPointerMounts(rt, dir, common, t.TempDir())
	require.NoError(t, err)
	require.Len(t, mounts, 2)
	rel, err := filepath.Rel(common, admin)
	require.NoError(t, err)
	assert.Equal(t, "/agent/work/.git", mounts[0].Container)
	assert.Equal(t, "gitdir: /agent/git/"+filepath.ToSlash(rel)+"\n", readFile(t, mounts[0].Host))
	assert.Equal(t, "/agent/git/"+filepath.ToSlash(rel)+"/gitdir", mounts[1].Container)
	assert.Equal(t, "/agent/work/.git\n", readFile(t, mounts[1].Host))
}

// rootsMapper places two exact roots at fixed child paths and refuses
// anything else: a per-root placement policy no prefix rule expresses.
type rootsMapper struct{ dir, common, hostDir, hostCommon string }

func (m rootsMapper) with(hostDir, hostCommon string) rootsMapper {
	m.hostDir, m.hostCommon = hostDir, hostCommon
	return m
}

func (m rootsMapper) toContainer(host string) (string, error) {
	switch host {
	case m.hostDir:
		return m.dir, nil
	case m.hostCommon:
		return m.common, nil
	}
	return "", errNoRoute
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
	wtMounts, err := worktreeBase{wt: sessionWorktree(t, f)}.mountBase(ctx, rt, t.TempDir(), dir, t.TempDir(), engineContainerSpec{}, f)
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
// own admin dir, and the common dir's config, packed-refs, objects and refs
// (whose lock-and-rename updates need the common dir itself mounted, not its
// files one by one). Hooks are readable but not writable
// (TestGitDirMounts_MainHooksIndexAndHeadAreReadOnly).
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
						mounts, err = worktreeBase{wt: sessionWorktree(t, f)}.mountBase(ctx, rt, t.TempDir(), dir, t.TempDir(), engineContainerSpec{}, f)
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
						filepath.Join(common, "objects"), filepath.Join(common, "refs")} {
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

// gitCommonFixture fills a linked checkout's common dir with the files git
// keeps there; hooks, when false, is left absent.
func gitCommonFixture(t *testing.T, common string, hooks bool) {
	t.Helper()
	for _, d := range []string{"objects", "refs"} {
		require.NoError(t, os.MkdirAll(filepath.Join(common, d), 0o755))
	}
	if hooks {
		require.NoError(t, os.MkdirAll(filepath.Join(common, "hooks"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(common, "hooks", "pre-commit"), []byte("#!/bin/sh\n"), 0o755))
	}
	for _, f := range []string{"config", "packed-refs", "HEAD", "index"} {
		require.NoError(t, os.WriteFile(filepath.Join(common, f), nil, 0o644))
	}
}

// Hooks in the common dir run ON THE HOST at the next git operation in any
// checkout, and the main checkout's index and HEAD are its own, not this
// checkout's. So a linked checkout's container sees all three read-only,
// shadowing the read-write common-dir mount, while this checkout's own HEAD
// and index (in its admin dir) and the common dir's config, packed-refs,
// objects and refs stay writable: committing from here needs them.
func TestGitDirMounts_MainHooksIndexAndHeadAreReadOnly(t *testing.T) {
	runtimes := map[string]Runtime{
		"renaming": fakeRuntime{name: "docker", binary: "docker", available: true},
		"identity": mapperRuntime{fakeRuntime: fakeRuntime{name: "docker", binary: "docker", available: true}, m: identityMapper{}},
	}
	for rtName, rt := range runtimes {
		t.Run(rtName, func(t *testing.T) {
			dir, admin, common := linkedCheckoutIn(t, "")
			gitCommonFixture(t, common, true)
			for _, f := range []string{"HEAD", "index"} {
				require.NoError(t, os.WriteFile(filepath.Join(admin, f), nil, 0o644))
			}
			mounts, err := gitDirMounts(context.Background(), rt, &git.Fake{CommonDirValue: common}, dir, t.TempDir())
			require.NoError(t, err)

			for _, ro := range []string{filepath.Join(common, "hooks"), filepath.Join(common, "hooks", "pre-commit"),
				filepath.Join(common, "index"), filepath.Join(common, "HEAD")} {
				host, readOnly, ok := hostBehind(mounts, mapped(t, rt, ro))
				require.True(t, ok, "%s must be mounted", ro)
				assert.Equal(t, ro, host, "%s must resolve to the host's own copy", ro)
				assert.True(t, readOnly, "%s must be read-only", ro)
			}
			for _, rw := range []string{filepath.Join(admin, "HEAD"), filepath.Join(admin, "index"),
				filepath.Join(common, "config"), filepath.Join(common, "packed-refs"),
				filepath.Join(common, "objects"), filepath.Join(common, "refs")} {
				host, readOnly, ok := hostBehind(mounts, mapped(t, rt, rw))
				require.True(t, ok, "%s must be mounted", rw)
				assert.Equal(t, rw, host, "%s must resolve to the host's own copy", rw)
				assert.False(t, readOnly, "%s must be writable: committing from this checkout updates it", rw)
			}

			// A runtime mounts in list order, so each overlay must come after
			// the common-dir mount it shadows.
			commonAt := slices.IndexFunc(mounts, func(m mount) bool { return m.Host == common })
			require.GreaterOrEqual(t, commonAt, 0, "the common dir is mounted")
			for _, name := range gitLinkedReadOnlyEntries {
				at := slices.IndexFunc(mounts, func(m mount) bool { return m.Host == filepath.Join(common, name) })
				require.GreaterOrEqual(t, at, 0, "%s is overlaid", name)
				assert.Greater(t, at, commonAt, "%s's overlay comes after the common-dir mount it shadows", name)
			}
		})
	}
}

// A common dir without hooks/ would let the container create one through the
// read-write mount, and git on the host would run what it put there. The
// directory is created first so it can be mounted read-only. A missing index
// is NOT created: an empty file is a corrupt index to the host's git, so it is
// left unshadowed.
func TestGitDirMounts_MissingHooksDirIsCreatedMissingIndexIsNot(t *testing.T) {
	rt := fakeRuntime{name: "docker", binary: "docker", available: true}
	dir, _, common := linkedCheckoutIn(t, "")
	gitCommonFixture(t, common, false)
	require.NoError(t, os.Remove(filepath.Join(common, "index")))

	mounts, err := gitDirMounts(context.Background(), rt, &git.Fake{CommonDirValue: common}, dir, t.TempDir())
	require.NoError(t, err)

	hooks := filepath.Join(common, "hooks")
	assert.DirExists(t, hooks, "the hooks dir is created so it can be shadowed")
	assert.Contains(t, mounts, bind(hooks, mapped(t, rt, hooks), true))
	assert.NoFileExists(t, filepath.Join(common, "index"), "a missing index is never created")
	for _, m := range mounts {
		assert.NotEqual(t, filepath.Join(common, "index"), m.Host, "a missing index has no bind source")
	}
}

// The hooks dir is created as git creates its own directories, so the host's
// git and the user can still manage it.
func TestGitDirMounts_CreatedHooksDirHasGitsMode(t *testing.T) {
	dir, _, common := linkedCheckoutIn(t, "")
	gitCommonFixture(t, common, false)
	_, err := gitDirMounts(context.Background(), fakeRuntime{name: "docker"}, &git.Fake{CommonDirValue: common}, dir, t.TempDir())
	require.NoError(t, err)

	reference := filepath.Join(t.TempDir(), "reference")
	require.NoError(t, os.Mkdir(reference, 0o755)) // the same umask applies to both
	want, err := os.Stat(reference)
	require.NoError(t, err)
	got, err := os.Stat(filepath.Join(common, "hooks"))
	require.NoError(t, err)
	assert.Equal(t, want.Mode().Perm(), got.Mode().Perm())
}

// An entry the child cannot name through the mount it is handed fails the
// workspace rather than going unshadowed.
func TestGitReadOnlyOverlays_UnroutableEntryFails(t *testing.T) {
	_, _, common := linkedCheckoutIn(t, "")
	gitCommonFixture(t, common, true)
	elsewhere := t.TempDir()
	mounts, err := gitReadOnlyOverlays(fakeRuntime{name: "docker"}, common, bind(elsewhere, elsewhere, false), gitLinkedReadOnlyEntries)
	require.Error(t, err)
	assert.Nil(t, mounts, "no partial overlay set escapes")
}

// A submodule's (or a --separate-git-dir checkout's) .git pointer names the
// common dir itself, so the index and HEAD there are THIS checkout's own:
// only hooks/ is shadowed, and committing in here still updates the rest.
func TestGitDirMounts_OwnGitDirKeepsIndexAndHeadWritable(t *testing.T) {
	rt := fakeRuntime{name: "docker", binary: "docker", available: true}
	root := t.TempDir()
	dir := filepath.Join(root, "sub")
	common := filepath.Join(root, "super", ".git", "modules", "sub")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	gitCommonFixture(t, common, true)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+common+"\n"), 0o644))

	mounts, err := gitDirMounts(context.Background(), rt, &git.Fake{CommonDirValue: common}, dir, t.TempDir())
	require.NoError(t, err)

	hooks := filepath.Join(common, "hooks")
	host, readOnly, ok := hostBehind(mounts, mapped(t, rt, hooks))
	require.True(t, ok, "hooks must be mounted")
	assert.Equal(t, hooks, host)
	assert.True(t, readOnly, "hooks run on the host and must be read-only")
	for _, rw := range []string{filepath.Join(common, "index"), filepath.Join(common, "HEAD")} {
		host, readOnly, ok := hostBehind(mounts, mapped(t, rt, rw))
		require.True(t, ok, "%s must be mounted", rw)
		assert.Equal(t, rw, host, "%s must resolve to the host's own copy", rw)
		assert.False(t, readOnly, "%s is this checkout's own and must be writable", rw)
	}
}

// A .git that is not a gitdir pointer fails the workspace before anything is
// written to the host: whose index and HEAD the common dir holds cannot be
// told.
func TestGitDirMounts_MalformedPointerFailsBeforeTouchingTheHost(t *testing.T) {
	dir, _, common := linkedCheckoutIn(t, "")
	gitCommonFixture(t, common, false)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("not a pointer\n"), 0o644))

	_, err := gitDirMounts(context.Background(), fakeRuntime{name: "docker"}, &git.Fake{CommonDirValue: common}, dir, t.TempDir())
	require.Error(t, err)
	assert.NoDirExists(t, filepath.Join(common, "hooks"), "nothing is created for a checkout that is refused")
}
