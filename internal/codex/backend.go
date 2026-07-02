package codex

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

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
	b.InitLaunch(
		agent.NewBaseLifecycle("codex", b.writeSettings),
		&CodexSkills{},
		agent.NewBaseContextProvider(),
		NewCodexSessionHistory(b),
	)
	return b
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

	if req.DryRun {
		return &agent.ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
	}

	args := b.buildArgs(req)
	if req.Verbosity >= 16 {
		_, _ = fmt.Fprintf(stderr, "[v16] %s %s\n", b.BinaryPath, strings.Join(args, " "))
	}

	// Context reaches Codex through the SessionStart hook + context file (the
	// shared file+hook mechanism), so Execute only forwards the context-file path
	// in the env — it no longer prepends context to the prompt.
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
	env := make(map[string]string)
	for k, v := range req.Env {
		env[k] = v
	}
	if b.ContextFilePath() != "" {
		env[agent.SCMContextFileEnv] = b.ContextFilePath()
	}

	var exitCode int32
	var err error
	if req.Mode == agent.ModeInteractive {
		exitCode, err = b.RunInteractive(ctx, args, env, req.Stdin, stdout, stderr, req.Resize)
	} else {
		exitCode, err = b.RunNonInteractive(ctx, args, env, stdout, stderr)
	}

	return &agent.ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
}

// buildArgs constructs the command-line arguments for the codex CLI. Oneshot
// runs use the non-interactive `exec` subcommand; interactive runs launch the
// TUI directly. Isolation follows the request: SkipSetup (distillation) runs
// read-only with no approvals so it can't invoke tools or mutate the workspace;
// AutoApprove uses --full-auto; otherwise codex's configured defaults apply.
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
	case req.AutoApprove:
		args = append(args, "--full-auto")
	}

	if prompt := agent.GetPromptContent(req.Prompt); prompt != "" {
		args = append(args, prompt)
	}

	return args
}
