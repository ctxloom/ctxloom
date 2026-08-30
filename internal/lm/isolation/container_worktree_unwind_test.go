package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/git"
)

// TestWorktreeBase_UnwindsWhatItCreated pins worktreeBase's two failure exits,
// which nothing reached before: mutating either conditional to its negation left
// the suite green.
//
// They are not symmetric, and that asymmetry IS the contract:
//
//   - the worktree never came up (resolveBase) -> return the error, unwind
//     NOTHING (there is nothing to unwind, and removing a checkout that failed
//     to appear would be the bug);
//   - the gitdir mount failed AFTER the worktree exists (mountBase) -> the
//     checkout must not survive the failed prepare.
//
// The second is the one that matters, and the split between resolution and
// mapping moved WHO discharges it. Tearing the checkout down inside mountBase
// would now be wrong: once resolveBase returns, the checkout belongs to the
// containerWorkspace, and the composed prepare (prepareWorkspace) Cleanup()s
// that workspace when the mapping fails. So the ownership handoff is what this
// subtest walks — resolve, fail to mount, Cleanup — and it still asserts what
// happened to the WORKTREE, not just what came back. Asserting the error alone
// would pass while the checkout leaks permanently.
//
// TestContainerWorktree_PrepareDegradesBeforeWorktree does not cover this: it
// fails at the container gate, before either half is entered, so its "no
// worktree is created" holds because the code never ran.
func TestWorktreeBase_UnwindsWhatItCreated(t *testing.T) {
	ctx := context.Background()
	rt := fakeRuntime{name: "docker", binary: "docker", available: true}
	scratch := t.TempDir()
	proj := t.TempDir()

	t.Run("worktree never came up: error out, unwind nothing", func(t *testing.T) {
		boom := errors.New("worktree add refused")
		f := &git.Fake{AddErr: boom}

		dir, cleanup, err := worktreeBase{wt: NewWorktree(f, "mock")}.resolveBase(ctx, proj, "m")

		require.Error(t, err)
		assert.ErrorIs(t, err, boom, "the worktree's own failure must reach the caller intact")
		assert.Empty(t, dir)
		assert.Nil(t, cleanup, "a failed resolve hands back no cleanup for the caller to run")
		assert.Empty(t, f.Removed,
			"nothing was created, so nothing may be removed — tearing down here would remove a checkout that never appeared")
	})

	t.Run("gitdir mount failed after the worktree exists: the checkout does not survive", func(t *testing.T) {
		boom := errors.New("common dir unreadable")
		f := &git.Fake{CommonDirErr: boom}
		base := worktreeBase{wt: NewWorktree(f, "mock")}

		dir, cleanup, err := base.resolveBase(ctx, proj, "m")
		require.NoError(t, err, "premise: the checkout comes up — the failure under test is in the MAPPING")
		// The premise is asserted against the git seam's RECORD, not the
		// filesystem: git.Fake.WorktreeAdd never creates a directory, so
		// DirExists/os.Stat here would be satisfied by a checkout that was never
		// made — the absence-satisfies-absence shape that makes a teardown
		// assertion vacuous.
		require.Len(t, f.Worktrees, 1, "premise: a checkout exists to be leaked")
		require.Equal(t, dir, f.Worktrees[0].Path, "premise: it is the dir resolveBase handed back")

		// The workspace the composed prepare would have built: it owns the
		// checkout from resolveBase on, which is why mountBase does not tear it
		// down itself.
		ws := &containerWorkspace{dir: dir, scratchRoot: scratch, agentID: "m", baseCleanup: cleanup}

		mounts, mountCleanup, err := base.mountBase(ctx, rt, proj, dir, scratch, engineContainerSpec{}, f)
		require.Error(t, err)
		assert.ErrorIs(t, err, boom, "the mapping failure must reach the caller intact")
		assert.Nil(t, mounts)
		assert.Nil(t, mountCleanup, "a failed mapping hands back no cleanup for the caller to run")
		assert.Empty(t, f.Removed,
			"the mapping must NOT tear down a checkout it does not own — the workspace does")

		// THE ASSERTION THIS TEST EXISTS FOR. The checkout was created moments
		// earlier; if the failed-mapping unwind does not remove it, nothing will,
		// and the checkout is leaked for good.
		require.NoError(t, ws.Cleanup())
		require.Len(t, f.Removed, 1, "the checkout created by this prepare must be torn down by the unwind")
		assert.Equal(t, dir, f.Removed[0], "the removed path is the checkout, not some other tree")
		assert.Empty(t, f.Worktrees, "the checkout must not survive the failed prepare")
	})
}

// TestContainerWorktree_FailedMappingDoesNotLeakTheCheckout closes the half of
// the unwind contract that moved when resolution and containerization were
// split. TestWorktreeBase_UnwindsWhatItCreated proves the workspace's Cleanup
// removes the checkout; it says nothing about whether the composed prepare
// actually CALLS that Cleanup when the mapping fails. Deleting that call leaks a
// checkout permanently and leaves the caller a nil workspace with no handle to
// remove it — the precise failure the old prepareBase teardown existed to
// prevent — so it is pinned here, through the real Container.PrepareWorkspace.
func TestContainerWorktree_FailedMappingDoesNotLeakTheCheckout(t *testing.T) {
	ctx := context.Background()

	fake := t.TempDir()
	script := filepath.Join(fake, "fake-docker")
	labels := fmt.Sprintf(`{"ctxloom.provenance":%q}`, HostProvenanceDigest(""))
	writeFakeRuntimeScript(t, script, filepath.Join(fake, "builds.log"), fake, labels)
	require.NoError(t, os.WriteFile(filepath.Join(fake, "ctxloom-agent-unwind-test_latest"), nil, 0o644))

	// The mapping fails: the checkout's git common dir cannot be resolved, so no
	// gitdir mirror mount can be built. Resolution has already created the
	// checkout by then, which is what makes this the leak-prone path.
	boom := errors.New("common dir unreadable")
	f := &git.Fake{CommonDirErr: boom}

	c := Container{
		runtime: fakeRuntime{name: "docker", binary: script, available: true},
		image:   "ctxloom-agent-unwind-test:latest",
		engineSpec: engineContainerSpec{
			engineInstall: []byte("RUN echo fake-install\n"),
			resolveAuth: func(string, string) (containerAuth, bool) {
				return containerAuth{mode: authEnv, envPassthrough: []string{"X"}}, true
			},
		},
		binaryPath: defaultContainerBinary,
		home:       defaultContainerHome,
		socketDir:  defaultContainerSocketDir,
		base:       worktreeBase{wt: NewWorktree(f, "mock")},
	}

	ws, err := c.PrepareWorkspace(ctx, t.TempDir(), "member-unwind")
	require.Error(t, err, "a mapping that cannot be built must fail the prepare, never launch a broken container")
	assert.ErrorIs(t, err, boom, "the mapping failure must reach the caller intact")
	assert.Nil(t, ws, "a failed prepare hands back no workspace")

	require.Len(t, f.Removed, 1,
		"THE ASSERTION: the checkout resolution created must be torn down by the failed prepare — nothing else holds a handle to it")
	assert.Empty(t, f.Worktrees, "the checkout must not survive the failed prepare")
}
