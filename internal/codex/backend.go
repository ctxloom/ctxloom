package codex

import (
	"context"
	"io"

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

// defaultCodexModel is the fallback model id reported when none is requested.
const defaultCodexModel = "o3-mini"

// Codex implements the Backend interface for OpenAI Codex CLI. The shared launch
// core (capability wiring, accessors, Setup/Cleanup) lives in the embedded
// agent.LaunchBackend; Codex adds only the Codex-specific Configure/Execute.
type Codex struct {
	agent.LaunchBackend
	writeSettings agent.WriteSettingsFunc
}

// NewCodex creates a new Codex backend with default settings. The writeSettings
// dispatch is injected (the registry supplies it).
func NewCodex(writeSettings agent.WriteSettingsFunc) *Codex {
	b := &Codex{writeSettings: writeSettings}
	b.BaseBackend = agent.NewBaseBackend("codex", "1.0.0")
	b.BinaryPath = "codex"
	// codex routes delivery through the cell seam. RawContext: its context is the
	// content-addressed cache file a SessionStart hook reads at run time, so Setup
	// materializes that file (+ CTXLOOM_CONTEXT_FILE) as a pre-step. ContextHook:
	// codex is the one engine that fires the SessionStart inject-context hook, so
	// the hook is keyed to the cache file's hash. The config surface writes the
	// [hooks] (incl. that hook) + [mcp_servers] tables of .codex/config.toml.
	b.InitLaunch(
		agent.NewBaseLifecycle("codex", b.writeSettings),
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

// Configure applies a decoded codex config to this backend.
func (b *Codex) Configure(cfg agent.BackendConfig) {
	if c, ok := cfg.(*CodexConfig); ok {
		agent.ApplyLocalCLIConfig(&b.BaseBackend, c.BinaryPath, c.Args, c.Env)
	}
}

// Execute runs the backend with the given request.
func (b *Codex) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	modelName := req.Model
	if modelName == "" {
		modelName = defaultCodexModel
	}
	modelInfo := &agent.ModelInfo{ModelName: modelName, Provider: "openai"}

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

// buildArgs constructs the command-line arguments for the codex CLI. Oneshot
// runs use the non-interactive `exec` subcommand; interactive runs launch the
// TUI directly. Isolation follows the request: SkipSetup (distillation) runs
// read-only with no approvals so it can't invoke tools or mutate the workspace;
// a bypass posture uses --full-auto and plan the same read-only sandbox;
// otherwise codex's configured defaults (interactive approval) apply.
func (b *Codex) buildArgs(req *agent.ExecuteRequest) []string {
	var args []string

	// Non-interactive runs go through `codex exec PROMPT`.
	if req.Mode == agent.ModeOneshot {
		args = append(args, "exec")
	}
	args = append(args, b.Args...)

	// Select the requested model (codex supports --model/-m). Empty lets codex use
	// its configured default rather than forcing one.
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	switch {
	case req.SkipSetup:
		// Minimal/distill: read-only sandbox, never prompt for approval.
		args = append(args, "--sandbox", "read-only", "--ask-for-approval", "never")
	case req.Permissions == agent.PermissionBypass:
		args = append(args, "--full-auto")
	case req.Permissions == agent.PermissionPlan:
		// Read-only planning maps to the same locked-down sandbox as minimal mode.
		args = append(args, "--sandbox", "read-only", "--ask-for-approval", "never")
		// default/acceptEdits: codex has no edit-only tier, so its configured
		// defaults (interactive approval) apply.
	}

	if prompt := agent.GetPromptContent(req.Prompt); prompt != "" {
		args = append(args, prompt)
	}

	return args
}
