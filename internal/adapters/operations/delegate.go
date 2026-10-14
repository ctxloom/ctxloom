package operations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// EngineProcess is a runner process started for a resolved launch: up,
// isolation-prepared, dialing home with the coordinator trio on its env —
// but not yet attached. Readiness (the dial-home) is the coordinator's;
// this is the spawn half.
type EngineProcess struct {
	// Kill tears the runner process and its cell down (idempotent).
	Kill func()
	// StderrTail reads the runner's bounded stderr tail without reaping it —
	// the only death reason available when the whole runner dies without a
	// terminal frame (docker-stop / OOM = runner loss). Nil-safe.
	StderrTail func() string
	// Wait blocks until the runner PROCESS exits and reports why; the
	// coordinator races it against the dial-home wait so a runner that died
	// at standup fails the spawn at once. Nil when the starter captures no
	// process (a test double), which degrades to timeout-only detection.
	Wait func() error
}

// StartEngine starts the runner process for a resolved launch in its cell's
// environment, with the reach-back trio on the RUNNER's env — never the
// engine's. A caller-supplied starter — one launch closure, readiness being
// the coordinator's awaitRunner — replaces the environment (test seam).
// A returned process is up; its dial-home is awaited by the coordinator.
func StartEngine(ctx context.Context, l launch.Launch, runnerEnv map[string]string, verbosity int, starter func(context.Context) (*isolation.RunnerHandle, error)) (*EngineProcess, error) {
	if starter == nil {
		env, ok := EnvironmentOf(l.Cell)
		if !ok {
			return nil, errors.New("delegate: the cell carries no environment and no starter was supplied")
		}
		starter = func(ctx context.Context) (*isolation.RunnerHandle, error) {
			return env.Start(ctx, isolation.RunnerRequest{Engine: string(l.Engine), Label: l.Label.Label, Verbosity: verbosity, Env: runnerEnv})
		}
	}
	handle, err := starter(ctx)
	if err != nil {
		_ = launch.Discard(ctx, l)
		return nil, err
	}
	var once sync.Once
	return &EngineProcess{
		StderrTail: func() string { return isolation.StderrTailOf(handle) },
		Wait:       isolation.WaitOf(handle),
		Kill: func() {
			once.Do(func() {
				handle.Kill()
				_ = launch.Discard(context.Background(), l)
			})
		},
	}, nil
}

// maxDirtyFilesListed bounds how many uncommitted paths a dirty-tree message
// names before collapsing the rest into "+N more". An agent worktree
// routinely carries dozens of modified delivered-surface files (regenerated
// docs, generated schemas) across a long coordinator session; a wall of
// paths would bury the two sentences that actually matter — what's dirty and
// what ctxloom is about to do about it.
const maxDirtyFilesListed = 10

// THE PARENT SNAPSHOT. A delegated agent_run spawn that carves a NEW
// worktree checks out a fresh branch (git.Git.WorktreeAdd, `git worktree add
// -b`): committed state only. A coordinator that drafts a file and then
// hands the work to a delegated child would otherwise get a child that
// silently runs against stale or missing content: exit 0, a plausible
// transcript, wrong bytes. That is this project's signature failure mode
// (see silent-no-op-failure-mode). So before such a checkout is carved,
// ctxloom commits the parent's uncommitted and untracked (not ignored)
// state OFF-BRANCH — a snapshot commit on top of the parent's HEAD, built in
// a private index (git.Git.SnapshotCommit), which moves no ref and leaves
// the parent's index and working tree untouched — and the child's branch is
// carved from that snapshot. It is the only behaviour: there is no handler
// to choose. Its counterpart at the other end of the run is the child's own
// run-end commit (isolation.SpecBuilder.CommitOnTeardown).
//
// The parent's branch reaches the snapshot only once the child is UP — its
// environment's Start has returned, the runner started where it runs (a
// container observed running) — by a compare-and-swap from the HEAD the
// snapshot sits on (parentCommit.started). A launch that fails before then,
// for any reason, leaves the parent's branch, index and working tree
// exactly as they were, and the child's branch — the only thing that held
// the snapshot — is deleted with its checkout (parentCommit.abandoned).
// THE INVARIANT: a launch never leaves a commit on the parent's branch for a
// child that did not start. A parent branch that moved between the carve and
// the start (the human committed) is left alone, loudly: the child keeps
// working from the snapshot, and its work merges back as any other.
//
// It runs from isolation's BeforeCheckout hook (parentCommit, launch.go), so
// it snapshots only when a NEW checkout is about to be carved, after the
// launch's isolation preconditions have passed: a refused launch, and a
// resume that re-enters its own checkout or its own branch, snapshot
// nothing.
//
// The ORIGINATOR's own worktree run is not snapshotted for: the human who
// asked for `--workspace worktree` is at the terminal with the tree in front
// of them, and the snapshot's whole subject is a child spawned by an agent
// that cannot see what it would hand over. Cells.Prepare gates on
// sessions.Identity.IsChild.
//
// --degraded plays no role: overloading the global degraded flag here would
// silently hand the child stale content for unrelated startup-finding
// reasons.

// dirtyFileList is the bounded file listing every dirty-tree message shows,
// together with the one fact a bare []string cannot carry: whether the
// listing that produced it succeeded. A WorkingChanges failure rendering as
// the empty set is a lie in exactly the messages that exist to say WHICH
// files are at stake — the preview shown immediately before
// auto-committing the user's branch.
type dirtyFileList struct {
	listed []string
	more   int
	err    error
}

// boundDirtyChanges truncates changes to maxDirtyFilesListed, recording how
// many were dropped. A WorkingChanges error is CARRIED, not swallowed: every
// caller still proceeds (best-effort), but says plainly that it could not
// find out rather than showing an empty list. Takes the (changes, err) pair
// so a caller can forward git's return directly.
func boundDirtyChanges(changes []string, err error) dirtyFileList {
	if err != nil {
		return dirtyFileList{err: err}
	}
	out := dirtyFileList{listed: changes}
	if len(out.listed) > maxDirtyFilesListed {
		out.more = len(out.listed) - maxDirtyFilesListed
		out.listed = out.listed[:maxDirtyFilesListed]
	}
	return out
}

// writeTo appends the bounded file listing (one indented path per line, "+N
// more" tail) shared by every dirty-tree message, or the reason there is no
// listing to show.
func (d dirtyFileList) writeTo(b *strings.Builder) {
	if d.err != nil {
		fmt.Fprintf(b, "  (could not list the changed files: %v — the paths below are unknown, NOT empty)\n", d.err)
		return
	}
	for _, c := range d.listed {
		fmt.Fprintf(b, "  %s\n", c)
	}
	if d.more > 0 {
		fmt.Fprintf(b, "  (+%d more)\n", d.more)
	}
}

// parentSnapshot is a parent tree's off-branch snapshot: the commit, the
// HEAD it sits on, and the branch that HEAD is — the compare-and-swap the
// parent's branch advances by once the child is up. The zero value is no
// snapshot.
type parentSnapshot struct {
	branch git.Branch
	head   string
	commit string
}

// snapshotParentTree snapshots workDir (the PARENT project tree) ahead of a
// delegated child's new worktree checkout, when it is dirty. WHAT COUNTS AS
// DIRTY is git's own notion of it — `git status --porcelain`
// (git.Git.IsDirty / WorkingChanges), which honors BOTH the tracked
// .gitignore and the repo's .git/info/exclude, where this codebase's
// per-agent worktree preparation writes the delivered-surface noise
// (isolation.WorktreeArtifactPatterns, written by
// Worktree.excludeConfigFromMerge). The snapshot stages with the same
// `git add -A`, so its notion of "noise" can never drift from the isolation
// layer's own.
//
// Best-effort on a git failure to tell (no binary, workDir not a repo —
// some test doubles pass a bare temp dir): it snapshots nothing and never
// blocks the spawn, matching how the isolation chain's OWN git checks
// degrade. A snapshot that is attempted and fails refuses the spawn.
func snapshotParentTree(ctx context.Context, gitClient git.Git, workDir, harp string) (parentSnapshot, error) {
	dirty, err := gitClient.IsDirty(ctx, workDir)
	if err != nil || !dirty {
		return parentSnapshot{}, nil
	}
	return snapshotDirtyTree(ctx, gitClient, workDir, harp, boundDirtyChanges(gitClient.WorkingChanges(ctx, workDir, 0)))
}

// snapshotDirtyTree makes the parent snapshot. A coherence guard refuses
// inside a detached-HEAD checkout (see the branch.IsZero() case below):
// there is no branch to advance once the child is up. An unborn HEAD fails
// CurrentBranch and is refused by the same guard. Past it, the snapshot is
// committed off-branch and announced, naming the branch it will advance and
// the bounded file list. A dirty status whose tree nonetheless equals HEAD's
// (git.Git.SnapshotCommit's "") is no snapshot: the child is carved from
// HEAD.
func snapshotDirtyTree(ctx context.Context, gitClient git.Git, workDir, harp string, files dirtyFileList) (parentSnapshot, error) {
	branch, berr := gitClient.CurrentBranch(ctx, workDir)
	// An unresolvable branch name is exactly the condition the detached-HEAD
	// guard below exists to catch (the caller cannot tell whether this is a
	// bare checkout), so failing loud here is strictly safer than guessing.
	if berr != nil {
		return parentSnapshot{}, fmt.Errorf(`agent_run: could not determine %s's current branch before snapshotting it for delegated agent %q: %w (an unresolvable branch is the one condition the detached-HEAD guard must not treat as safe) — pass workspace: "none" for this spawn to run it against the live checkout instead`, workDir, harp, berr)
	}
	if branch.IsZero() {
		return parentSnapshot{}, fmt.Errorf(`agent_run: %s is a detached-HEAD checkout, so its uncommitted changes cannot be committed for delegated agent %q (this looks like a delegated child's OWN isolated worktree, not a branch checkout — a commit here would land on no branch and could be silently discarded when that worktree is torn down) — pass workspace: "none" for this spawn to run it against the live checkout instead`, workDir, harp)
	}
	sha, head, err := gitClient.SnapshotCommit(ctx, workDir, renderCommitMessage(harp))
	if err != nil {
		return parentSnapshot{}, fmt.Errorf(`agent_run: snapshotting %s for delegated agent %q: %w`, workDir, harp, err)
	}
	if sha == "" {
		return parentSnapshot{}, nil
	}
	clidiag.Warn("ctxloom", "%s", renderCommitPreview(harp, workDir, branch, sha, files))
	return parentSnapshot{branch: branch, head: head, commit: sha}, nil
}

// renderCommitPreview is the notice shown for every parent snapshot, so the
// commit the parent's branch will gain is visible: the snapshot, the branch,
// and the bounded file list.
func renderCommitPreview(harp, workDir string, branch git.Branch, sha string, files dirtyFileList) string {
	var b strings.Builder
	fmt.Fprintf(&b, "agent_run: committed %s's uncommitted changes as %s so delegated agent %q sees them in its own git worktree (a worktree checkout only ever contains committed state); branch %q advances to it once the agent has started, and stays as it is if the launch fails:\n", workDir, shortSHA(sha), harp, branch)
	files.writeTo(&b)
	return strings.TrimRight(b.String(), "\n")
}

// shortSHA is sha abbreviated for a message.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// renderCommitMessage is the parent snapshot's message — readable in `git
// log` with no other context: WHO did it (ctxloom, automatically) and WHY
// (so a named delegated agent's worktree could see it).
func renderCommitMessage(harp string) string {
	return fmt.Sprintf(`ctxloom: commit working tree before delegating to %s

ctxloom committed this working tree automatically before spawning delegated
agent %q into its own git worktree: a worktree checkout only ever contains
committed state, so these changes would otherwise be invisible to the child.
`, harp, harp)
}

// Abort tears down a prepared-but-never-started launch's workspace.
