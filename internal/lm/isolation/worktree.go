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

// worktreeBaseRef is the ref each per-agent worktree is checked out to: HEAD,
// DETACHED (T0.2). Ephemeral per-agent checkouts are never branch-per-agent in
// this phase — detached avoids "branch already checked out elsewhere" collisions
// between concurrent members and leaves no stray branches to clean up.
const worktreeBaseRef = "HEAD"

// worktreeTeardownTimeout bounds the WIP-safe teardown's git calls so a wedged
// git can't hang a member's Cleanup forever.
const worktreeTeardownTimeout = 30 * time.Second

// Worktree is the fan-out CONFIG-isolation policy: each member runs in its own
// per-agent git worktree, so the existing native writers (.mcp.json/.claude/
// AGENTS.md/.kiro/) populate an isolated cwd instead of clobbering the one shared
// project surface. It is NOT a security boundary — only container bypasses
// approvals — so approvals stay Prompt. SpawnClient is the SAME bare self-invoked
// subprocess as None; the isolation is expressed purely via the worktree cwd
// (RunOptions.WorkDir) plus per-agent config-home envs. Not a git repo, or the
// worktree add fails → PrepareWorkspace errors so the caller degrades to None
// (warn, never block — CLAUDE.md).
type Worktree struct {
	git     git.Git
	baseRef string
	// state is the run's session identity, stamped by Prepare
	// (withSessionState). A known harp homes the per-agent scratch (checkout +
	// config-home) under the session's ephemeral/ dir instead of the OS temp
	// dir — one place to inspect a session's regenerable state (§6d). Zero on
	// paths without session accounting → the OS temp dir, exactly as before.
	state SessionState
}

// Ensure Worktree satisfies the Policy interface.
var _ Policy = Worktree{}

// NewWorktree builds a worktree policy over the given Git seam. A nil Git uses the
// default git-binary implementation; tests pass a git.Fake to drive the lifecycle
// and the WIP-safe teardown without a real repo.
func NewWorktree(g git.Git) Worktree {
	if g == nil {
		g = git.NewExec()
	}
	return Worktree{git: g, baseRef: worktreeBaseRef}
}

// Name identifies the policy.
func (Worktree) Name() string { return "worktree" }

// Approvals keeps the engine's in-tool prompt: a worktree isolates CONFIG, not
// the blast radius. Only container (a real boundary) bypasses.
func (Worktree) Approvals() Approvals { return ApprovalsPrompt }

// PrepareWorkspace creates a fresh detached worktree for the member. It errors
// (→ caller degrades to None) when projectDir is not a git repo or the worktree
// add fails. On success it also, best-effort, (a) provisions a per-agent
// config-home dir whose CLAUDE_CONFIG_DIR/CODEX_HOME/KIRO_HOME isolate the engine
// GLOBAL layer, and (b) writes the broadened ctxloom-config excludes to the shared
// common-dir .git/info/exclude so a developer member's merge-back never carries
// per-agent config (§3.1). Neither best-effort step fails the workspace.
func (w Worktree) PrepareWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error) {
	if !w.git.IsRepo(projectDir) {
		// The caller degrades to None (shared cwd). NOTE the user edge: concurrent
		// members in a NON-git repo share the one cwd and lose config isolation —
		// worktrees are the only mechanism that restores it, and a non-git tree has
		// none. Fault tolerance wins: warn + shared cwd beats blocking the LLM.
		return nil, fmt.Errorf("worktree isolation: %q is not a git repository", projectDir)
	}

	wtPath := worktreeScratchPath(w.scratchBase(), "ctxloom-wt", agentID)
	if err := w.git.WorktreeAdd(ctx, projectDir, wtPath, w.baseRef); err != nil {
		return nil, fmt.Errorf("worktree add: %w", err)
	}

	ws := &worktreeWorkspace{
		git:     w.git,
		repoDir: projectDir,
		dir:     wtPath,
	}
	ws.configHome = w.provisionConfigHome(agentID)
	w.excludeConfigFromMerge(ctx, projectDir)
	w.skipTrackedConfig(ctx, wtPath)
	return ws, nil
}

// SpawnClient launches the bare self-invoked plugin subprocess — identical to
// None. The worktree is expressed purely via the caller's RunOptions.WorkDir, so
// no per-workspace launch machinery is needed here.
func (Worktree) SpawnClient(backendName, label string, verbosity int, _ Workspace) (pb.Client, error) {
	return pb.NewSelfInvokingClientForLabel(backendName, label, verbosity)
}

// provisionConfigHome creates the per-agent config-home root (P2, T0.6). Returns
// "" on failure — the run still proceeds against the shared global config (warn),
// never blocking. The scoped envs are preferred over a per-session HOME, which
// would strip ~/.gitconfig/ssh identity the worktree still needs.
func (w Worktree) provisionConfigHome(agentID string) string {
	home := worktreeScratchPath(w.scratchBase(), "ctxloom-cfg", agentID)
	// 0700 like every MkdirTemp sibling in this package: the dir holds engine
	// creds/state (CLAUDE_CONFIG_DIR & co.) in the SHARED OS temp dir — never
	// world-traversable.
	if err := os.MkdirAll(home, 0o700); err != nil {
		clidiag.Warn("ctxloom", "worktree: per-agent config-home unavailable (using shared global config): %v", err)
		return ""
	}
	return home
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
// worktree checkout, Env() the per-agent config-home envs (EnvWorkspace), and
// Cleanup() the WIP-safe, nested-worktree-aware teardown.
type worktreeWorkspace struct {
	git        git.Git
	repoDir    string
	dir        string
	configHome string
}

// Ensure the workspace exposes its per-agent config-home envs.
var _ EnvWorkspace = (*worktreeWorkspace)(nil)

// Dir returns the worktree checkout the member's engine runs in.
func (w *worktreeWorkspace) Dir() string { return w.dir }

// Env returns the per-agent config-home envs that isolate each engine's GLOBAL
// config layer (T0.6). Empty when no config-home could be provisioned.
func (w *worktreeWorkspace) Env() map[string]string {
	if w.configHome == "" {
		return nil
	}
	return map[string]string{
		"CLAUDE_CONFIG_DIR": filepath.Join(w.configHome, "claude"),
		"CODEX_HOME":        filepath.Join(w.configHome, "codex"),
		"KIRO_HOME":         filepath.Join(w.configHome, "kiro"),
	}
}

// Cleanup runs the WIP-safe, repo-worktree-aware teardown, then removes the
// per-agent config-home. Idempotent (guarded by clearing dir). It NEVER returns
// an error — every git inability warns and continues (fault tolerance), and WIP
// is sacred: an inner worktree with uncommitted work, or an unknowable state,
// leaves the whole tree in place rather than risk destroying it.
func (w *worktreeWorkspace) Cleanup() error {
	if w.dir == "" {
		return nil
	}
	target := w.dir
	w.dir = ""

	ctx, cancel := context.WithTimeout(context.Background(), worktreeTeardownTimeout)
	defer cancel()
	w.teardown(ctx, target)

	if w.configHome != "" {
		home := w.configHome
		w.configHome = ""
		if err := os.RemoveAll(home); err != nil {
			warnCleanupResidue("per-agent config-home", home, err)
		}
	}
	return nil
}

// teardown removes the target worktree WIP-safely and nested-worktree-aware:
//  1. list the repo-global worktrees; if that fails, LEAK the target rather than
//     blind-remove it (a nested inner's WIP could be silently destroyed).
//  2. remove any worktree nested UNDER the target INNER-FIRST — but only after a
//     WIP check; a dirty (or unknowable) inner aborts the whole teardown (git's
//     own dirty-check misses these, which is exactly how nested WIP gets lost).
//  3. remove the target itself with force=false (git refuses a dirty tree — a
//     second WIP guard), then prune.
func (w *worktreeWorkspace) teardown(ctx context.Context, target string) {
	list, err := w.git.WorktreeList(ctx, w.repoDir)
	if err != nil {
		clidiag.Warn("ctxloom", "worktree teardown: cannot list worktrees; leaving %q in place to avoid destroying nested work: %v", target, err)
		return
	}

	for _, inner := range nestedUnder(list, target) {
		dirty, derr := w.git.IsDirty(ctx, inner.Path)
		if derr != nil || dirty {
			clidiag.Warn("ctxloom", "worktree teardown: nested worktree %q has uncommitted work (or unknown state); leaving %q in place to preserve it", inner.Path, target)
			return
		}
		if err := w.git.WorktreeRemove(ctx, w.repoDir, inner.Path, false); err != nil {
			clidiag.Warn("ctxloom", "worktree teardown: cannot remove nested worktree %q; leaving %q in place: %v", inner.Path, target, err)
			return
		}
	}

	if dirty, derr := w.git.IsDirty(ctx, target); derr != nil || dirty {
		clidiag.Warn("ctxloom", "worktree %q has uncommitted changes (or unknown state); leaving it in place to preserve WIP", target)
		return
	}
	if err := w.git.WorktreeRemove(ctx, w.repoDir, target, false); err != nil {
		clidiag.Warn("ctxloom", "worktree teardown: cannot remove %q: %v", target, err)
		return
	}
	if err := w.git.WorktreePrune(ctx, w.repoDir); err != nil {
		clidiag.Warn("ctxloom", "worktree prune failed: %v", err)
	}
}

// nestedUnder returns the worktrees strictly nested inside target, DEEPEST-FIRST
// (by path-separator depth) so inner worktrees are handled before their parents.
func nestedUnder(list []git.Worktree, target string) []git.Worktree {
	prefix := target + string(os.PathSeparator)
	var nested []git.Worktree
	for _, wt := range list {
		if wt.Path != target && strings.HasPrefix(wt.Path, prefix) {
			nested = append(nested, wt)
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
	id := containerNameSafe.ReplaceAllString(agentID, "-")
	id = strings.Trim(id, "-._")
	if id == "" {
		id = "agent"
	}
	return filepath.Join(base, fmt.Sprintf("%s-%s-%s", prefix, id, randToken()))
}
