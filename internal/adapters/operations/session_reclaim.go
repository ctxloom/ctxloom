package operations

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
)

// reclaimTriage is the reaper's triage for one session: its scratch worktrees
// first (worktreeTriage), then — on apply, once nothing spares the session —
// the coordinator root it founded in projectDir (removeSessionRoot). The root
// is the session's own tree, and nothing but a resume of this session can
// ever adopt it, so a reaped session takes it along.
func reclaimTriage(g git.Git, projectDir string) sessions.Triage {
	worktrees := worktreeTriage(g)
	return func(ctx context.Context, harp string, probe sessions.LockProbe, apply bool) (string, error) {
		spared, err := worktrees(ctx, harp, probe, apply)
		if err != nil || spared != "" || !apply {
			return spared, err
		}
		return "", removeSessionRoot(projectDir, harp)
	}
}

// removeSessionRoot removes the coordinator root harp founded in projectDir
// through coord.RemoveRoot, which claims it first: a root a live process
// holds — a session resumed from this one, which has adopted the tree — is
// refused and kept. The root is keyed by the project identity the
// coordinator host keys it by, read (never resolved, which would mint one)
// like doctor reads it. Any other failure is returned, so the reaper leaves
// the session alone and reports why rather than half-reaping it. The root is
// the controller's own filesystem, where the reaper's Layout removes the
// session's members.
func removeSessionRoot(projectDir, harp string) error {
	if projectDir == "" {
		return nil
	}
	id, err := projectid.ReadMarker(projectDir)
	if err != nil {
		id = ""
	}
	if err := coord.RemoveRoot(safefs.New(), id, projectDir, harp); err != nil && !errors.Is(err, coord.ErrStateOwned) {
		return fmt.Errorf("its coordinator root: %w", err)
	}
	return nil
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
// ANY survivor spares the whole session. A work/ dir that still holds a
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
