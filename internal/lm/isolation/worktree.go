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
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// credentialSeedFixIt is the fix-it hint attached to a worktree credential-seed
// fail-loud finding (see Worktree.seedCredentials): unlike the container path's
// isolationFixIt (which points at installing/starting a runtime), the escape
// hatch here is authenticating the engine or supplying its API key — there is no
// runtime to start.
const credentialSeedFixIt = "authenticate the engine on this host (e.g. `claude login`) or set its API-key env var, or pass --degraded (env CTXLOOM_DEGRADED=1) to run this member on the shared host config instead of an isolated one"

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
// worktree add fails → PrepareWorkspace errors so the caller degrades to None.
// A lost worktree is a WORKSPACE-axis degrade (config isolation only, not a
// security boundary), so it stays a silent warn-and-continue — unlike a lost
// CONTAINER boundary, which is fatal unless --degraded.
type Worktree struct {
	git     git.Git
	baseRef string
	// state is the run's session identity, stamped by Prepare
	// (withSessionState). A known harp homes the per-agent scratch (checkout +
	// config-home) under the session's ephemeral/ dir instead of the OS temp
	// dir — one place to inspect a session's regenerable state (§6d). Zero on
	// paths without session accounting → the OS temp dir, exactly as before.
	state SessionState
	// backend is the REGISTERED backend name (e.g. "claude-code") this
	// policy's config-home is provisioned for. provisionConfigHome uses it
	// to look up a credentialSeedSpec (auth.go) and seed subscription
	// credentials into the isolated config-home — without it, an isolated
	// engine that honours its config-home var for creds too (claude) starts
	// logged out (grave-prize). Empty is valid (a bare Worktree{}, or a
	// caller with no backend context, e.g. the worktree-in-container base's
	// generic test constructor): no spec matches, so no seeding is
	// attempted — the pre-fix behavior.
	backend string
}

// Ensure Worktree satisfies the Policy interface.
var _ Policy = Worktree{}

// NewWorktree builds a worktree policy over the given Git seam for the given
// backend. A nil Git uses the default git-binary implementation; tests pass a
// git.Fake to drive the lifecycle and the WIP-safe teardown without a real repo.
// backend is the registered backend name (e.g. "claude-code"); pass "" when no
// backend context is available (config-home credential seeding is then skipped —
// see the Worktree.backend field doc).
func NewWorktree(g git.Git, backend string) Worktree {
	if g == nil {
		g = git.NewExec()
	}
	return Worktree{git: g, baseRef: worktreeBaseRef, backend: backend}
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
//
// The deferred recover below exists because the worktree checkout and the
// config-home are real on-disk resources created BEFORE this function returns
// a Workspace the caller could Cleanup() — if anything after WorktreeAdd
// panics (a bug in excludeConfigFromMerge/skipTrackedConfig, or — the case
// that surfaced this — a mutation-testing mutant deliberately breaking one of
// them), the caller never gets a handle to clean up, and the checkout +
// config-home leak under the OS temp dir with nothing left to remove them.
// Recovering here, best-effort removing what THIS call created, and
// re-panicking preserves the original failure (a real bug still crashes / a
// mutant still gets killed) while guaranteeing no resource outlives the call
// that made it.
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
	defer func() {
		if r := recover(); r != nil {
			_ = os.RemoveAll(ws.dir)
			if ws.configHome != "" {
				_ = os.RemoveAll(ws.configHome)
			}
			panic(r)
		}
	}()
	ws.configHome = w.provisionConfigHome(agentID)
	w.excludeConfigFromMerge(ctx, projectDir)
	w.skipTrackedConfig(ctx, wtPath)
	return ws, nil
}

// SpawnClient launches the bare self-invoked plugin subprocess via the Host
// runtime — identical to None. The worktree is expressed purely via the caller's
// RunOptions.WorkDir, so no per-workspace launch machinery is needed here.
func (Worktree) SpawnClient(backendName, label string, verbosity int, ws Workspace, spawnEnv map[string]string) (pb.Client, error) {
	// Identical spawn to None (the workspace rides RunOptions.WorkDir, not
	// the spawn) — call the one unit rather than duplicate it.
	return None{}.SpawnClient(backendName, label, verbosity, ws, spawnEnv)
}

// provisionConfigHome creates the per-agent config-home root (P2, T0.6) and, when
// this policy carries a backend with a registered credentialSeedSpec, seeds it
// with the backend's host subscription credentials (grave-prize). Returns "" on
// the MkdirAll failure — the run still proceeds against the shared global config
// (warn), never blocking. The scoped envs are preferred over a per-session HOME,
// which would strip ~/.gitconfig/ssh identity the worktree still needs.
func (w Worktree) provisionConfigHome(agentID string) string {
	home := worktreeScratchPath(w.scratchBase(), "ctxloom-cfg", agentID)
	// 0700 like every MkdirTemp sibling in this package: the dir holds engine
	// creds/state (CLAUDE_CONFIG_DIR & co.) in the SHARED OS temp dir — never
	// world-traversable.
	if err := os.MkdirAll(home, 0o700); err != nil {
		clidiag.Warn("ctxloom", "worktree: per-agent config-home unavailable (using shared global config): %v", err)
		// Defensive against a mutant flipping this check: home is a
		// deterministic path (not MkdirTemp-random), so a real success
		// misclassified as failure would otherwise leave a fully-created,
		// unreferenced dir on disk — the caller stores "" and can never find
		// it again. A genuine MkdirAll failure makes this a harmless no-op.
		_ = os.RemoveAll(home)
		return ""
	}
	w.seedCredentials(home, agentID)
	return home
}

// seedCredentials seeds w.backend's subscription credentials into the per-agent
// config-home when the backend has a registered credentialSeedSpec (auth.go) —
// an engine whose isolation env var relocates CREDENTIALS, not just config. No
// spec (w.backend == "", or a backend deliberately left out of the registry —
// see credentialSeedSpecs' doc) is a silent no-op: the pre-fix, config-only
// provisioning.
//
// The copy mechanics (I/O failure) stay best-effort like the rest of this
// provisioning step — a warn, not a block. But "nothing seedable" is NOT
// best-effort: no envTrigger (e.g. ANTHROPIC_API_KEY) set AND no host
// credential file present is exactly the silent-logged-out-agent failure mode
// fail-loudly exists to catch (the original grave-prize bug), so it records a
// ClassIsolation finding the choke owner aborts on in strict mode (default)
// unless --degraded — matching how the container path treats unresolvable auth
// (resolveClaudeContainerAuth / container.go).
func (w Worktree) seedCredentials(configHome, agentID string) {
	spec, ok := credentialSeedSpecs[w.backend]
	if !ok {
		return
	}
	result, err := hostCredentialSeed(spec, configHome)
	if err != nil {
		clidiag.Warn("ctxloom", "worktree: could not seed %s credentials for %q (using an unseeded config-home): %v", spec.engine, agentID, err)
		return
	}
	if result == seedNoSource {
		strictness.Fail(strictness.ClassIsolation, credentialSeedFixIt,
			"worktree isolation for agent %q: no %s and no host %s credentials found to seed the per-agent config-home — the agent would start logged out",
			agentID, spec.envTrigger, spec.engine)
	}
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
