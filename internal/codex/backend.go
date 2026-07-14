package codex

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

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

// codexAuthFile is the credential file codex resolves from $CODEX_HOME.
const codexAuthFile = "auth.json"

// Codex implements the Backend interface for OpenAI Codex CLI. The shared launch
// core (capability wiring, accessors, Setup/Cleanup) lives in the embedded
// agent.LaunchBackend; Codex adds only the Codex-specific Configure/Execute.
type Codex struct {
	agent.LaunchBackend
}

// NewCodex creates a new Codex backend with default settings.
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
	b.InitLaunch(
		agent.NewBaseLifecycle("codex"),
		&CodexSkills{},
		agent.NewBaseContextProvider(),
		NewCodexSessionHistory(b),
		&agent.CellDelivery{Build: agent.BuildWellKnown(NewSurfaces), RawContext: true, ContextHook: true},
	)
	b.SetExecuteEnv(cellCodexHomeEnv)
	return b
}

// cellCodexHomeEnv is codex's per-backend child-env contributor. Setup delivers
// codex's config (.codex/config.toml) and cell-scoped prompts (.codex/prompts)
// under <WorkDir>/.codex in EVERY cell (they ride the delivery dir), so point
// CODEX_HOME there — the one env that makes codex discover them (its project
// config is cwd-relative, but its prompts/sessions hang off CODEX_HOME). This
// applies to a SharedCell too: without it, codex would read prompts from the
// user's global ~/.codex and miss the cell-scoped skills. Skipped for a minimal/
// distill run (SkipSetup), which delivers no surfaces and should keep codex's
// global home.
//
// A cell-scoped CODEX_HOME also moves codex's CREDENTIAL lookup — auth.json is
// resolved from $CODEX_HOME, never from $HOME — so every run through this env
// must seed the cell home with the user's credentials (linkUserCodexAuth) or it
// authenticates as nobody and 401s.
//
// OPEN QUESTION (plan risk): a ProcessIsolatedCell (container) already has a fresh
// $HOME, so a <WorkDir>/.codex CODEX_HOME may be redundant or point at a
// non-existent in-namespace path. It is set here consistently pending a live
// container smoke test; revisit if codex resolves its home differently under the
// container mount model.
func cellCodexHomeEnv(req *agent.ExecuteRequest) map[string]string {
	if req.SkipSetup {
		return nil
	}
	work := req.WorkDir
	if work == "" {
		work = "."
	}
	return map[string]string{"CODEX_HOME": cellScopedCodexHome(work)}
}

// linkUserCodexAuth seeds a cell-scoped $CODEX_HOME with the user's codex
// credentials. codex resolves auth.json from $CODEX_HOME ONLY — never from $HOME
// (its own help: "--ignore-user-config: Do not load $CODEX_HOME/config.toml; auth
// still uses CODEX_HOME") — so redirecting CODEX_HOME without seeding auth makes
// every host run 401 against api.openai.com.
//
// The seed is a SYMLINK to the user's real auth.json: no credential is ever copied
// into the workspace (where it would sit in a tracked dir), and a token refresh
// written through it lands in the user's own file instead of forking a second,
// diverging credential.
//
// No-ops when the user has no auth.json (env-var auth, e.g. CODEX_API_KEY), when
// the cell home already IS the user's home, or when the cell holds a REAL auth.json
// of its own — a hand-placed cell credential is not ours to replace.
func linkUserCodexAuth(cellHome string) error {
	userHome, err := codexHome()
	if err != nil || userHome == "" || filepath.Clean(userHome) == filepath.Clean(cellHome) {
		return nil
	}
	src := filepath.Join(userHome, codexAuthFile)
	if _, err := os.Stat(src); err != nil {
		return nil
	}

	dst := filepath.Join(cellHome, codexAuthFile)
	switch info, err := os.Lstat(dst); {
	case err == nil && info.Mode()&os.ModeSymlink == 0:
		return nil
	case err == nil:
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("replace stale codex auth link: %w", err)
		}
	}
	if err := os.MkdirAll(cellHome, 0o755); err != nil {
		return fmt.Errorf("create cell codex home: %w", err)
	}
	if err := os.Symlink(src, dst); err != nil {
		return fmt.Errorf("link codex credentials into %s: %w", cellHome, err)
	}
	return nil
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

	// A cell-scoped CODEX_HOME (cellCodexHomeEnv) relocates codex's credential
	// lookup along with its config, so seed it before launch. Skipped for the
	// container cell, which resolves its own home inside the namespace: a host-path
	// symlink would only dangle there.
	if !req.DryRun && !req.SkipSetup && req.WorkDir != "" && req.CellKind != agent.CellKindProcessIsolated {
		if err := linkUserCodexAuth(cellScopedCodexHome(req.WorkDir)); err != nil {
			agent.Warn("codex credentials could not be linked into the run's CODEX_HOME "+
				"(%v) — codex will start unauthenticated", err)
		}
	}

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
