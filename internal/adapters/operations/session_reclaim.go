package operations

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// sessionTriage is the reaper's triage for this adapter: the worktree
// question (worktreeTriage), and — on apply, for a session it did not spare
// — the deletion of the session's macOS Keychain credential items
// (isolation.ReapKeychainItems). The items are keyed by the session home's
// path, so the reap that removes the home is the one clock that can find
// them; a failure there is reported and does not spare the session, because
// the disk is still the reaper's to reclaim.
func sessionTriage(g git.Git, reapKeychain func(harp string) error) sessions.Triage {
	worktrees := worktreeTriage(g)
	return func(ctx context.Context, harp string, probe sessions.LockProbe, apply bool) (string, error) {
		spared, err := worktrees(ctx, harp, probe, apply)
		if err != nil || spared != "" || !apply {
			return spared, err
		}
		if err := reapKeychain(harp); err != nil {
			clidiag.Warn("ctxloom", "reap %s: its Keychain credential item(s) could not be deleted: %v", harp, err)
		}
		return "", nil
	}
}

// worktreeTriage classifies a session's scratch worktrees through the very
// same classifier the worktree leaf uses (isolation.ClassifyHarpWorktrees),
// tears the safe ones down on apply, and spares the session if any survive.
// The reaper's own probe is used rather than a fresh one: the reap holds
// this harp's lock, and probing from under its own hold would read it as a
// live owner.
func worktreeTriage(g git.Git) sessions.Triage {
	return func(ctx context.Context, harp string, probe sessions.LockProbe, apply bool) (string, error) {
		candidates, err := isolation.ClassifyHarpWorktrees(ctx, g, harp, lockProbe(probe))
		if err != nil {
			return "", fmt.Errorf("its scratch worktrees could not be classified: %w", err)
		}
		if apply {
			candidates = isolation.ReapWorktrees(ctx, g, candidates)
		}
		if held, why := worktreeHoldingWork(candidates); held {
			return why, nil
		}
		return "", nil
	}
}

// worktreeHoldingWork reports whether any of a session's scratch worktrees is
// still standing, and why the first such one was left.
//
// ANY survivor spares the whole session. An ephemeral dir that still holds a
// checkout carrying uncommitted work cannot be removed without taking that
// work with it. Reporting beats reclaiming on every tie.
func worktreeHoldingWork(candidates []isolation.WorktreeCandidate) (bool, string) {
	for _, wt := range candidates {
		if wt.Verdict == isolation.VerdictReaped {
			continue
		}
		reason := wt.Reason
		if reason == "" {
			reason = "it is still on disk"
		}
		return true, fmt.Sprintf("its scratch worktree %s must be preserved: %s", filepath.Base(wt.Path), reason)
	}
	return false, ""
}
