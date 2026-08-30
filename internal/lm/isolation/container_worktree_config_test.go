package isolation

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// newCell builds the two host trees projectConfigMount arbitrates between: the
// LIVE project and an already-materialized worktree checkout. Both are real
// directories because the function under test decides by stat()ing them —
// git.Fake creates no checkout, so a fixture that skipped the mkdir would be
// answering about a tree that does not exist.
func newCell(t *testing.T) (projectDir, worktreeDir string) {
	t.Helper()
	root := t.TempDir()
	projectDir = filepath.Join(root, "project")
	worktreeDir = filepath.Join(root, "ctxloom-wt-member")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	require.NoError(t, os.MkdirAll(worktreeDir, 0o755))
	return projectDir, worktreeDir
}

// writeConfigTree puts a .ctxloom under dir with one identifiable file in it,
// and returns the config dir. The file matters: an empty directory would let a
// delivery assertion pass against a tree carrying no configuration at all.
func writeConfigTree(t *testing.T, dir, marker string) string {
	t.Helper()
	cfg := filepath.Join(dir, paths.AppDirName)
	require.NoError(t, os.MkdirAll(cfg, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "config.yaml"), []byte(marker), 0o644))
	return cfg
}

// TestProjectConfigMount pins the whole decision table of the worktree cell's
// config delivery — the fix for the {worktree, container} run that died as an
// unreadable go-plugin handshake because the in-container ctxloom found no
// config in its checkout and refused to start (exit 3).
//
// Each subtest names the fact it would lose. The delivery arm is the defect
// itself; the two skip arms are the ways an over-eager delivery would do harm
// that the refusal never did.
func TestProjectConfigMount(t *testing.T) {
	rt := fakeRuntime{name: "docker", binary: "docker", available: true}

	t.Run("gitignored project config is delivered into the checkout", func(t *testing.T) {
		projectDir, worktreeDir := newCell(t)
		source := writeConfigTree(t, projectDir, "project config")
		// The premise, asserted rather than assumed: this is exactly the state
		// a gitignored .ctxloom produces — present in the project, absent from
		// a fresh checkout. Without this the "delivered" assertion below could
		// be satisfied by a checkout that already had one.
		require.NoDirExists(t, filepath.Join(worktreeDir, paths.AppDirName),
			"premise: the checkout starts with no config, which is what makes the in-container ctxloom refuse")

		mount, ok, err := projectConfigMount(rt, projectDir, worktreeDir)

		require.NoError(t, err)
		require.True(t, ok, "a project config the checkout lacks is the whole case this delivery exists for")
		assert.Equal(t, source, mount.Host, "the LIVE project's config tree is the source, not the checkout's")
		assert.True(t, mount.ReadOnly,
			"read-write would punch through the worktree axis's promise that the live project is not the cell's to change")

		// The container side must name where the CHECKOUT appears in the
		// container's namespace, not its host path. fakeRuntime's mapper is
		// deliberately non-identity, so a target built from the host path is
		// distinguishable from one routed through the mapper — under a real
		// non-identity mapper that difference lands the config outside the
		// checkout and the cell refuses exactly as before.
		wantTarget := filepath.Join(rt.mapper().toContainer(worktreeDir), paths.AppDirName)
		assert.Equal(t, wantTarget, mount.Container,
			"the mount target is the checkout's CONTAINER path; using the host path delivers the config nowhere the cell will look")
		assert.NotEqual(t, filepath.Join(worktreeDir, paths.AppDirName), mount.Container,
			"guard on the guard: if these ever coincide the assertion above stops testing the mapper at all")

		// Pre-created host-side, as the invoking user: a target the daemon has
		// to create is created as ROOT under a rootful daemon, leaving the
		// host-side WIP-safe teardown unable to remove it.
		assert.DirExists(t, filepath.Join(worktreeDir, paths.AppDirName),
			"the mountpoint must exist in the checkout before the daemon binds over it")
	})

	t.Run("a checkout with its own committed config keeps it", func(t *testing.T) {
		projectDir, worktreeDir := newCell(t)
		writeConfigTree(t, projectDir, "parent config")
		own := writeConfigTree(t, worktreeDir, "the cell's own config")

		mount, ok, err := projectConfigMount(rt, projectDir, worktreeDir)

		require.NoError(t, err)
		assert.False(t, ok,
			"own .ctxloom always wins (worktreeSignpost's precedence); shadowing it makes a deliberately separate project silently adopt its parent's config")
		assert.Equal(t, Mount{}, mount, "no mount, not a zero-valued one that some caller might still append")

		// Not just "no mount was returned" — the tree itself must be untouched.
		got, err := os.ReadFile(filepath.Join(own, "config.yaml"))
		require.NoError(t, err)
		assert.Equal(t, "the cell's own config", string(got), "the checkout's own config must survive intact")
	})

	t.Run("a project with no config delivers nothing", func(t *testing.T) {
		projectDir, worktreeDir := newCell(t)

		mount, ok, err := projectConfigMount(rt, projectDir, worktreeDir)

		require.NoError(t, err)
		assert.False(t, ok, "there is nothing to deliver; the refusal that follows is then about the project itself")
		assert.Equal(t, Mount{}, mount)
		assert.NoDirExists(t, filepath.Join(worktreeDir, paths.AppDirName),
			"no source means no mountpoint either — creating an empty .ctxloom would make the cell resolve to a config that does not exist")
	})

	t.Run("a project .ctxloom that is a FILE is not delivered", func(t *testing.T) {
		projectDir, worktreeDir := newCell(t)
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, paths.AppDirName), []byte("not a dir"), 0o644))

		mount, ok, err := projectConfigMount(rt, projectDir, worktreeDir)

		require.NoError(t, err)
		assert.False(t, ok, "a bind mount of a file over a directory mountpoint is not a config tree")
		assert.Equal(t, Mount{}, mount)
	})
}

// TestWorktreeBase_MountBaseCarriesTheConfigMount closes the wiring gap the
// unit tests above cannot see: projectConfigMount can be entirely correct and
// still deliver nothing if mountBase drops what it returns. Dropping the config
// mount from the returned slice leaves every assertion in TestProjectConfigMount
// green while the defect is fully restored.
//
// It also pins that delivery is ADDITIVE — the gitdir mirror is what makes git
// resolve inside the cell at all, so a change that returned the config mount
// INSTEAD of it would trade one broken cell for another.
func TestWorktreeBase_MountBaseCarriesTheConfigMount(t *testing.T) {
	ctx := context.Background()
	rt := fakeRuntime{name: "docker", binary: "docker", available: true}
	projectDir, worktreeDir := newCell(t)
	source := writeConfigTree(t, projectDir, "project config")

	f := &git.Fake{}
	base := worktreeBase{wt: NewWorktree(f, "mock")}

	mounts, cleanup, err := base.mountBase(ctx, rt, projectDir, worktreeDir, t.TempDir(), engineContainerSpec{}, f)

	require.NoError(t, err)
	assert.Nil(t, cleanup, "the mountpoint dies with the ephemeral checkout, so there is nothing to unwind")

	// The gitdir mirror, unchanged: without it the cell's .git pointer file
	// resolves to nothing and git is broken in-container.
	assert.Contains(t, mounts, rt.ExposeMapped(filepath.Join(worktreeDir, ".git"), false),
		"the gitdir mirror must survive the addition of the config mount")

	wantTarget := filepath.Join(rt.mapper().toContainer(worktreeDir), paths.AppDirName)
	assert.Contains(t, mounts, Mount{Host: source, Container: wantTarget, ReadOnly: true},
		"the config mount must reach the run spec; building it and discarding it restores the defect exactly")
	assert.Len(t, mounts, 2, "exactly the gitdir mirror and the config delivery")
}

// TestWorktreeBase_MountBaseOmitsTheConfigMountWhenThereIsNothingToDeliver is
// the negative half of the wiring: mountBase must return the git mirror ALONE
// rather than a zero-valued Mount{} appended for shape. A Mount{Host:"",
// Container:""} in a run spec is not inert — it renders as a bind argument the
// runtime would reject or, worse, interpret.
func TestWorktreeBase_MountBaseOmitsTheConfigMountWhenThereIsNothingToDeliver(t *testing.T) {
	ctx := context.Background()
	rt := fakeRuntime{name: "docker", binary: "docker", available: true}
	projectDir, worktreeDir := newCell(t)

	f := &git.Fake{}
	base := worktreeBase{wt: NewWorktree(f, "mock")}

	mounts, _, err := base.mountBase(ctx, rt, projectDir, worktreeDir, t.TempDir(), engineContainerSpec{}, f)

	require.NoError(t, err)
	require.Len(t, mounts, 1, "a project with no config contributes no mount at all")
	assert.Equal(t, rt.ExposeMapped(filepath.Join(worktreeDir, ".git"), false), mounts[0])
	assert.NotContains(t, mounts, Mount{}, "no empty mount may ride along for shape")
}
