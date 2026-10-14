// Package git is the ctxloom family's git DI seam: every git operation the
// isolation layer (and future callers) needs goes through the Git interface, not
// scattered exec.Command("git", …) calls. That gives the highest-risk paths — the
// worktree lifecycle and the WIP-safe, nested-worktree-aware teardown — a unit-
// testable seam (see Fake) so we never again lose uncommitted work by force-
// removing a worktree without inspecting it first.
//
// The default implementation (NewExec) shells out to the system git binary,
// mirroring internal/adapters/remote's runGit: every command carries an explicit working
// directory via exec.Cmd.Dir (NOT os.Chdir, which is process-global and would
// race the concurrent fan-out), captures stderr into the returned error, and
// respects context cancellation.
package git

import (
	"context"
	"errors"
	"time"
)

// Worktree is one entry from `git worktree list` (repo-global). Path is the
// working-tree directory; Head the checked-out commit; Bare marks the bare main
// repo entry.
type Worktree struct {
	Path string
	Head string
	// Branch is the attached branch, zero when Detached. Porcelain reports
	// `branch refs/heads/<name>`; as a Branch it compares equal to the value
	// the caller handed WorktreeAdd (a resume once refused every preserved
	// checkout because the two spellings of one branch compared unequal).
	Branch   Branch
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

	// CommonDir returns the ABSOLUTE path to the repo's common git directory
	// (git rev-parse --git-common-dir). This is where the shared info/exclude
	// lives — a linked worktree resolves it to the MAIN repo's .git, which is why
	// a common-dir exclude is honored inside every worktree.
	CommonDir(ctx context.Context, dir string) (string, error)

	// WorktreeAdd creates a fresh worktree at path on a NEW branch named
	// branch, starting from ref (git -C repoDir worktree add -b branch path
	// ref). Named, never detached, and that is the whole point: a commit made
	// inside the checkout is held by the branch in the shared repository, so
	// it survives every way the checkout can disappear — teardown, prune, a
	// harness auto-clean, a /tmp wipe. A detached checkout's commits were held
	// only by its own HEAD and went unreachable the moment it was removed.
	// The branch is -b, never -B: a name already taken is git's own refusal,
	// never a silent reset of someone else's branch. relativePaths records
	// the checkout and its registration relative to each other
	// (RelativePathsFlag, git 2.48+), so they resolve from any mount of the
	// repository that keeps their relative position.
	WorktreeAdd(ctx context.Context, repoDir, path string, branch Branch, ref string, relativePaths bool) error
	// WorktreeAddExisting creates a worktree at path checked out on the
	// EXISTING branch `branch` (git worktree add <path> <branch>): a
	// checkout re-entered on the branch an earlier one left behind.
	WorktreeAddExisting(ctx context.Context, repoDir, path string, branch Branch, relativePaths bool) error
	// BranchExists reports whether repoDir has the local branch `branch`.
	BranchExists(ctx context.Context, repoDir string, branch Branch) (bool, error)

	// Version is the git binary's version ("2.48.1"), as `git version`
	// reports it.
	Version(ctx context.Context) (string, error)

	// WorktreeRemove removes the worktree at path (no --force: git REFUSES a
	// dirty worktree, the WIP-safe default the teardown relies on). There is
	// deliberately no force-remove escape hatch on this seam — this is the
	// exact area where the project's standing rule is "never force-remove
	// without inspecting/preserving WIP first", so the capability
	// is structurally unavailable rather than merely unused-by-convention.
	WorktreeRemove(ctx context.Context, repoDir, path string) error

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
	// project history. Used by trigger evaluation (internal/adapters/operations) to
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

	// CurrentBranch returns dir's checked-out branch (git rev-parse
	// --abbrev-ref HEAD). A detached HEAD — git's own "HEAD" sentinel — is the
	// ZERO Branch with a nil error, so a caller tells the two apart with
	// IsZero rather than a string literal. A worktree this seam created is
	// always on its named branch (WorktreeAdd), so a zero Branch there means a
	// checkout ctxloom did NOT make.
	CurrentBranch(ctx context.Context, dir string) (Branch, error)

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

	// SnapshotCommit commits dir's working tree OFF-BRANCH and moves no
	// ref: everything CommitAll would stage (git add -A) is staged into a
	// PRIVATE copy of dir's index, never the real one, and committed on top
	// of HEAD (git write-tree, git commit-tree -p HEAD) under message, with
	// the identity and signing `git commit` would use; no commit hook runs.
	// dir's real index and working tree are left byte-identical. head is
	// the HEAD the snapshot sits on; snapshot is "" when the tree equals
	// HEAD's (nothing to snapshot) — so a snapshot always carries content.
	SnapshotCommit(ctx context.Context, dir, message string) (snapshot, head string, err error)

	// AdvanceBranch moves branch to `to` only while it is still at `from`
	// (git update-ref, a compare-and-swap; never forced) and, when dir's
	// HEAD is on branch, resets dir's index to it (git reset -q, the working
	// tree untouched) — so a tree that held exactly `to`'s content reads
	// clean, as it would after `git add -A && git commit`. A branch that
	// moved meanwhile is ErrRefMoved, and nothing is changed.
	AdvanceBranch(ctx context.Context, dir string, branch Branch, to, from string) error

	// DeleteBranchAt deletes the local branch only while it is still at
	// `at` (git update-ref -d, a compare-and-swap) and is checked out in no
	// worktree: it removes a branch nothing has committed to since it was
	// made, and nothing else.
	DeleteBranchAt(ctx context.Context, repoDir string, branch Branch, at string) error

	// Clone clones url into dir, which must not exist yet. branch, when
	// non-zero, is the branch checked out (git clone --branch); zero takes
	// the remote's own default branch, so the caller can ASK what that is by
	// cloning and reading HEAD rather than guessing "main" or "master". Only
	// the checked-out branch's TIP is fetched (--single-branch, --depth 1):
	// the clone exists to commit on top of that tip and push, and a commit
	// whose parent the remote already holds needs nothing behind it, so a
	// full history would be paid for on every publish and never read. git
	// ignores --depth for a plain local path, which is why a local remote is
	// always a file:// URL by the time it gets here.
	//
	// AUTHENTICATION IS THE USER'S GIT, NOT CTXLOOM'S. This runs the git
	// binary, which resolves ~/.ssh/config, ssh-agent, credential helpers and
	// known_hosts exactly as it does for every other repository on the
	// machine. ctxloom holds no key material and installs no host-key policy:
	// a HostKeyCallback is the easy-to-get-quietly-wrong part, and getting it
	// wrong means a silent MITM on a path that publishes content others load.
	Clone(ctx context.Context, url, dir string, branch Branch) error

	// HeadSHA returns dir's current HEAD commit.
	HeadSHA(ctx context.Context, dir string) (string, error)

	// FileBlobSHA returns the blob SHA of path as it exists at ref, or ""
	// when ref's tree has no such path. The two outcomes a caller must never
	// conflate are separated at the type level: "" with a nil error means
	// ABSENT, and an error means the question could not be ANSWERED (bad ref,
	// unreadable repository). It is read from `git ls-tree`, which exits 0
	// with empty output for a missing path — unlike `rev-parse ref:path`,
	// which fails identically for "no such file" and "no such ref", so a
	// publisher using it would report an existing file as new the moment the
	// branch name was wrong.
	FileBlobSHA(ctx context.Context, dir, ref, path string) (string, error)

	// Push pushes refspec (e.g. "HEAD:refs/heads/main") to the named remote
	// from dir. Same ambient-credential story as Clone.
	Push(ctx context.Context, dir, remote, refspec string) error

	// RemoteRefSHA asks the REMOTE (git ls-remote) what commit ref points at,
	// or "" when the remote has no such ref. This is the only way to check
	// what actually landed on the far side: a `git push` that exits 0 and a
	// remote holding the new commit are two different facts, and this codebase
	// has a documented history of writes that report success while nothing
	// moves.
	RemoteRefSHA(ctx context.Context, dir, remote, ref string) (string, error)

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

	// MergedBranches returns the LOCAL branches already merged into ref
	// (git branch --merged <ref>). ref is any commit-ish, not necessarily a
	// branch; ref="" means repoDir's HEAD — "merged into whatever is checked
	// out here" is the common question. There was no
	// merged-ness primitive anywhere in this codebase before doctor's
	// foreign-worktree check needed one: printing "unmerged" without
	// checking would be a fabricated claim.
	//
	// Bounded by an internal timeout in the exec implementation: this is
	// invoked once per foreign worktree on EVERY `ctxloom doctor` run —
	// including the one `ctxloom init` runs — so a hung or corrupt
	// repository must not hang the whole report.
	MergedBranches(ctx context.Context, repoDir, ref string) ([]Branch, error)

	// DeleteBranch deletes the LOCAL branch with git's SAFE delete
	// (git branch -d): git refuses a branch not merged into its upstream or
	// HEAD, so a caller that has not checked merged-ness still cannot lose
	// commits through it.
	DeleteBranch(ctx context.Context, repoDir string, branch Branch) error
}

// ErrRefMoved is AdvanceBranch's refusal: the branch is no longer where the
// caller last saw it, so it is left alone.
var ErrRefMoved = errors.New("git: the branch moved")

// LogEntry is one commit returned by LogSince.
type LogEntry struct {
	SHA     string
	Date    time.Time
	Subject string
	Files   []string // paths this commit changed
	// FilesUnknown marks an entry whose changed-file list could NOT be
	// gathered (the per-commit query failed, e.g. an unreachable SHA under a
	// concurrent history rewrite). Without it an empty Files is ambiguous:
	// "this commit touched nothing" and "we never found out what it touched"
	// look identical, and a consumer filtering commits by path would report a
	// confident no-match built on evidence it never gathered.
	FilesUnknown bool
}
