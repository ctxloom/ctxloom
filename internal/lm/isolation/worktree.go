package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// worktreeBaseRef is the ref each per-agent worktree's branch starts from:
// the project's HEAD at the moment the member is prepared.
const worktreeBaseRef = "HEAD"

// worktreeBranchPrefix is the ref namespace every per-agent worktree branch
// lives under (the naming standard's short-lived `<type>/<desc>` form). The
// branch is NAMED, never detached: a member's commits are then held by the
// shared repository and survive the checkout's teardown, which is what lets
// the coordinator merge from it after the member is gone. The uniqueness
// token in the checkout's name (worktreeScratchPath) is what keeps
// concurrent members from colliding on "branch already checked out".
const worktreeBranchPrefix = "ctxloom/"

// worktreeTeardownTimeout bounds the WIP-safe teardown's git calls so a wedged
// git can't hang a member's Cleanup forever.
const worktreeTeardownTimeout = 30 * time.Second

// worktreeScratchPrefix names every per-agent worktree checkout this policy
// creates (worktreeScratchPath's prefix arg in PrepareWorkspace) — shared with
// worktree_reap.go's startup sweep so it can find exactly these directories
// under a session's ephemeral/ dir without drifting from what PrepareWorkspace
// actually names them.
const worktreeScratchPrefix = "ctxloom-wt"

// Worktree is the fan-out CONFIG-isolation policy: each member runs in its own
// per-agent git worktree, so the existing native writers (.mcp.json/.claude/
// AGENTS.md) populate an isolated cwd instead of clobbering the one shared
// project surface. It is NOT a security boundary — only container bypasses
// approvals — so approvals stay Prompt. SpawnClient is the SAME bare self-invoked
// subprocess as None; the isolation is expressed purely via the worktree cwd
// (RunOptions.WorkDir) plus the per-agent scratch and git identity its Env()
// carries. The engine's config home is NOT this policy's to provide: it is
// decided off the agent binding for every cell (operations.ResolveInTreeAgentHome),
// so a worktree run and a live-tree run with the same binding share the same
// answer. Not a git repo, or the worktree add fails → PrepareWorkspace errors
// so the caller degrades to None. A lost worktree is a WORKSPACE-axis degrade
// (config isolation only, not a security boundary), so it stays a silent
// warn-and-continue — unlike a lost CONTAINER boundary, which is fatal unless
// --degraded. The ONE exception (sizable-antler): a resume whose deterministic
// per-agent path (checkoutPath) is already occupied never falls into that
// silent degrade — reuseExistingCheckout either verifies the occupant as this
// agent's own surviving checkout and REUSES it, or REFUSES loud, naming the
// path. A degrade may tolerate a problem; it must never grant a bypass that
// strands an agent's own prior work while it writes, unknowingly, into the
// live project tree instead.
type Worktree struct {
	git git.Git
	// state is the run's session identity, stamped by Prepare
	// (withSessionState). A known harp homes the per-agent scratch (checkout +
	// toolchain temp) under the session's ephemeral/ dir instead of the OS
	// temp dir — one place to inspect a session's regenerable state (§6d).
	// Zero on paths without session accounting → the OS temp dir.
	state SessionState
}

// Ensure Worktree satisfies the Policy interface.
var _ Policy = Worktree{}

// NewWorktree builds a worktree policy over the given Git seam. A nil Git uses
// the default git-binary implementation; tests pass a git.Fake to drive the
// lifecycle and the WIP-safe teardown without a real repo.
func NewWorktree(g git.Git) Worktree {
	if g == nil {
		g = git.NewExec()
	}
	return Worktree{git: g}
}

// Name identifies the policy.
func (Worktree) Name() string { return "worktree" }

// ResolveWorkspace creates a worktree for the member on its own named branch
// (worktreeBranchName), OR — when a session harp is known (checkoutPath) and
// a resume finds its own prior checkout still standing at the deterministic
// path a WIP-preserving teardown left it at — REUSES that checkout in place
// (reuseExistingCheckout) instead of adding a new one. It errors when
// projectDir is not a git repo, the worktree add fails, or the resume path is
// already occupied by something that cannot be VERIFIED as this agent's own
// leftover: a bare path collision is never treated as proof of ownership
// (sizable-antler), so an unverifiable occupant is a REFUSAL, not a
// degrade — the caller must NOT silently continue with no workspace
// isolation the way a genuine "not a repo"/"add failed" error still degrades
// to None for. On success it also, best-effort, provisions the member's
// toolchain scratch dir and writes the broadened ctxloom-config excludes to
// the shared common-dir .git/info/exclude so a developer member's merge-back
// never carries per-agent config (§3.1). Neither best-effort step fails the
// workspace.
//
// The deferred recover below exists because the worktree checkout and
// the scratch dir provisioned after it are real on-disk resources created
// BEFORE this function returns a Workspace the caller could Cleanup() — if
// anything after WorktreeAdd panics (a bug in excludeConfigFromMerge/
// skipTrackedConfig, or — the case that surfaced this — a mutation-testing
// mutant deliberately breaking one of them), the caller never gets a handle
// to clean up, and the checkout + scratch home leak under the OS temp dir
// with nothing left to remove them. Recovering here, best-effort removing
// what THIS call created, and re-panicking preserves the original failure (a
// real bug still crashes / a mutant still gets killed) while guaranteeing no
// resource outlives the call that made it.
func (w Worktree) ResolveWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error) {
	if !w.git.IsRepo(projectDir) {
		// The caller degrades to None (shared cwd). NOTE the user edge: concurrent
		// members in a NON-git repo share the one cwd and lose config isolation —
		// worktrees are the only mechanism that restores it, and a non-git tree has
		// none. Fault tolerance wins: warn + shared cwd beats blocking the LLM.
		return nil, fmt.Errorf("worktree isolation: %q is not a git repository", projectDir)
	}

	wtPath := w.checkoutPath(agentID)
	reused, err := w.reuseExistingCheckout(ctx, projectDir, wtPath)
	if err != nil {
		return nil, err
	}
	if !reused {
		if err := w.git.WorktreeAdd(ctx, projectDir, wtPath, worktreeBranchName(wtPath), worktreeBaseRef); err != nil {
			return nil, fmt.Errorf("worktree add: %w", err)
		}
	}
	ws := &worktreeWorkspace{
		git:     w.git,
		repoDir: projectDir,
		dir:     wtPath,
		agentID: agentID,
	}
	defer func() {
		if r := recover(); r != nil {
			// A REUSED checkout is a resumed agent's own preserved WIP, not
			// something this call created — the recovery below must remove
			// only what THIS call is responsible for, or a mutant/bug in the
			// best-effort steps after this point would DESTROY the very work
			// sizable-antler exists to protect (a degrade must never do
			// damage — see obstinate-judiciary). Only a freshly-added
			// checkout is unwound here.
			if !reused {
				_ = os.RemoveAll(ws.dir)
				// Removing the directory does not retire the repo's
				// administrative registration of it (.git/worktrees/<name>) —
				// that is what prune is for, and the graceful teardown below
				// runs it for the same reason. Without it every recovered
				// panic leaves a `git worktree list` entry naming a path that
				// no longer exists. A FRESH context: the caller's may already
				// be cancelled, and this unwind must still complete.
				pruneCtx, cancelPrune := context.WithTimeout(context.Background(), worktreeTeardownTimeout)
				_ = w.git.WorktreePrune(pruneCtx, projectDir)
				cancelPrune()
			}
			if ws.scratchDir != "" {
				_ = os.RemoveAll(ws.scratchDir)
			}
			panic(r)
		}
	}()
	ws.scratchDir = w.provisionScratchDir(agentID)
	w.excludeConfigFromMerge(ctx, projectDir)
	w.skipTrackedConfig(ctx, wtPath)
	return ws, nil
}

// Mount maps nothing. Like None, the worktree policy runs the engine on the
// HOST, directly inside the checkout ResolveWorkspace created — there is no
// second environment to map the tree into. The toolchain-scratch and git
// identity env the worktree does provide rides worktreeWorkspace.Env (the
// EnvWorkspace seam the spawn already consults), not a mount plan.
func (Worktree) Mount(context.Context, Workspace) (MountPlan, error) { return MountPlan{}, nil }

// PrepareWorkspace resolves and maps in one step (see prepareWorkspace).
func (w Worktree) PrepareWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error) {
	return prepareWorkspace(ctx, w, projectDir, agentID)
}

// provisionScratchDir creates the per-agent TOOLCHAIN scratch root
// (worktreeWorkspace.Env()'s TMPDIR/GOTMPDIR) under the same scratchBase as
// the checkout — the session's ephemeral/ dir when a harp is known, else the
// OS temp dir. This is the fix for the shared-/tmp toolchain
// contention that corrupted concurrent agents (spawner-env audit): every
// worktree member previously inherited the SAME process TMPDIR, so `go
// build`'s per-invocation $WORK scratch, `git`'s temp blobs, and any other
// tool honouring TMPDIR collided across members. Returns "" on the MkdirAll
// failure — best-effort, never blocking the run; Env() then simply omits
// TMPDIR/GOTMPDIR and the child falls back to the shared process default.
func (w Worktree) provisionScratchDir(agentID string) string {
	dir := worktreeScratchPath(w.scratchBase(), "ctxloom-tmp", agentID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		clidiag.Warn("ctxloom", "worktree: per-agent scratch dir unavailable (toolchain temp state will share the process default): %v", err)
		_ = os.RemoveAll(dir)
		return ""
	}
	return dir
}

// SpawnClient launches the bare self-invoked plugin subprocess via the Host
// runtime — identical to None. The worktree is expressed purely via the caller's
// RunOptions.WorkDir, so no per-workspace launch machinery is needed here.
func (Worktree) SpawnClient(backendName, label string, verbosity int, ws Workspace, spawnEnv map[string]string) (pb.Client, error) {
	// Identical spawn to None (the workspace rides RunOptions.WorkDir, not
	// the spawn) — call the one unit rather than duplicate it.
	return None{}.SpawnClient(backendName, label, verbosity, ws, spawnEnv)
}

// StartRunner launches the bare `ctxloom llm host` runner — identical to None
// (the worktree rides RunOptions.WorkDir, not the spawn), so it defers to the
// one unit rather than duplicate it.
func (Worktree) StartRunner(ctx context.Context, backendName, label string, verbosity int, ws Workspace, spawnEnv map[string]string) (*RunnerHandle, error) {
	return None{}.StartRunner(ctx, backendName, label, verbosity, ws, spawnEnv)
}

// excludeConfigFromMerge writes the broadened ctxloom-config exclude block to the
// repo's shared common-dir .git/info/exclude (§3.1). Best-effort: any failure
// warns and returns — the run continues (the excludes only matter for a
// merge-back, which fan-out members do not perform in this phase). Idempotent and
// shared-safe: EnsureFile only appends missing patterns, so concurrent members
// converge on the same block.
func (w Worktree) excludeConfigFromMerge(ctx context.Context, projectDir string) {
	common, err := w.git.CommonDir(ctx, projectDir)
	if err != nil {
		clidiag.Warn("ctxloom", "worktree: cannot resolve git common dir for config excludes: %v", err)
		return
	}
	info := filepath.Join(common, "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		clidiag.Warn("ctxloom", "worktree: cannot prepare %q for config excludes: %v", info, err)
		return
	}
	exclude := filepath.Join(info, "exclude")
	if err := gitignore.EnsureFile(exclude, gitignore.WorktreeComment, gitignore.WorktreeArtifactPatterns...); err != nil {
		clidiag.Warn("ctxloom", "worktree: cannot write config excludes to %q: %v", exclude, err)
	}
}

// skipTrackedConfig hides ctxloom's per-agent config edits from a developer
// member's merge-back by setting the skip-worktree bit on any TRACKED config file
// in the worktree (§3.1). The info/exclude above covers the UNTRACKED case; this
// covers the case where the repo genuinely tracks a config path (e.g. a committed
// .mcp.json) that the member's Setup will overwrite. Best-effort: any git inability
// warns and continues — the run still proceeds (the bit only matters for a
// merge-back, which fan-out members do not perform in this phase).
func (w Worktree) skipTrackedConfig(ctx context.Context, wtPath string) {
	tracked, err := w.git.ListTracked(ctx, wtPath, gitignore.WorktreeArtifactPatterns...)
	if err != nil {
		clidiag.Warn("ctxloom", "worktree: cannot list tracked config for skip-worktree: %v", err)
		return
	}
	for _, f := range tracked {
		if err := w.git.UpdateIndexSkipWorktree(ctx, wtPath, f, true); err != nil {
			clidiag.Warn("ctxloom", "worktree: cannot skip-worktree tracked config %q: %v", f, err)
		}
	}
}

// worktreeWorkspace is the Worktree policy's workspace: Dir() is the per-agent
// worktree checkout, Env() the env for what the worktree provisioned
// (EnvWorkspace), and Cleanup() the WIP-safe, nested-worktree-aware teardown.
type worktreeWorkspace struct {
	git     git.Git
	repoDir string
	dir     string
	// agentID is the member label PrepareWorkspace was given (Worktree.
	// PrepareWorkspace's agentID param, copied at construction) — Env() uses
	// it to build the per-agent git identity (gitIdentity) and
	// provisionScratchDir uses it to name the scratch dir.
	agentID string
	// scratchDir is the per-agent TOOLCHAIN scratch root (provisionScratchDir)
	// that Env() points TMPDIR/GOTMPDIR at — scoping build/VCS temp-file
	// traffic per agent. "" when provisioning failed (best-effort; Env() then
	// omits both vars).
	scratchDir string
}

// Ensure the workspace exposes the env for what it provisioned.
var _ EnvWorkspace = (*worktreeWorkspace)(nil)

// Dir returns the worktree checkout the member's engine runs in.
func (w *worktreeWorkspace) Dir() string { return w.dir }

// Env returns the env for what this worktree provisioned — and ONLY that.
// It never names an engine's config home: that is decided off the agent
// binding for every cell (operations.ResolveInTreeAgentHome), and a worktree
// that set it would make a run's home depend on which workspace it picked.
// HOME itself is left untouched, deliberately: a blanket HOME override would
// strip the ~/.gitconfig/~/.ssh identity the worktree still needs for git.
//
// Empty when neither a scratch dir nor a git identity could be provisioned.
//
//   - TMPDIR / GOTMPDIR: provisionScratchDir's per-agent root, when
//     provisioning succeeded. Deliberately NOT GOCACHE — Go's build cache is
//     content-addressed and safe for concurrent multi-process use; the
//     failure this fixes is in the per-invocation $WORK scratch dir under
//     TMPDIR, which is not.
//   - GIT_AUTHOR_{NAME,EMAIL} / GIT_COMMITTER_{NAME,EMAIL}: these env vars
//     outrank every file-based git config INCLUDING repo-local — the only
//     lever that reaches a linked worktree's shared .git/config, which a
//     scoped HOME/XDG var cannot touch (gitIdentity's doc).
func (w *worktreeWorkspace) Env() map[string]string {
	env := map[string]string{}
	if w.scratchDir != "" {
		env["TMPDIR"] = w.scratchDir
		env["GOTMPDIR"] = w.scratchDir
	}
	name, email := gitIdentity(w.agentID)
	if name != "" {
		env["GIT_AUTHOR_NAME"] = name
		env["GIT_AUTHOR_EMAIL"] = email
		env["GIT_COMMITTER_NAME"] = name
		env["GIT_COMMITTER_EMAIL"] = email
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

// gitIdentity returns the GIT_AUTHOR_NAME/GIT_AUTHOR_EMAIL an agent's commits
// are attributed to (GIT_COMMITTER_* mirrors them — callers set all four to the
// same pair). It never impersonates the human: the name always self-identifies
// as an agent, and the email rides a synthetic "agents.ctxloom.local" domain
// that deliberately resolves nowhere, so it can never collide with — or be
// mistaken for — a real person's address. agentID (the resolved agent's name,
// or the run's own session harp for the top-level session — see
// PrepareWorkspace's caller doc) is the traceable part: it is what makes an
// agent's commits attributable to THAT agent rather than a generic "ctxloom"
// identity, and what stopped `git config user.email` leaking from a worktree
// into the shared main checkout from mattering — the scoped env wins over
// repo-local config regardless of what the shared .git/config says. Empty
// agentID (no backend/agent context) is a no-op: name is "" and callers omit
// all four vars, falling back to whatever the process/host git config resolves.
//
// This is a package-level helper (not a method) so BOTH isolation halves derive
// the identity from the SAME format in ONE place: the host+worktree path
// (worktreeWorkspace.Env) and the container path (Container.PrepareWorkspace,
// which the wrapped Worktree's Env() never reaches — containerWorkspace does not
// implement EnvWorkspace).
func gitIdentity(agentID string) (name, email string) {
	if agentID == "" {
		return "", ""
	}
	return "ctxloom agent " + agentID, sanitizeAgentID(agentID) + "@agents.ctxloom.local"
}

// Cleanup runs the WIP-safe, repo-worktree-aware teardown, then removes the
// provisioned toolchain scratchDir. Idempotent (guarded by clearing dir). It NEVER
// returns an error — every git inability warns and continues (fault
// tolerance), and WIP is sacred: an inner worktree with uncommitted work, or
// an unknowable state, leaves the whole tree in place rather than risk
// destroying it.
func (w *worktreeWorkspace) Cleanup() error {
	if w.dir == "" {
		return nil
	}
	target := w.dir
	w.dir = ""

	ctx, cancel := context.WithTimeout(context.Background(), worktreeTeardownTimeout)
	defer cancel()
	teardownWorktree(ctx, w.git, w.repoDir, target)

	if w.scratchDir != "" {
		dir := w.scratchDir
		w.scratchDir = ""
		if err := os.RemoveAll(dir); err != nil {
			warnCleanupResidue("per-agent toolchain scratch dir", dir, err)
		}
	}
	return nil
}

// teardownWorktree removes the target worktree WIP-safely and
// nested-worktree-aware. It is a package-level function, not a method: it needs
// only a Git seam and the owning repo dir, and BOTH callers hold those without
// holding a workspace — the graceful worktreeWorkspace.Cleanup path, and the
// startup reaper (worktree_reap.go), whose candidates are orphans no live
// workspace value describes.
//  1. list the repo-global worktrees; if that fails, LEAK the target rather than
//     blind-remove it (a nested inner's WIP could be silently destroyed).
//  2. remove any worktree nested UNDER the target INNER-FIRST — but only after a
//     WIP check; a dirty (or unknowable) inner aborts the whole teardown (git's
//     own dirty-check misses these, which is exactly how nested WIP gets lost).
//  3. remove the target itself with force=false (git refuses a dirty tree — a
//     second WIP guard), then prune.
func teardownWorktree(ctx context.Context, g git.Git, repoDir, target string) {
	list, err := g.WorktreeList(ctx, repoDir)
	if err != nil {
		clidiag.Warn("ctxloom", "worktree teardown: cannot list worktrees; leaving %q in place to avoid destroying nested work: %v", target, err)
		return
	}

	for _, inner := range nestedUnder(list, target) {
		if unsafe, reason := unsafeToRemove(ctx, g, inner.Path); unsafe {
			clidiag.Warn("ctxloom", "worktree teardown: nested worktree %q %s; leaving %q in place to preserve it", inner.Path, reason, target)
			return
		}
		if err := g.WorktreeRemove(ctx, repoDir, inner.Path); err != nil {
			clidiag.Warn("ctxloom", "worktree teardown: cannot remove nested worktree %q; leaving %q in place: %v", inner.Path, target, err)
			return
		}
	}

	if unsafe, reason := unsafeToRemove(ctx, g, target); unsafe {
		clidiag.Warn("ctxloom", "worktree %q %s; leaving it in place to preserve WIP", target, reason)
		return
	}
	if err := g.WorktreeRemove(ctx, repoDir, target); err != nil {
		clidiag.Warn("ctxloom", "worktree teardown: cannot remove %q: %v", target, err)
		return
	}
	if err := g.WorktreePrune(ctx, repoDir); err != nil {
		clidiag.Warn("ctxloom", "worktree prune failed: %v", err)
	}
	// NOT auto-retiring the shared config-exclude block here.
	// A first draft called gitignore.RetireWorktreeConfigBlock once no
	// linked worktree remained, and it regressed a live, currently-passing
	// acceptance contract — tests/acceptance/features/journeys/j002200_isolation.feature's
	// "A worktree run leaves the project tree clean" asserts the shared
	// common-dir info/exclude STILL carries the ctxloom worktree-config
	// block immediately after a single worktree's teardown (the scenario's
	// own comment: proof the per-agent config edits were hidden from the
	// shared tree during the run, not proof the mechanism was torn down
	// again right after). Auto-retiring on every ordinary single-agent
	// teardown would strip that evidence — and, worse, cause the block to
	// flap in and out across back-to-back agent runs, re-triggering the
	// exact "won't delete / deletes when it must not" instability this
	// package's own comments describe as the historical bug the block was
	// added to fix in the first place. RetireWorktreeConfigBlock is kept as
	// a tested, exported utility (gitignore.go) — the "no removal path
	// exists at all" half is fixed — but deciding WHEN it is
	// safe to invoke (process exit? an explicit gc/reap command? never
	// automatically?) is a product call, not one this batch makes alone;
	// see DECISIONS.md.
}

// unsafeToRemove is teardownWorktree's WIP-safety gate, extended past IsDirty alone:
// IsDirty's `status --porcelain` deliberately does NOT
// see gitignored/excluded content — that blindness is what lets a prepared
// agent worktree's own delivered noise (.claude/, CLAUDE.md, .ctxloom/cache/,
// written into this same repo's common-dir info/exclude) coexist with the
// WIP check at all. But it means a worktree holding ONLY ignored files reads
// clean here, and `git worktree remove` (force=false) happily deletes it —
// this was the other half of the mechanism that destroyed agent-authored
// work. An error from EITHER probe is treated the same as "dirty": an
// unreadable state must never be read as "safe to delete".
func unsafeToRemove(ctx context.Context, g git.Git, dir string) (unsafe bool, reason string) {
	if dirty, err := g.IsDirty(ctx, dir); err != nil || dirty {
		return true, "has uncommitted changes (or unknown state)"
	}
	if ignored, err := g.HasIgnoredContent(ctx, dir); err != nil || ignored {
		return true, "holds gitignored/excluded files (or unknown state)"
	}
	return false, ""
}

// retireConfigExcludeIfUnused removes the shared config-exclude block
// (§3.1, gitignore.WorktreeArtifactPatterns under gitignore.WorktreeComment)
// from the repo's common-dir info/exclude, but ONLY once no worktree other
// than the main one remains — the block lives in the repo's ONE
// shared common-dir file (git has no per-worktree info/exclude), so removing
// it while a SIBLING agent worktree is still alive would strip that
// sibling's own noise-hiding, false-dirtying it and re-triggering the exact
// destructive-teardown risk unsafeToRemove exists to catch. Best-effort:
// any failure just leaves the block in place (the safe default) rather than
// risk removing it under uncertainty.
// sameWorktreePath reports whether a and b name the same directory, tolerating
// the symlink-alias case CommonDir's own tests already guard against (macOS
// /tmp vs /private/tmp and similar): an exact string match short-circuits,
// falling back to a same-file stat comparison only when both paths exist.
// nestedUnder returns the worktrees strictly nested inside target, DEEPEST-FIRST
// (by path-separator depth) so inner worktrees are handled before their parents.
//
// Matching considers target under BOTH the spelling the caller holds and its
// realpath resolution: `git worktree list --porcelain` reports every path
// symlink-resolved, while target is whatever scratchBase built — os.TempDir()
// on macOS is /var/folders/… behind the /var → /private/var symlink, and a
// symlinked HOME does the same to the session ephemeral dir. A raw prefix
// match against one spelling then finds nothing nested. Resolution is
// best-effort: an unresolvable target (a path already removed, or one that
// never existed — several callers pass synthetic paths) simply keeps the raw
// comparison.
func nestedUnder(list []git.Worktree, target string) []git.Worktree {
	prefixes := []string{target + string(os.PathSeparator)}
	if resolved, err := filepath.EvalSymlinks(target); err == nil && resolved != target {
		prefixes = append(prefixes, resolved+string(os.PathSeparator))
	}
	var nested []git.Worktree
	for _, wt := range list {
		for _, prefix := range prefixes {
			if strings.HasPrefix(wt.Path, prefix) {
				nested = append(nested, wt)
				break
			}
		}
	}
	sep := string(os.PathSeparator)
	sort.SliceStable(nested, func(i, j int) bool {
		return strings.Count(nested[i].Path, sep) > strings.Count(nested[j].Path, sep)
	})
	return nested
}

// scratchBase picks where this worktree's per-agent scratch (checkout +
// config-home) lives: the session's ephemeral/ dir when the run carries a
// harp — regenerable state belongs in the per-session layout, and cleanup of
// the session dir sweeps it — else the OS temp dir (no session accounting, or
// the ephemeral dir cannot be prepared). Best-effort like the rest of the
// worktree half: a fallback warns and the run proceeds.
func (w Worktree) scratchBase() string {
	if !safePathSegment(w.state.Harp) {
		// An EMPTY harp is the documented no-session-accounting construction
		// and stays silent. A NON-empty harp that fails the validator is a
		// rejected value on the same untrusted channel (an env map) the
		// container path hard-errors on — reporting it is the least this side
		// can do, since the fallback silently relocates every per-agent
		// scratch resource out of the session layout the run claims to use.
		if w.state.Harp != "" {
			clidiag.WarnOnce("ctxloom", "worktree: session harp %q is not a safe path segment; per-agent scratch falls back to the OS temp dir instead of the session's ephemeral dir", w.state.Harp)
		}
		return os.TempDir()
	}
	dir, err := paths.HarpEphemeralDir(w.state.Harp)
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		clidiag.Warn("ctxloom", "worktree: session ephemeral dir unavailable (%v); using the OS temp dir", err)
		return os.TempDir()
	}
	return dir
}

// worktreeScratchPath builds a unique, ctxloom-managed scratch path under base
// (the session's ephemeral dir, or the OS temp dir — NOT inside the repo tree)
// keyed by prefix + a sanitized agent id + a random suffix, so concurrent
// members never collide.
func worktreeScratchPath(base, prefix, agentID string) string {
	return filepath.Join(base, fmt.Sprintf("%s-%s-%s", prefix, sanitizeAgentID(agentID), randToken()))
}

// checkoutPath returns the per-agent worktree CHECKOUT path (not the
// toolchain scratch dir — provisionScratchDir keeps its own random suffix
// unconditionally; only the checkout's identity needs to survive a resume).
//
// When the run carries a valid session harp, this is a pure, DETERMINISTIC
// function of (harp, agentID): no random suffix. It is stable across a
// resume because the coordinator's resume path reuses the SAME harp for the
// SAME logical run (internal/agentcoord's enqueueRun checks
// currentRun(harp) before minting a new one) — so a second ResolveWorkspace
// call for that harp+agentID always recomputes the identical path a
// WIP-preserving teardown may have left standing, which is exactly what lets
// reuseExistingCheckout find and verify it instead of the resumed agent
// silently getting a brand-new, empty checkout while its prior work sits
// invisible under a path nothing recomputes (sizable-antler). Two DIFFERENT
// agents within the same harp already get different agentIDs, so dropping
// the random suffix here introduces no new collision risk.
//
// Without a harp (no session accounting: the legacy/OS-temp-dir fallback),
// there is no stable cross-process identity to make "is this my own
// leftover" answerable at all, so this keeps the historical random-suffixed
// path unchanged — a fresh checkout every call, same as before this fix.
func (w Worktree) checkoutPath(agentID string) string {
	base := w.scratchBase()
	if safePathSegment(w.state.Harp) {
		return filepath.Join(base, fmt.Sprintf("%s-%s", worktreeScratchPrefix, sanitizeAgentID(agentID)))
	}
	return worktreeScratchPath(base, worktreeScratchPrefix, agentID)
}

// reuseExistingCheckout decides what ResolveWorkspace does when checkoutPath
// already names something on disk BEFORE this call would create it:
//
//   - Nothing there yet → (false, nil): the ordinary fresh-`worktree add`
//     path is untouched.
//   - Something there, and `git worktree list` confirms it is a linked
//     worktree of projectDir on the EXACT branch a fresh create at this path
//     would use (worktreeBranchName) → (true, nil): REUSE it — this call
//     must NOT call WorktreeAdd (git would refuse both the occupied path and
//     the already-existing branch anyway).
//   - Anything else — the list can't be read, the path isn't registered as a
//     worktree of this repo at all, or it's registered on some OTHER
//     branch — → (false, err): REFUSE, naming the path and what a human
//     should check, rather than adopting an unverified occupant or letting
//     the caller silently degrade to running with no workspace isolation.
//
// A bare path match is deliberately NOT enough on its own: sizable-antler is
// explicit that "a path collision is not proof of ownership" — the
// registered-worktree-on-the-right-branch check is what turns "something is
// sitting at the path I'd use" into "this is provably the checkout my own
// naming scheme would have produced", without requiring a second, independent
// identity channel this layer doesn't have.
func (w Worktree) reuseExistingCheckout(ctx context.Context, projectDir, wtPath string) (bool, error) {
	if _, err := os.Stat(wtPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("worktree isolation: cannot inspect existing path %q before preparing a workspace there: %w", wtPath, err)
	}

	expectedBranch := worktreeBranchName(wtPath)
	list, err := w.git.WorktreeList(ctx, projectDir)
	if err != nil {
		return false, fmt.Errorf("worktree isolation: %q already exists (likely a prior run's preserved WIP) but its ownership could not be verified: git worktree list failed: %w — refusing to reuse or overwrite it; inspect the path by hand, then either remove it or retry once git is healthy", wtPath, err)
	}
	for _, wt := range list {
		if !sameWorktreePath(wt.Path, wtPath) {
			continue
		}
		if wt.Branch != expectedBranch {
			return false, fmt.Errorf("worktree isolation: %q already exists as a git worktree but on branch %q, not the %q this agent's naming would use — refusing to adopt a workspace that is not provably this agent's own; inspect it by hand before resuming", wtPath, wt.Branch, expectedBranch)
		}
		return true, nil
	}
	return false, fmt.Errorf("worktree isolation: %q already exists but is not a registered git worktree of %q — refusing to reuse or silently run elsewhere with no workspace isolation; inspect and remove the stale path (or run the worktree reaper), then resume again", wtPath, projectDir)
}

// sameWorktreePath reports whether a and b name the same directory,
// tolerating the symlink-alias case CommonDir's own tests already guard
// against (macOS /tmp vs /private/tmp and similar) — mirroring nestedUnder's
// resolution. An exact string match short-circuits; otherwise both sides must
// resolve for the comparison to count, so an unresolvable path (removed
// mid-race, or one side simply not present) never falsely reads as a match.
func sameWorktreePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// worktreeBranchName derives the branch a per-agent checkout is created on
// from the checkout's own directory name: the scratch prefix swapped for the
// agent ref namespace, sanitized id and uniqueness token riding through. A
// pure function of the path, so whoever holds the checkout (the coordinator,
// doctor, a human triaging leftovers) can name the branch without a side
// table, and the reverse.
func worktreeBranchName(wtPath string) string {
	return worktreeBranchPrefix + strings.TrimPrefix(filepath.Base(wtPath), worktreeCandidatePrefix)
}

// sanitizeAgentID renders agentID safe for use as a single path segment or a
// git-email local-part: containerNameSafe's allowlist, trimmed of leading/
// trailing separators, falling back to "agent" when that leaves nothing
// (empty agentID, or one that is entirely disallowed characters).
func sanitizeAgentID(agentID string) string {
	id := containerNameSafe.ReplaceAllString(agentID, "-")
	id = strings.Trim(id, "-._")
	if id == "" {
		id = "agent"
	}
	return id
}
