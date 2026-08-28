package isolation

import (
	"context"
	"errors"
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

		mounts, mountCleanup, err := base.mountBase(ctx, rt, dir, scratch, engineContainerSpec{}, f)
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
