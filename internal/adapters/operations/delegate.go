package operations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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

// StartEngine starts the runner process for a resolved launch through its
// cell's transport (docker-direct for a container cell, a bare self-invoked
// runner for a host cell), with the reach-back trio on the RUNNER's env —
// never the engine's. A caller-supplied starter replaces the cell's
// transport (test seam). A returned process is up; its dial-home is awaited
// by the coordinator.
func StartEngine(ctx context.Context, l launch.Launch, runnerEnv map[string]string, verbosity int, starter isolation.EngineStarter) (*EngineProcess, error) {
	if starter == nil {
		cell, ok := TransportOf(l.Cell)
		if !ok {
			return nil, errors.New("delegate: the cell carries no transport handle and no starter was supplied")
		}
		starter = isolation.StarterForWorkspace(cell.Policy, cell.Workspace, string(l.Engine), l.Label.Label, verbosity, runnerEnv)
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

// The dirty-tree handler is what a delegated agent_run spawn does when it
// resolves to worktree isolation while the PARENT project tree (workDir —
// the coordinator's own checkout; never the child's future workspace)
// carries uncommitted changes. The vocabulary, its names and its one parser
// are launch.DirtyTreeHandler's; which member governs a launch is settled
// ONCE by launch.Resolve (the invocation's, else the project's
// `dirty_tree_handler` default, else "commit") and arrives here on the
// CellRequest already decided.
//
// WHY THIS EXISTS AT ALL: worktree isolation checks out a fresh branch at a
// ref (git.Git.WorktreeAdd, `git worktree add -b`) — a checkout of COMMITTED
// state only, HEAD and everything reachable from it. A
// coordinator that drafts a file and then hands the work to a delegated
// child would otherwise get a child that silently runs against stale or
// missing content: exit 0, a plausible transcript, wrong bytes. That is this
// project's signature failure mode (see silent-no-op-failure-mode), and
// worktree-by-default would introduce it deliberately if nothing decided
// what to do about it. Four explicit choices, no refuse-or-degrade blur:
//
//   - launch.DirtyTreeHandlerCommit ("commit", the DEFAULT): commit the
//     parent's dirty state first, so the child sees it. Gated behind a
//     per-PROJECT human acknowledgement (dirty_tree_commit_ack) — see
//     commitDirtyTree.
//   - launch.DirtyTreeHandlerCopy ("copy"): carve the worktree at HEAD as
//     usual, then reproduce the parent's uncommitted changes INSIDE it as
//     uncommitted WIP — nothing is ever committed to the parent's branch.
//   - launch.DirtyTreeHandlerStale ("stale"): proceed against committed
//     state only, warning that the child will not see the listed changes.
//   - launch.DirtyTreeHandlerFail ("fail"): refuse the spawn outright — the
//     message names the uncommitted paths and the alternatives.
//
// --degraded (strictness.Degraded()) plays NO role in any of the four: which
// one runs is governed entirely by dirty_tree_handler. Overloading the
// global degraded flag here would silently convert "refuse/handle a dirty
// spawn deliberately" back into "hand the child stale content" via a flag
// set for unrelated startup-finding reasons — reintroducing the exact bug
// this gate exists to prevent. (isolationGateErr DOES still respect
// --degraded; that is unchanged and unrelated to this gate.)
//
// The ORIGINATOR's own worktree run is not gated: the human who asked for
// `--workspace worktree` is at the terminal with the tree in front of them,
// and the handler's whole subject is a child spawned by an agent that
// cannot see what it would hand over. Cells.Prepare gates on
// sessions.Identity.IsChild.
//
// dirtyTreeOutcome is what handleDirtyParentTree decided, for the cells
// adapter to act on. Only the "copy" handler populates copy — its
// file reproduction is deferred until the worktree actually exists (see
// applyCopySnapshot's call site).
type dirtyTreeOutcome struct {
	copy *copySnapshot
}

// copySnapshot is the parent's dirty state captured at handleDirtyParentTree
// time (BEFORE the worktree is created), applied into the worktree once it
// exists. Captured once rather than re-derived later so there is exactly one
// read of "what's dirty" per spawn — no window for the parent tree to drift
// between the decision and the application.
type copySnapshot struct {
	// patch is `git diff HEAD` from the parent — tracked modifications and
	// deletions, ""  when there were none.
	patch string
	// untracked are the parent's untracked-but-not-ignored file paths
	// (relative to sourceDir) to copy verbatim — DiffPatch/git apply never
	// carries these; they need their own byte-for-byte reproduction.
	untracked []string
	// sourceDir is the parent directory untracked files are read FROM.
	sourceDir string
}

// dirtyFileList is the bounded file listing every dirty-tree message shows,
// together with the one fact a bare []string cannot carry: whether the
// listing that produced it succeeded. A WorkingChanges failure rendering as
// the empty set is a lie in exactly the messages that exist to say WHICH
// files are at stake — the refusal that claims uncommitted work would be
// invisible to the child, and the preview shown immediately before
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

// handleDirtyParentTree is the dirty-tree handler dispatch: given that
// workDir (the PARENT project tree) is being checked ahead of a delegated
// spawn resolving to worktree isolation, decide (and where possible, act on)
// what handler says to do. WHAT COUNTS AS DIRTY is git's own notion of it —
// `git status --porcelain` (git.Git.IsDirty / WorkingChanges), which already
// honors BOTH the tracked .gitignore and the repo's .git/info/exclude. This
// is deliberately NOT a bespoke ignore-pattern allowlist reimplementing "what
// counts as noise": this codebase's own per-agent worktree preparation
// already writes the delivered-surface noise that would otherwise make this
// gate unusable into the shared common-dir .git/info/exclude
// (gitignore.WorktreeArtifactPatterns, written by
// Worktree.excludeConfigFromMerge), and the tracked .gitignore separately
// covers the generated living-docs journeys. Once a repo has
// prepared even ONE agent worktree, that noise is invisible to `git status
// --porcelain` for every tree sharing the repo's common dir — including the
// parent's, which is exactly the tree this inspects. Reusing git's own
// porcelain check (the SAME mechanism the worktree teardown already trusts
// to decide WIP-safety, git.Git.IsDirty's doc) means this gate's notion of
// "noise" can never drift from the isolation layer's own.
//
// Tracked modifications always count. Untracked files count too, but ONLY
// when git itself would not already call them ignored/excluded.
//
// Best-effort on a git failure (no binary, workDir not a repo — some test
// doubles pass a bare temp dir): never blocks the spawn, matching how the
// isolation chain's OWN git checks degrade (chainFor's worktree branch
// degrades silently to None on a non-repo dir rather than failing the run).
func handleDirtyParentTree(ctx context.Context, cfg *config.Config, gitClient git.Git, workDir, agentName string, handler launch.DirtyTreeHandler) (dirtyTreeOutcome, error) {
	dirty, err := gitClient.IsDirty(ctx, workDir)
	if err != nil || !dirty {
		return dirtyTreeOutcome{}, nil
	}
	files := boundDirtyChanges(gitClient.WorkingChanges(ctx, workDir, 0))

	switch handler {
	case launch.DirtyTreeHandlerFail:
		return dirtyTreeOutcome{}, dirtyTreeFailError(agentName, workDir, files)

	case launch.DirtyTreeHandlerStale:
		var b strings.Builder
		fmt.Fprintf(&b, "agent_run: agent %q is spawning into a worktree while %s has uncommitted changes (dirty_tree_handler: \"stale\") — the child will NOT see these changes, only committed state:\n", agentName, workDir)
		files.writeTo(&b)
		b.WriteString(`change dirty_tree_handler to "commit" or "copy" to carry these across, or pass workspace: "none" for this call to run against the live checkout instead`)
		clidiag.Warn("ctxloom", "%s", b.String())
		return dirtyTreeOutcome{}, nil

	case launch.DirtyTreeHandlerCopy:
		patch, perr := gitClient.DiffPatch(ctx, workDir)
		if perr != nil {
			return dirtyTreeOutcome{}, fmt.Errorf(`dirty_tree_handler "copy": reading %s's tracked changes: %w`, workDir, perr)
		}
		untracked, uerr := gitClient.ListUntracked(ctx, workDir)
		if uerr != nil {
			return dirtyTreeOutcome{}, fmt.Errorf(`dirty_tree_handler "copy": listing %s's untracked files: %w`, workDir, uerr)
		}
		return dirtyTreeOutcome{copy: &copySnapshot{patch: patch, untracked: untracked, sourceDir: workDir}}, nil

	case launch.DirtyTreeHandlerCommit:
		return dirtyTreeOutcome{}, commitDirtyTree(ctx, cfg, gitClient, workDir, agentName, files)

	default:
		// Unreachable through launch.Resolve, which settles a parsed handler
		// before the cell is prepared. It stays as a REFUSAL rather than a
		// fallback to the commit arm: a caller that reached this dispatch
		// with a value no parse admitted (or none at all) has said nothing
		// this function may act on, and the arm it would otherwise land on
		// rewrites the user's branch.
		return dirtyTreeOutcome{}, fmt.Errorf("agent_run: dirty_tree_handler %q reached the dirty-tree dispatch unparsed (known: %s) — refusing to spawn rather than guess a handler that could commit %s", handler, strings.Join(launch.DirtyTreeHandlerNames(), "|"), workDir)
	}
}

// dirtyTreeFailError renders the "fail" handler's refusal — this gate's
// original, only behavior before the other three handlers existed. Kept
// byte-for-byte compatible with that original message.
func dirtyTreeFailError(agentName, workDir string, files dirtyFileList) error {
	var b strings.Builder
	fmt.Fprintf(&b, "agent_run: refusing to spawn agent %q into a worktree — %s has uncommitted changes a worktree checkout cannot see (git worktree add checks out committed state only: HEAD and everything reachable from it, nothing you haven't committed yet). These changes would be invisible to the child:\n", agentName, workDir)
	files.writeTo(&b)
	b.WriteString(`commit the changes, or pass workspace: "none" for this agent_run call to run it against the live checkout instead`)
	return errors.New(b.String())
}

// commitDirtyTreeAckKey names the acknowledgement the "commit" handler's
// ack-refusal message and warning point at — kept as a constant so the
// refusal text and any future doc/init-interview wiring name it identically.
// It is no longer a config.yaml key (see config.SetDirtyTreeCommitAck's doc);
// the name survives as the concept's identifier and as the on-disk store's
// own file stem (paths.DirtyTreeCommitAckFileName).
const commitDirtyTreeAckKey = "dirty_tree_commit_ack"

// commitDirtyTree implements dirty_tree_handler: "commit". It is gated
// TWICE, in order: (1) a coherence guard against auto-committing inside a
// detached-HEAD checkout (see the branch=="HEAD" case below), and (2) the
// per-checkout human acknowledgement (config.DirtyTreeCommitAcknowledged,
// read ONLY from its own admission-store file under .ctxloom/state/ — never
// req/agent-supplied data, and never the layered config chain, which has
// THREE channels (a home file, an environment variable, an argv) an agent
// can reach — by design: agent_run is normally invoked by a coordinator
// AGENT over MCP, in a process with no TTY, often while the human is away.
// An interactive prompt would either hang forever or be answered by an
// agent — which is not the user's consent. A durable, human-written
// acknowledgement record is the only form of prior consent that survives
// headless operation. DO NOT add a per-call override for this: a per-call
// parameter would let a delegating AGENT grant itself permission to commit
// on the user's behalf, which defeats the entire point — this must be a
// human act, done once, via `ctxloom init` or `ctxloom manage
// dirty-tree-ack`). Only once both pass does it warn (naming the branch and
// the bounded file list) and
// mutate. It never silently trusts a bare "commit succeeded": CommitAll's
// own before/after diff must show real content, or this refuses (see the
// len(changed)==0 case) — this codebase has a documented history of commits
// landing EMPTY due to an index-clobbering pre-commit-hook bug (since
// fixed), and a post-commit stat is not proof a commit landed.
func commitDirtyTree(ctx context.Context, cfg *config.Config, gitClient git.Git, workDir, agentName string, files dirtyFileList) error {
	branch, berr := gitClient.CurrentBranch(ctx, workDir)
	// An error here used to be discarded (`branch, _ :=`), leaving
	// branch=="" — which is NOT "HEAD", so the detached-HEAD guard below never
	// fired. An unresolvable branch name is exactly the condition this guard
	// exists to catch (the caller cannot tell whether this is a bare
	// checkout), so failing loud here is strictly safer than guessing.
	if berr != nil {
		return fmt.Errorf(`dirty_tree_handler "commit": could not determine %s's current branch: %w (this guard exists specifically to catch a detached-HEAD checkout, and an unresolvable branch name is the one condition it must not silently treat as safe) — pass dirty_tree_handler: "copy" or "stale" for this spawn instead, or "fail" to refuse it outright`, workDir, berr)
	}
	if branch == "HEAD" {
		return fmt.Errorf(`dirty_tree_handler "commit": %s is a detached-HEAD checkout (this looks like a delegated child's OWN isolated worktree, not a branch checkout — committing here would land on no branch and could be silently discarded when that worktree is later torn down) — pass dirty_tree_handler: "copy" or "stale" for this spawn instead, or "fail" to refuse it outright`, workDir)
	}

	if !config.DirtyTreeCommitAcknowledged(report.To(strictness.Sink("ctxloom")), cfg.FS(), cfg.GetAppDir()) {
		var b strings.Builder
		fmt.Fprintf(&b, "agent_run: refusing to auto-commit for delegated agent %q on branch %q — %s has uncommitted changes, a worktree checkout only ever contains committed state, and dirty_tree_handler is configured to \"commit\" (the default), but this checkout has not acknowledged that ctxloom may commit on your behalf:\n", agentName, branch, workDir)
		files.writeTo(&b)
		fmt.Fprintf(&b, "\nThe \"commit\" handler WOULD stage and commit these to branch %q so the child could see them. To allow this, a human must run `ctxloom manage commit trust` (or answer yes to the dirty-tree question in `ctxloom init`) — this acknowledgement (%s) is a human act only; it cannot be set from config.yaml, an environment variable, or any per-call parameter.\n\n", branch, commitDirtyTreeAckKey)
		b.WriteString(`Or, for this call, choose a different handler instead: dirty_tree_handler: "copy" (reproduce these changes as uncommitted WIP inside the child's worktree — nothing committed), "stale" (spawn the child without these changes), "fail" (refuse the spawn outright).`)
		return errors.New(b.String())
	}

	preview := renderCommitPreview(agentName, workDir, branch, files)
	clidiag.Warn("ctxloom", "%s", preview)

	sha, changed, cerr := gitClient.CommitAll(ctx, workDir, renderCommitMessage(agentName))
	if cerr != nil {
		return fmt.Errorf(`dirty_tree_handler "commit": committing %s: %w`, workDir, cerr)
	}
	if len(changed) == 0 {
		return fmt.Errorf(`dirty_tree_handler "commit": commit %s on branch %q reports success but captured NO changed files versus its parent — refusing to spawn against what may be an empty commit (this codebase has a documented history of commits landing empty; inspect %s by hand before retrying)`, sha, branch, workDir)
	}
	return nil
}

// renderCommitPreview is the "commit" handler's warning — shown before every
// individual auto-commit, regardless of the standing project acknowledgement
// (the ack authorizes the BEHAVIOR CLASS once; this keeps each individual
// mutation visible). Names the branch, the bounded file list, that this is a
// configured-handler side effect (not an incidental one), and the
// alternatives.
func renderCommitPreview(agentName, workDir, branch string, files dirtyFileList) string {
	var b strings.Builder
	fmt.Fprintf(&b, "agent_run: dirty_tree_handler \"commit\" is about to commit %s's uncommitted changes on branch %q so delegated agent %q can see them in its own git worktree (a worktree checkout only ever contains committed state):\n", workDir, branch, agentName)
	files.writeTo(&b)
	b.WriteString(`This is happening because dirty_tree_handler is configured to "commit" (the default, acknowledged via `)
	b.WriteString(commitDirtyTreeAckKey)
	b.WriteString(`, set by ctxloom init or ctxloom manage commit trust). Alternatives for this call: dirty_tree_handler "copy" (reproduce these changes as uncommitted WIP inside the child's worktree instead of committing them here), "stale" (spawn the child without these changes), "fail" (refuse the spawn instead of touching this branch); or pass workspace: "none" to run against the live checkout instead.`)
	return b.String()
}

// renderCommitMessage is the exact, self-explanatory commit message format
// for every dirty_tree_handler: "commit" auto-commit — readable in `git log`
// with no other context: WHO did it (ctxloom, automatically), WHY (so a
// named delegated agent's worktree could see it), and that it is a
// configured, reversible choice (naming the handler and its alternatives).
func renderCommitMessage(agentName string) string {
	return fmt.Sprintf(`ctxloom: auto-commit for delegated agent spawn (dirty_tree_handler=commit)

ctxloom committed this working tree automatically before spawning delegated
agent %q into its own git worktree: a worktree checkout only ever contains
committed state, so these changes would otherwise be invisible to the child.

This is the configured dirty_tree_handler behavior ("commit", the default).
Alternatives: "copy" (reproduce these changes as uncommitted WIP inside the
child's worktree instead of committing them here), "stale" (spawn the child
without these changes), "fail" (refuse the spawn instead of touching this
branch). Set dirty_tree_handler in .ctxloom/config.yaml, or pass it per call
to agent_run.
`, agentName)
}

// applyCopySnapshot reproduces snap (captured from the parent BEFORE the
// worktree existed) inside targetDir (the now-created worktree): the tracked
// patch first (git apply — modifications and deletions), then every
// untracked file verbatim. FAILS LOUDLY on any error, including a mid-loop
// untracked-file failure — it never half-applies and continues, matching
// CommitAll's own no-silent-partial-success contract.
func applyCopySnapshot(ctx context.Context, gitClient git.Git, targetDir string, snap *copySnapshot) error {
	if snap.patch != "" {
		applied, err := gitClient.ApplyPatch(ctx, targetDir, snap.patch)
		if err != nil {
			return fmt.Errorf(`dirty_tree_handler "copy": reproducing tracked changes into %s: %w`, targetDir, err)
		}
		// ApplyPatch reports applied=false (no error) for a patch
		// it considered empty/whitespace-only. snap.patch is non-empty here,
		// so applied=false means DiffPatch handed us content ApplyPatch does
		// not consider real — refuse rather than silently reproducing NONE
		// of the parent's tracked changes while reporting success.
		if !applied {
			return fmt.Errorf(`dirty_tree_handler "copy": %s's captured tracked-change patch was not applied (reported as empty) — refusing to proceed as if it had been`, targetDir)
		}
	}
	for _, rel := range snap.untracked {
		if err := copyUntrackedFile(snap.sourceDir, targetDir, rel); err != nil {
			return fmt.Errorf(`dirty_tree_handler "copy": reproducing untracked file %q into %s: %w`, rel, targetDir, err)
		}
	}
	return nil
}

// copyUntrackedFile reproduces one untracked entry from sourceDir/rel at
// targetDir/rel, creating any needed parent directories. `git ls-files
// --others` lists exactly two kinds of entry, and both are reproduced: a
// regular file (bytes + permission bits) and a SYMLINK (recreated as a link
// carrying the same target text — never dereferenced into a copy of what it
// points at, which would silently turn a link into a file and, for a link
// pointing outside the tree, bake a host path into the child's worktree).
// Anything else cannot be reproduced, and is refused rather than skipped:
// applyCopySnapshot's contract is that it never half-applies and continues.
func copyUntrackedFile(sourceDir, targetDir, rel string) error {
	src := filepath.Join(sourceDir, rel)
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	dst := filepath.Join(targetDir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, rerr := os.Readlink(src)
		if rerr != nil {
			return rerr
		}
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return err
		}
		return os.Symlink(target, dst)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file or symlink (mode %s) — ctxloom cannot reproduce it in the child's worktree", info.Mode().Type())
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode().Perm())
}

// Abort tears down a prepared-but-never-started launch's workspace.
