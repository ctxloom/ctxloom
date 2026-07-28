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
	"time"
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

	// LogSince returns commits reachable from HEAD at or after since (the zero
	// time = no lower bound), newest first, each carrying its changed files.
	// Bounded by maxEntries (<=0 defaults to a small cap) so a caller scoping
	// evidence to "since some point in the past" can never walk unbounded
	// project history. Used by trigger evaluation (internal/operations) to
	// gather git evidence for a Deferred task's revive trigger.
	LogSince(ctx context.Context, dir string, since time.Time, maxEntries int) ([]LogEntry, error)

	// RepoDirs returns a sorted, bounded inventory of directories that exist in
	// the repo — from TRACKED content and from UNTRACKED working-tree paths
	// alike. Trigger evaluation needs it to answer existence-style conditions
	// ("once package X exists"), which commit history cannot: the thing may
	// predate the history window, or live uncommitted in no commit at all.
	// maxDirs <=0 defaults to a bounded cap.
	RepoDirs(ctx context.Context, dir string, maxDirs int) ([]string, error)

	// WorkingChanges returns the bounded porcelain status of the working tree
	// (e.g. "?? internal/foo/bar.go"): work that exists but is in no commit.
	// maxEntries <=0 defaults to a bounded cap.
	WorkingChanges(ctx context.Context, dir string, maxEntries int) ([]string, error)

	// CurrentBranch returns dir's checked-out branch name (git rev-parse
	// --abbrev-ref HEAD). Detached HEAD (the case inside a `git worktree add
	// --detach` checkout — every ctxloom-created worktree) reports git's own
	// sentinel string "HEAD", never a real branch name; callers that need to
	// tell the two apart compare against that literal.
	CurrentBranch(ctx context.Context, dir string) (string, error)

	// CommitAll stages EVERY change git considers dirty at dir — including
	// untracked-but-not-ignored files (git add -A), matching exactly what
	// IsDirty/WorkingChanges measure — and commits with message. Returns the
	// new commit's SHA and the names of files that differ between the
	// pre-commit HEAD and the new commit (git diff --name-only), so a caller
	// can verify the commit actually captured content: a post-commit stat is
	// NOT proof a commit landed (this codebase has a documented history of a
	// pre-commit-hook bug that let commits land empty while still reporting
	// success). An empty changedFiles slice with a nil error means exactly
	// that: git accepted the commit but it carries no diff versus its parent.
	CommitAll(ctx context.Context, dir, message string) (sha string, changedFiles []string, err error)

	// DiffNameOnly returns the names of files that differ between two refs
	// (git diff --name-only a b).
	DiffNameOnly(ctx context.Context, dir, a, b string) ([]string, error)

	// DiffPatch returns a unified diff of dir's TRACKED modifications and
	// deletions against HEAD (git diff HEAD), suitable for ApplyPatch.
	// Untracked files never appear in it — ListUntracked covers those.
	DiffPatch(ctx context.Context, dir string) (string, error)

	// ListUntracked returns dir's untracked-but-not-ignored files (git
	// ls-files -z --others --exclude-standard) — the exact same notion of
	// "noise" IsDirty/WorkingChanges already honor (.gitignore AND
	// .git/info/exclude).
	ListUntracked(ctx context.Context, dir string) ([]string, error)

	// ApplyPatch applies patch (as produced by DiffPatch) to dir's working
	// tree (git apply). An empty (or whitespace-only) patch is a no-op and
	// reports applied=false — distinguishable from a real apply at the type
	// level, so a caller can tell "nothing to apply" from "applied" rather
	// than trusting a nil error alone. Any failure (conflict, malformed
	// patch) is returned verbatim — callers must fail loudly rather than
	// half-apply and continue.
	ApplyPatch(ctx context.Context, dir, patch string) (applied bool, err error)

	// HasIgnoredContent reports whether dir contains any file git considers
	// ignored/excluded (.gitignore OR .git/info/exclude) — the exact class of
	// content IsDirty deliberately does NOT count as WIP. IsDirty's blindness
	// to this class is intentional (see its doc): a prepared agent worktree's
	// own delivered noise — .claude/, CLAUDE.md, .ctxloom/cache/ — is written
	// into .git/info/exclude precisely so it does not trip the ordinary WIP
	// check. But a DESTRUCTIVE removal (worktree teardown) must not reuse
	// that same blindness: an ignored file is still real content on disk,
	// and `git worktree remove` deletes it right along with everything else.
	// Callers that need "is it safe to delete this tree" must check BOTH
	// IsDirty and HasIgnoredContent, not IsDirty alone.
	HasIgnoredContent(ctx context.Context, dir string) (bool, error)
}

// LogEntry is one commit returned by LogSince.
type LogEntry struct {
	SHA     string
	Date    time.Time
	Subject string
	Files   []string // paths this commit changed
}
