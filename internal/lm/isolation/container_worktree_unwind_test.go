package isolation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/git"
)

// TestWorktreeBase_PrepareBaseUnwindsWhatItCreated pins worktreeBase.prepareBase's
// two failure exits, which nothing reached before: mutating either conditional to
// its negation left the suite green.
//
// They are not symmetric, and that asymmetry IS the contract:
//
//   - the worktree never came up  -> return the error, unwind NOTHING (there is
//     nothing to unwind, and removing a checkout that failed to appear would be
//     the bug);
//   - the gitdir mount failed AFTER the worktree exists -> tear the worktree
//     down BEFORE returning.
//
// The second is the one that matters. prepareBase is, by its own comment, "the
// ONE place a botched collapse could leak a checkout": the caller only removes
// the shared scratch, so a checkout this function creates and abandons is leaked
// permanently. Asserting the error alone would pass while the checkout leaks —
// so both cases assert what happened to the WORKTREE, not just what came back.
//
// TestContainerWorktree_PrepareDegradesBeforeWorktree does not cover this: it
// fails at the container gate, before prepareBase is ever entered, so its "no
// worktree is created" holds because the code never ran.
func TestWorktreeBase_PrepareBaseUnwindsWhatItCreated(t *testing.T) {
	ctx := context.Background()
	rt := fakeRuntime{name: "docker", binary: "docker", available: true}
	scratch := t.TempDir()
	proj := t.TempDir()

	t.Run("worktree never came up: error out, unwind nothing", func(t *testing.T) {
		boom := errors.New("worktree add refused")
		f := &git.Fake{AddErr: boom}

		dir, mounts, cleanup, err := worktreeBase{wt: NewWorktree(f, "mock")}.
			prepareBase(ctx, rt, proj, "m", scratch, engineContainerSpec{}, f)

		require.Error(t, err)
		assert.ErrorIs(t, err, boom, "the worktree's own failure must reach the caller intact")
		assert.Empty(t, dir)
		assert.Nil(t, mounts)
		assert.Nil(t, cleanup, "a failed base hands back no cleanup for the caller to run")
		assert.Empty(t, f.Removed,
			"nothing was created, so nothing may be removed — tearing down here would remove a checkout that never appeared")
	})

	t.Run("gitdir mount failed after the worktree exists: tear the worktree down", func(t *testing.T) {
		boom := errors.New("common dir unreadable")
		f := &git.Fake{CommonDirErr: boom}

		dir, mounts, cleanup, err := worktreeBase{wt: NewWorktree(f, "mock")}.
			prepareBase(ctx, rt, proj, "m", scratch, engineContainerSpec{}, f)

		require.Error(t, err)
		assert.ErrorIs(t, err, boom, "the resolve failure must reach the caller intact")
		assert.Empty(t, dir)
		assert.Nil(t, mounts)
		assert.Nil(t, cleanup)

		// THE ASSERTION THIS TEST EXISTS FOR. The worktree was created moments
		// earlier; if prepareBase returns without removing it, the caller's
		// scratch removal will not, and the checkout is leaked for good.
		require.Len(t, f.Removed, 1, "the worktree created by this call must be torn down before returning")
		created := f.Removed[0]
		assert.True(t, filepath.IsAbs(created), "the removed path is the real checkout, not a placeholder")
		_, statErr := os.Stat(created)
		assert.True(t, os.IsNotExist(statErr), "the checkout must not survive the failed prepare")
	})
}
