package codex

import (
	"context"
	"io"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// backend.go wires codex onto the shared launch core and the surfaces × cells
// delivery seam.

// CodexConfig is codex's typed LLM config. The backend owns this struct; the
// config package only carries the raw body that decodes into it.
type CodexConfig struct {
	Model      string            `mapstructure:"model"`
	BinaryPath string            `mapstructure:"binary_path"`
	Args       []string          `mapstructure:"args"`
	Env        map[string]string `mapstructure:"env"`
}

// BackendType identifies the backend this config drives.
func (CodexConfig) BackendType() string { return "codex" }

// Codex implements the Backend interface for OpenAI Codex CLI. The shared launch
// core (capability wiring, accessors, Setup/Cleanup) lives in the embedded
// agent.LaunchBackend; Codex adds only the Codex-specific Configure/Execute.
type Codex struct {
	agent.LaunchBackend
	// resolvedProjectDir is the "virtual project dir" cellScopedCodexHome joins
	// ".codex" onto for THIS run — computed once per Setup (white-dawn §2.2A)
	// and read by both buildSurfaces (Setup's delivery target) and
	// cellCodexHomeEnv (Execute's env), so the two can never disagree about
	// where CODEX_HOME points. See resolveCodexProjectDir.
	resolvedProjectDir string
	// resolvedTrustAbsPath is the absolute WorkDir to pre-seed
	// `[projects."<path>"] trust_level = "trusted"` for, set ONLY when
	// resolveCodexProjectDir found an isolation-provided CODEX_HOME (an
	// ephemeral, never-committed home safe to auto-trust) — "" for the
	// in-tree/None path, which never pre-seeds trust.
	resolvedTrustAbsPath string
}

// NewCodex creates a new Codex backend with default settings. Its InitLaunch
// call necessarily diverges from NewAntigravity/NewClaudeCode/NewKiro's: codex
// alone needs Setup-time state (resolvedProjectDir/resolvedTrustAbsPath, see
// buildSurfaces) threaded into its CellDelivery.Build, so it supplies a bound
// method instead of the shared agent.BuildWellKnown every other well-known-
// file backend uses — a deliberate, reviewed divergence (white-dawn §2.2A),
// not a missed sibling update.
// reprise:accept-drift
func NewCodex() *Codex {
	b := &Codex{}
	b.BaseBackend = agent.NewBaseBackend("codex", "1.0.0")
	b.BinaryPath = "codex"
	// codex routes delivery through the cell seam. RawContext: its context is the
	// content-addressed cache file a SessionStart hook reads at run time, so Setup
	// materializes that file (+ CTXLOOM_CONTEXT_FILE) as a pre-step. ContextHook:
	// codex is the one engine that fires the SessionStart inject-context hook, so
	// the hook is keyed to the cache file's hash. The config surface writes the
	// [hooks] (incl. that hook) + [mcp_servers] tables of .codex/config.toml.
	// Build is b.buildSurfaces (a method, not the shared BuildWellKnown every
	// other well-known-file backend uses) because codex — uniquely among them —
	// needs Setup-time state (resolvedProjectDir/resolvedTrustAbsPath) threaded
	// into its surfaces; see buildSurfaces' doc.
	b.InitLaunch(
		agent.NewBaseLifecycle("codex"),
		&CodexCommands{},
		agent.NewBaseContextProvider(),
		NewCodexSessionHistory(b),
		&agent.CellDelivery{Build: b.buildSurfaces, RawContext: true, ContextHook: true},
	)
	b.SetExecuteEnv(b.cellCodexHomeEnv)
	return b
}

// resolveCodexProjectDir resolves the "virtual project dir" this run's codex
// surfaces target — cellScopedCodexHome(dir) is the FINAL CODEX_HOME
// (config.toml/prompts/skills/state/auth.json all hang off it). This is the
// single-owner fix for the env-precedence bug the per-engine-isolation-home
// plan found live: launch_backend.go's ExecuteEnv applies req.Env FIRST, then
// this backend's SetExecuteEnv contributor LAST, so cellCodexHomeEnv used to
// unconditionally override an isolation-provided CODEX_HOME
// (internal/lm/isolation/worktree.go's Env(), gated through
// credentialSeedSpecs["codex"]) with the in-tree <WorkDir>/.codex — the
// worktree config-home CODEX_HOME was DEAD.
//
// When the isolation layer already set CODEX_HOME, it is now the single
// owner: isolation/auth.go's codex HomeVar uses Subdir ".codex", so the value
// already ends in "/.codex" — this strips that suffix back to the virtual
// project dir cellScopedCodexHome expects, so every existing cell-scoped
// writer (cellScopedCodexHome/cellScopedPromptsDir/cellScopedSkillsDir,
// settings.go's SettingsPath) keeps resolving the join itself, unchanged; it
// simply joins against the isolation-provided config-home instead of the real
// WorkDir. isolated=true then, so the caller (Setup) knows this run's config
// home is ephemeral and safe to pre-seed trust into (see
// WriteSettingsWithTrust's doc) — an out-of-tree home that spills no engine
// state into the project tree and is removed at Worktree.Cleanup.
//
// No isolation-provided value (None/shared-cwd, or no backend context) falls
// back to today's default: WorkDir itself, in-tree. This is a KNOWN,
// deliberately-scoped residual — see the codex state spill note in this
// package's materialize/apply doc and the task's final report — not a
// silent regression: it is EXACTLY today's behavior, already dbea746-
// gitignored, and None was never an isolation boundary to begin with (no
// concurrent-agent claim is made for it).
func resolveCodexProjectDir(env map[string]string, workDir string) (dir string, isolated bool) {
	if home := env["CODEX_HOME"]; home != "" {
		if stripped := strings.TrimSuffix(home, string(filepath.Separator)+".codex"); stripped != home {
			return stripped, true
		}
		// An isolation-provided CODEX_HOME not in the expected "/.codex" shape
		// (a caller override, or a future spec whose Subdir changes) — use it
		// AS the project dir directly. cellScopedCodexHome will nest an extra
		// ".codex" under it, which is at least self-consistent (Setup and
		// Execute still agree) even if it doesn't match what the isolation
		// layer intended.
		return home, true
	}
	if workDir == "" {
		return ".", false
	}
	return workDir, false
}

// buildSurfaces is codex's CellDelivery.Build: unlike every other well-known-
// file backend (which use the shared agent.BuildWellKnown, ignoring
// isolatedDir entirely), codex needs its OWN Setup-time resolution
// (resolvedProjectDir/resolvedTrustAbsPath, computed by Setup below) threaded
// into NewSurfaces so its config/commands/skills surfaces target the SAME
// isolation-provided CODEX_HOME cellCodexHomeEnv points the launched child
// at — the single-owner fix (white-dawn §2.2A). isolatedDir (the generic
// per-backend placement channel) is intentionally unused here: codex's
// resolution is backend-specific (env-derived, not cell-kind-derived).
func (b *Codex) buildSurfaces(in agent.SurfaceInputs, _ string) agent.SurfaceSet {
	return NewSurfaces(in, b.resolvedProjectDir, b.resolvedTrustAbsPath, nil)
}

// Setup resolves this run's CODEX_HOME ownership ONCE (resolveCodexProjectDir)
// before delegating to the shared LaunchBackend.Setup, which calls
// buildSurfaces — storing the result on b so cellCodexHomeEnv (Execute, later
// in the same request lifecycle: grpc/server.go's Run calls Setup then
// Execute on the same backend instance) reads the IDENTICAL value rather than
// re-deriving it from a possibly-different view of req. This is what makes
// Setup's delivery and Execute's env agree by construction, not convention.
func (b *Codex) Setup(ctx context.Context, req *agent.SetupRequest) error {
	dir, isolated := resolveCodexProjectDir(req.Env, req.WorkDir)
	b.resolvedProjectDir = dir
	b.resolvedTrustAbsPath = ""
	if isolated {
		if abs, err := filepath.Abs(req.WorkDir); err == nil {
			b.resolvedTrustAbsPath = abs
		} else {
			b.resolvedTrustAbsPath = req.WorkDir
		}
	}
	return b.LaunchBackend.Setup(ctx, req)
}

// cellCodexHomeEnv is codex's per-backend child-env contributor. It reads the
// SAME resolution Setup already computed (b.resolvedProjectDir) so Execute's
// CODEX_HOME can never disagree with where Setup delivered config.toml/
// prompts/skills — the single-owner fix (see resolveCodexProjectDir). Falls
// back to re-deriving fresh from req only for a caller that reaches Execute
// without Setup having run first (defensive; the normal request lifecycle
// always runs Setup first — see Setup's doc — except SkipSetup, handled
// below). Skipped for a minimal/distill run (SkipSetup), which delivers no
// surfaces and should keep codex's global home.
//
// OPEN QUESTION (plan risk): a ProcessIsolatedCell (container) already has a
// fresh $HOME, so a relocated CODEX_HOME may be redundant or point at a
// non-existent in-namespace path. It is set here consistently pending a live
// container smoke test; revisit if codex resolves its home differently under
// the container mount model.
func (b *Codex) cellCodexHomeEnv(req *agent.ExecuteRequest) map[string]string {
	if req.SkipSetup {
		return nil
	}
	dir := b.resolvedProjectDir
	if dir == "" {
		dir, _ = resolveCodexProjectDir(req.Env, req.WorkDir)
	}
	return map[string]string{"CODEX_HOME": cellScopedCodexHome(dir)}
}

// Configure applies a decoded codex config to this backend.
func (b *Codex) Configure(cfg agent.BackendConfig) {
	if c, ok := cfg.(*CodexConfig); ok {
		agent.ApplyLocalCLIConfig(&b.BaseBackend, c.BinaryPath, c.Args, c.Env)
	}
}

// Execute runs the backend with the given request.
func (b *Codex) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// Report ONLY the model we actually asked for. An empty req.Model means codex
	// resolves its own configured default, which lives server-side (its model list
	// is account-scoped and fetched) — so ctxloom cannot name it, and inventing a
	// fallback id here would stamp a model that never ran onto every consumer of
	// ModelInfo (bundle distillation records it as source provenance).
	modelInfo := &agent.ModelInfo{ModelName: req.Model, Provider: "openai"}

	// Codex's credential seeding (balmy-comic) is now a COPY into the
	// isolation-provided CODEX_HOME, performed host-side by
	// internal/lm/isolation/worktree.go's provisionConfigHome BEFORE this run
	// even starts (credentialSeedSpecs["codex"], auth.go) — replacing the
	// former per-Execute SYMLINK (linkUserCodexAuth, deleted). Nothing to do
	// here: by the time Execute runs, auth.json is already in place (or
	// OPENAI_API_KEY rides the env instead).

	// Context reaches Codex through the SessionStart hook + context file (the
	// shared file+hook mechanism), so Execute only forwards the context-file
	// path in the env (ExecuteCLI) — it never prepends context to the prompt.
	//
	// CONCURRENCY LIMIT (weave/map fan-out): the SessionStart hook is registered
	// in a WORKSPACE-FIXED file (.codex/config.toml) with the per-run context hash
	// baked into its command, and Codex natively reads a WORKSPACE-FIXED AGENTS.md
	// — neither has a per-invocation redirect (Codex has no --mcp-config/--settings/
	// --append-system-prompt equivalent). So N codex agents in one cwd would each
	// rewrite config.toml — last writer wins → cross-agent context clobber. Unlike
	// claude (per-invocation flags) and kiro (per-agent agent-JSON `--agent`), codex
	// has NO redirection lever, so per-agent CONCURRENT isolation requires a
	// per-agent cwd (git worktree) or container. See taskloom loyal-eel / memory
	// per-agent-config-delivery (ISOLATION AXIS).
	return b.ExecuteCLI(ctx, req, b.buildArgs(req), nil, modelInfo, stdout, stderr)
}

// buildArgs constructs the command-line arguments for the codex CLI. Oneshot runs
// go through the non-interactive `codex exec` subcommand; interactive runs launch
// the TUI directly.
//
// THE TWO SUBCOMMANDS DO NOT SHARE A FLAG SET, and an unknown flag is a hard
// exit-2 "unexpected argument", never a warning — so a posture flag must be
// checked against the subcommand that will receive it (codex-cli 0.144.4, `codex
// --help` / `codex exec --help`):
//
//	--sandbox <read-only|workspace-write|danger-full-access>  BOTH
//	--dangerously-bypass-approvals-and-sandbox                BOTH
//	--ask-for-approval <policy>                               INTERACTIVE ONLY
//	                     (`codex exec` rejects it — a non-interactive run has
//	                      nobody to ask, so it carries no approval policy)
//	--full-auto                                               NEITHER (removed;
//	                     codex deprecated it in favour of --sandbox)
//
// Every posture NAMES its sandbox rather than inheriting codex's default, so a
// change to that default upstream cannot silently relax ctxloom's stated posture:
//
//	plan / SkipSetup (distill, compaction) → read-only        (+ never approve)
//	default / acceptEdits                  → workspace-write  (codex has no
//	                                         edit-only tier)
//	bypass                                 → no sandbox, no approvals
//
// bypass deliberately does NOT reuse --sandbox workspace-write (codex's suggested
// --full-auto replacement): that is exactly the default posture's flag, which
// would collapse two distinct postures into one. The honest mapping of "bypass all
// permission checks" is codex's own full-access escape hatch, the peer of claude's
// --dangerously-skip-permissions.
func (b *Codex) buildArgs(req *agent.ExecuteRequest) []string {
	var args []string

	interactive := req.Mode != agent.ModeOneshot
	if !interactive {
		args = append(args, "exec")
	}
	args = append(args, b.Args...)

	// Select the requested model (codex supports --model/-m). Empty lets codex use
	// its configured default rather than forcing one.
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	sandbox := "workspace-write"
	neverApprove := false
	switch {
	case req.SkipSetup, req.Permissions == agent.PermissionPlan:
		// Minimal/distill and plan mean the same thing to codex: read the workspace,
		// change nothing, never stop to ask.
		sandbox, neverApprove = "read-only", true
	case req.Permissions == agent.PermissionBypass:
		sandbox = ""
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	if sandbox != "" {
		args = append(args, "--sandbox", sandbox)
	}
	if neverApprove && interactive {
		args = append(args, "--ask-for-approval", "never")
	}

	if prompt := agent.GetPromptContent(req.Prompt); prompt != "" {
		args = append(args, prompt)
	}

	return args
}
