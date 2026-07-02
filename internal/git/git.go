// Package git is the ctxloom family's git DI seam: every git operation the
// isolation layer (and future callers) needs goes through the Git interface, not
// scattered exec.Command("git", …) calls. That gives the highest-risk paths — the
// worktree lifecycle and the WIP-safe, nested-worktree-aware teardown — a unit-
// testable seam (see Fake) so we never again lose uncommitted work by force-
// removing a worktree without inspecting it first.
//
// The default implementation (NewExec) shells out to the system git binary,
// mirroring internal/remote's runGit: every command carries an explicit working
// directory via exec.Cmd.Dir (NOT os.Chdir, which is process-global and would
// race the concurrent fan-out), captures stderr into the returned error, and
// respects context cancellation.
package git

import (
	"context"
)

// Worktree is one entry from `git worktree list` (repo-global). Path is the
// working-tree directory; Head the checked-out commit; Branch the attached ref
// (empty when Detached); Bare marks the bare main repo entry.
type Worktree struct {
	Path     string
	Head     string
	Branch   string
	Detached bool
	Bare     bool
}

// Git is the DI seam over the git binary. Every method that touches a repository
// takes an explicit directory to run in (dir / repoDir) so operations are
// concurrency-safe (per-command cwd, no os.Chdir) — the fan-out runs many members
// in parallel. Fault tolerance is the caller's job: an error here means the
// caller warns and degrades (e.g. worktree → None); it must never block the LLM.
type Git interface {
	// IsRepo reports whether dir is inside a git working tree (handles the linked-
	// worktree case where .git is a file, not a directory). Best-effort: any error
	// or missing binary reports false.
	IsRepo(dir string) bool

	// Toplevel returns the working-tree root of the repo containing dir
	// (git rev-parse --show-toplevel).
	Toplevel(ctx context.Context, dir string) (string, error)

	// CommonDir returns the ABSOLUTE path to the repo's common git directory
	// (git rev-parse --git-common-dir). This is where the shared info/exclude
	// lives — a linked worktree resolves it to the MAIN repo's .git, which is why
	// a common-dir exclude is honored inside every worktree.
	CommonDir(ctx context.Context, dir string) (string, error)

	// WorktreeAdd creates a fresh, DETACHED worktree at path checked out to ref
	// (git -C repoDir worktree add --detach path ref). Detached by design: these
	// are ephemeral per-agent checkouts, never branch-per-agent.
	WorktreeAdd(ctx context.Context, repoDir, path, ref string) error

	// WorktreeRemove removes the worktree at path. force=false makes git REFUSE a
	// dirty worktree — the WIP-safe default the teardown relies on; force=true is
	// only for known-clean removals.
	WorktreeRemove(ctx context.Context, repoDir, path string, force bool) error

	// WorktreeList returns the repo-GLOBAL worktree list (parsed from
	// git -C repoDir worktree list --porcelain). Needed by the nested-worktree-
	// aware teardown to find inner worktrees created under ours (e.g. claude's
	// native EnterWorktree) before removing the outer.
	WorktreeList(ctx context.Context, repoDir string) ([]Worktree, error)

	// WorktreePrune drops administrative files for worktrees whose directories are
	// gone (git -C repoDir worktree prune) — the teardown's final sweep.
	WorktreePrune(ctx context.Context, repoDir string) error

	// UpdateIndexSkipWorktree sets/clears the skip-worktree bit on a TRACKED file
	// (git -C dir update-index --skip-worktree / --no-skip-worktree file), hiding
	// a local modification from status/stage/merge. Used to keep tracked config
	// ctxloom must edit out of a developer member's merge-back.
	UpdateIndexSkipWorktree(ctx context.Context, dir, file string, skip bool) error

	// ListTracked returns the repository-tracked files under dir matching the given
	// pathspecs (git -C dir ls-files -z -- <pathspecs>). Empty when nothing matches.
	// The worktree policy uses it to find which per-agent config files a worktree
	// already TRACKS, so ctxloom can skip-worktree its edits to them (§3.1 merge
	// isolation for the TRACKED case; the untracked case is covered by info/exclude).
	ListTracked(ctx context.Context, dir string, pathspecs ...string) ([]string, error)

	// IsDirty reports whether dir has uncommitted or untracked changes
	// (git -C dir status --porcelain is non-empty). This is the WIP check the
	// teardown runs before removing anything.
	IsDirty(ctx context.Context, dir string) (bool, error)
}
