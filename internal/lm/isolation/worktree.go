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

// backendsWithNoGlobalState are registered backends that provably have NO
// engine-global config/credential state of their own to isolate, so the
// "unregistered backend" finding below must not fire for them. Only
// entry today: "mock", ctxloom's own built-in test double — a bare echo that
// never spawns a grandchild process and never touches disk (see
// tests/acceptance/features/j002200_isolation.feature's own hermeticity note).
// This is a NAMED, independently-verified exemption, never a convenience
// default — a real, user-selectable backend (e.g. "acp") that falls through
// both registries is exactly the bug this guards against, not a candidate for
// this list.
//
// Keyed by CANONICAL name (enginekeys.go asserts it); read only through
// backendHasNoGlobalState, which resolves aliases.
var backendsWithNoGlobalState = map[string]bool{"mock": true}

// worktreeBaseRef is the ref each per-agent worktree is checked out to: HEAD,
// DETACHED (T0.2). Ephemeral per-agent checkouts are never branch-per-agent in
// this phase — detached avoids "branch already checked out elsewhere" collisions
// between concurrent members and leaves no stray branches to clean up.
const worktreeBaseRef = "HEAD"

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
	git git.Git
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
	// logged out. Empty is valid (a bare Worktree{}, or a
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
	return Worktree{git: g, backend: backend}
}

// Name identifies the policy.
func (Worktree) Name() string { return "worktree" }

// ResolveWorkspace creates a fresh detached worktree for the member. It errors
// (→ caller degrades to None) when projectDir is not a git repo or the worktree
// add fails. On success it also, best-effort, provisions the member's HOST
// isolation lever and writes the broadened ctxloom-config excludes to the
// shared common-dir .git/info/exclude so a developer member's merge-back never
// carries per-agent config (§3.1). Neither best-effort step fails the
// workspace.
//
// The lever, when the backend is registered in credentialSeedSpecs (auth.go),
// is a SCOPED env var (CLAUDE_CONFIG_DIR/CODEX_HOME/KIRO_HOME) pointed at a
// per-agent subdir; the rest of the process env, including HOME, is
// untouched. A backend not in that registry gets the pre-fix,
// config-only-isolation no-op (no per-agent env at all).
//
// The deferred recover below exists because the worktree checkout and
// whichever scratch home was provisioned are real on-disk resources created
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

	wtPath := worktreeScratchPath(w.scratchBase(), worktreeScratchPrefix, agentID)
	if err := w.git.WorktreeAdd(ctx, projectDir, wtPath, worktreeBaseRef); err != nil {
		return nil, fmt.Errorf("worktree add: %w", err)
	}
	// Stamp the owner pid IMMEDIATELY after the checkout exists — the fix
	// for a bug (ReapOrphanedWorktrees, worktree_reap.go): a crashed/killed
	// run's worktree is never reaped by anything else (teardown only ever runs
	// on a graceful Cleanup), so a later startup sweep needs a way to prove
	// THIS worktree's owner is gone before it dares remove it. Best-effort: a
	// failed write only means a future sweep must conservatively skip this one
	// (never force), not that ResolveWorkspace itself fails.
	recordWorktreeOwner(wtPath)

	ws := &worktreeWorkspace{
		git:     w.git,
		repoDir: projectDir,
		dir:     wtPath,
		backend: w.backend,
		agentID: agentID,
	}
	defer func() {
		if r := recover(); r != nil {
			_ = os.RemoveAll(ws.dir)
			removeWorktreeOwnerMarker(ws.dir)
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
			if ws.configHome != "" {
				_ = os.RemoveAll(ws.configHome)
			}
			if ws.scratchDir != "" {
				_ = os.RemoveAll(ws.scratchDir)
			}
			panic(r)
		}
	}()
	if _, seeded := credentialSeedSpecFor(w.backend); !seeded && w.backend != "" && !backendHasNoGlobalState(w.backend) {
		// A backend registered in NEITHER credentialSeedSpecs (e.g. "acp")
		// used to fall through here with zero engine-global isolation and NO
		// finding at all — the run reports "worktree" isolation while the
		// engine's global config/creds stay fully shared with the host.
		// w.backend == "" stays silent: that is the documented no-context
		// construction, not an unregistered backend. backendsWithNoGlobalState
		// is the one, independently-verified exemption (the built-in mock
		// backend), not a silent carve-out.
		strictness.Fail(strictness.ClassIsolation, credentialSeedFixIt,
			"worktree isolation for agent %q: backend %q is not registered in the credential-seed registry — only the working directory is isolated; the engine's global config and credentials remain fully shared with the host",
			agentID, w.backend)
	}
	ws.configHome, ws.deniedHomeVars = w.provisionConfigHome(agentID, wtPath)
	ws.scratchDir = w.provisionScratchDir(agentID)
	w.excludeConfigFromMerge(ctx, projectDir)
	w.skipTrackedConfig(ctx, wtPath)
	return ws, nil
}

// Mount maps nothing. Like None, the worktree policy runs the engine on the
// HOST, directly inside the checkout ResolveWorkspace created — there is no
// second environment to map the tree into. The per-agent config-home and
// toolchain-scratch env the worktree does provide rides worktreeWorkspace.Env
// (the EnvWorkspace seam the spawn already consults), not a mount plan.
func (Worktree) Mount(context.Context, Workspace) (MountPlan, error) { return MountPlan{}, nil }

// PrepareWorkspace resolves and maps in one step (see prepareWorkspace).
func (w Worktree) PrepareWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error) {
	return prepareWorkspace(ctx, w, projectDir, agentID)
}

// provisionScratchDir creates the per-agent TOOLCHAIN scratch root
// (worktreeWorkspace.Env()'s TMPDIR/GOTMPDIR) under the same scratchBase as
// the checkout and config-home — the session's ephemeral/ dir when a harp is
// known, else the OS temp dir. This is the fix for the shared-/tmp toolchain
// contention that corrupted concurrent agents (spawner-env audit): every
// worktree member previously inherited the SAME process TMPDIR, so `go
// build`'s per-invocation $WORK scratch, `git`'s temp blobs, and any other
// tool honouring TMPDIR collided across members. Returns "" on the MkdirAll
// failure — best-effort like provisionConfigHome, never blocking the run;
// Env() then simply omits TMPDIR/GOTMPDIR and the child falls back to the
// shared process default.
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

// provisionConfigHome creates the per-agent config-home root (P2, T0.6) and, when
// this policy carries a backend with a registered credentialSeedSpec, seeds it
// with the backend's host subscription credentials. Returns "" on
// the MkdirAll failure — the run still proceeds against the shared global config
// (warn), never blocking. The scoped envs are preferred over a per-session HOME,
// which would strip ~/.gitconfig/ssh identity the worktree still needs. denied
// carries any GatedOnCreds HomeVars seedCredentials decided NOT to isolate (see
// its doc) — Env() reads it back to omit them.
//
// workDir is THIS member's own checkout — the directory the engine will
// actually run in, and therefore the one an engine-generated workspace-trust
// answer must name. It is the per-agent worktree, never the shared project
// root: trusting the project root would answer for a directory this member
// never enters.
func (w Worktree) provisionConfigHome(agentID, workDir string) (home string, denied map[string]bool) {
	home = worktreeScratchPath(w.scratchBase(), "ctxloom-cfg", agentID)
	// 0700 like every MkdirTemp sibling in this package: the dir holds engine
	// creds/state (CLAUDE_CONFIG_DIR & co.) in the SHARED OS temp dir — never
	// world-traversable.
	if err := os.MkdirAll(home, 0o700); err != nil {
		// This used to be a plain clidiag.Warn, while a LESSER
		// (partial: creds present but unseedable) failure two frames later in
		// seedCredentials is a strictness.Fail(ClassIsolation) — inverting
		// the severity ordering, so a TOTAL loss of engine-global isolation
		// was quieter than a partial one, and a strict run would proceed
		// unsandboxed-in-config where it would have aborted on the lesser
		// fault. Escalate to match: fatal unless --degraded, like every
		// sibling isolation gate.
		strictness.Fail(strictness.ClassIsolation, credentialSeedFixIt,
			"worktree isolation for agent %q: per-agent config-home unavailable (%v) — falling back to the shared global config, with no engine-global isolation at all",
			agentID, err)
		// Defensive against a mutant flipping this check: home is a
		// deterministic path (not MkdirTemp-random), so a real success
		// misclassified as failure would otherwise leave a fully-created,
		// unreferenced dir on disk — the caller stores "" and can never find
		// it again. A genuine MkdirAll failure makes this a harmless no-op.
		_ = os.RemoveAll(home)
		return "", nil
	}
	denied = w.seedCredentials(home, agentID, workDir)
	w.prepareHomeVarDirs(home, denied)
	return home, denied
}

// prepareHomeVarDirs creates the per-agent directories Env() points this
// backend's HomeVars at. Seeding alone does not: hostCredentialSeed creates
// spec.destSubdir and nothing else, and only on the path where there was
// something to seed — so an engine that authenticates from its envTrigger, or
// one with no seedable files at all (kiro), is otherwise handed a scoped var
// naming a directory nobody created. 0700 like every sibling scratch dir here:
// these hold engine config/state, and leaving the engine to mkdir them itself
// yields whatever its umask says instead. denied vars are skipped — Env() does
// not export them, so their directory would be pure litter. Best-effort, like
// the rest of this provisioning step: a failure warns and leaves the var
// pointing at an absent directory, exactly as before.
func (w Worktree) prepareHomeVarDirs(configHome string, denied map[string]bool) {
	spec, ok := credentialSeedSpecFor(w.backend)
	if !ok {
		return
	}
	for _, hv := range spec.HomeVars {
		if denied[hv.EnvVar] {
			continue
		}
		dir := filepath.Join(configHome, hv.Subdir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			clidiag.Warn("ctxloom", "worktree: cannot create the per-agent %s directory %q (the engine will see a missing config home): %v", hv.EnvVar, dir, err)
		}
	}
}

// seedCredentials seeds w.backend's subscription credentials into the per-agent
// config-home when the backend has a registered credentialSeedSpec (auth.go)
// with copyable sourceFiles (claude/codex — HonoursVarForCreds true: an engine
// whose isolation env var relocates CREDENTIALS, not just config), or — for a
// HonoursVarForCreds==false spec whose creds live in an unrelocatable global
// store (kiro) — gates each GatedOnCreds HomeVar on its bypass env instead (see
// gateHomeVars). No spec (w.backend == "", or a backend genuinely left out of
// the registry, with no host isolation lever at all) is a silent no-op: the
// pre-fix, config-only provisioning.
//
// The copy mechanics (I/O failure) stay best-effort like the rest of this
// provisioning step — a warn, not a block. But "nothing seedable" is NOT
// best-effort: no envTrigger (e.g. ANTHROPIC_API_KEY) set AND no host
// credential file present is exactly the silent-logged-out-agent failure mode
// fail-loudly exists to catch, so it records a
// ClassIsolation finding the choke owner aborts on in strict mode (default)
// unless --degraded — matching how the container path treats unresolvable auth
// (resolveClaudeContainerAuth / container.go).
// It routes through isolation.CopyAmbient — the ONE one-way ambient copy-in,
// shared with the in-tree axis (D8, ruled). Sharing the mechanism is
// what makes the D4/D5 rulings — claude's field-scoped .claude.json, codex's
// [mcp_servers]/[hooks] elision — apply to a fan-out member for free, instead
// of being an in-tree-only privilege the worktree axis silently missed. The
// LOCATION stays split: this axis's homes remain home-rooted under
// ~/.ctxloom/sessions/<harp>/ephemeral/, because they are per-AGENT, not
// per-session.
func (w Worktree) seedCredentials(configHome, agentID, workDir string) map[string]bool {
	spec, ok := credentialSeedSpecFor(w.backend)
	if !ok {
		return nil
	}
	if spec.sourceFiles == nil {
		return w.gateHomeVars(spec, agentID)
	}
	report, err := CopyAmbient(AmbientRequest{Engine: w.backend, InstanceHome: configHome, WorkDir: workDir})
	if err != nil {
		clidiag.Warn("ctxloom", "worktree: could not seed %s credentials for %q (using an unseeded config-home): %v", spec.engine, agentID, err)
		return nil
	}
	if report.NoSource {
		strictness.Fail(strictness.ClassIsolation, credentialSeedFixIt,
			"worktree isolation for agent %q: no %s and no host %s credentials found to seed the per-agent config-home — the agent would start logged out",
			agentID, spec.envTrigger, spec.engine)
	}
	return nil
}

// gateHomeVars decides, for a HonoursVarForCreds==false spec (kiro — its
// credentials live in a global sqlite no per-agent HomeVar relocates), whether
// each GatedOnCreds HomeVar is safe to isolate: safe when spec.envTrigger is
// present in the process env (the agent authenticates under a fresh var via
// that key — live-verified for kiro: KIRO_API_KEY + a fresh XDG_DATA_HOME
// authenticates headlessly, no browser). When it is absent, isolating that var
// would silently strand the agent logged out of a credential store it can never
// reach again, so this records a
// ClassIsolation fail-loud finding (degradable via --degraded) and returns the
// var DENIED — Env() omits it, so the agent falls back to the engine's shared
// global store instead. Non-gated HomeVars on the same spec (kiro's KIRO_HOME —
// kiro's whole home, but no creds) are never denied.
func (w Worktree) gateHomeVars(spec credentialSeedSpec, agentID string) map[string]bool {
	granted := spec.envTrigger != "" && os.Getenv(spec.envTrigger) != ""
	var denied map[string]bool
	for _, hv := range spec.HomeVars {
		if !hv.GatedOnCreds || granted {
			continue
		}
		if denied == nil {
			denied = map[string]bool{}
		}
		denied[hv.EnvVar] = true
		strictness.Fail(strictness.ClassIsolation, credentialSeedFixIt,
			"worktree isolation for agent %q: isolating %s would relocate %s's credential store away from where it's authenticated, with no %s set to authenticate a fresh one — sharing the host's global store instead",
			agentID, hv.EnvVar, spec.engine, spec.envTrigger)
	}
	return denied
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
// worktree checkout, Env() the per-agent host-lever envs (EnvWorkspace), and
// Cleanup() the WIP-safe, nested-worktree-aware teardown.
type worktreeWorkspace struct {
	git        git.Git
	repoDir    string
	dir        string
	configHome string
	// backend is the registered backend name this workspace's scratch home
	// was provisioned for (Worktree.backend, copied at PrepareWorkspace
	// time) — Env() looks up credentialSeedSpecs[backend] to build its
	// HomeVars set.
	backend string
	// deniedHomeVars names GatedOnCreds env vars seedCredentials decided NOT
	// to isolate (gateHomeVars — kiro's XDG_DATA_HOME with no KIRO_API_KEY);
	// Env() omits them.
	deniedHomeVars map[string]bool
	// agentID is the member label PrepareWorkspace was given (Worktree.
	// PrepareWorkspace's agentID param, copied at construction) — Env() uses
	// it to build the per-agent git identity (gitIdentity) and
	// provisionScratchDir uses it to name the scratch dir.
	agentID string
	// scratchDir is the per-agent TOOLCHAIN scratch root (provisionScratchDir)
	// that Env() points TMPDIR/GOTMPDIR at — scoping build/VCS temp-file
	// traffic per agent the same way configHome scopes engine config. "" when
	// provisioning failed (best-effort; Env() then omits both vars).
	scratchDir string
}

// Ensure the workspace exposes its per-agent host-lever envs.
var _ EnvWorkspace = (*worktreeWorkspace)(nil)

// Dir returns the worktree checkout the member's engine runs in.
func (w *worktreeWorkspace) Dir() string { return w.dir }

// Env returns the per-agent host-lever envs. configHome set (claude/codex/
// kiro): the SCOPED config-home envs that isolate each engine's GLOBAL
// config/state/creds home (T0.6, widened per per-engine-isolation-home plan
// §6), driven entirely by credentialSeedSpecs[w.backend].HomeVars —
// claude/codex each get their one var, kiro gets two (KIRO_HOME always,
// XDG_DATA_HOME unless gateHomeVars denied it). HOME itself is left
// untouched, deliberately: a blanket HOME override would strip the
// ~/.gitconfig/~/.ssh identity the worktree still needs for git itself,
// which a scoped var avoids by construction.
//
// Empty when NEITHER a scratch dir, a git identity, nor any configHome var
// could be provisioned/resolved.
//
// TWO additions ride here UNCONDITIONALLY, ahead of and independent from the
// configHome engine-config lever above — every worktree member gets them
// regardless of backend registry membership, because they scope
// TOOLCHAIN/VCS state shared across linked worktrees, not engine config:
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
	if w.configHome != "" {
		if spec, ok := credentialSeedSpecFor(w.backend); ok {
			for _, hv := range spec.HomeVars {
				if w.deniedHomeVars[hv.EnvVar] {
					continue
				}
				env[hv.EnvVar] = filepath.Join(w.configHome, hv.Subdir)
			}
		}
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
// provisioned configHome and, independently, the toolchain scratchDir (both
// may be set together). Idempotent (guarded by clearing dir). It NEVER
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
	// Unconditional: whether teardown actually removed target or (WIP-safely)
	// left it in place, this process's ownership of it is ending either way —
	// see removeWorktreeOwnerMarker's doc for why a left-in-place tree stays
	// correctly protected regardless.
	removeWorktreeOwnerMarker(target)

	if w.configHome != "" {
		home := w.configHome
		w.configHome = ""
		if err := os.RemoveAll(home); err != nil {
			warnCleanupResidue("per-agent config-home", home, err)
		}
	}
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
	// acceptance contract — tests/acceptance/features/j002200_isolation.feature's
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
