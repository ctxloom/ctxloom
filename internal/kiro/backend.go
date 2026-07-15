// Package kiro implements the ctxloom Backend for Kiro CLI (`kiro-cli`), the
// direct-CLI driving path: interactive TUI passthrough and headless one-shot via
// `kiro-cli chat`. The structured (ACP) path lives in internal/acp; this package
// is the terminal-direct half. The shared launch core (capability wiring,
// Setup/Cleanup) lives in the embedded agent.LaunchBackend; this type adds only
// the kiro-specific Configure/Execute/buildArgs.
//
// LIVE-VERIFIED against an authenticated kiro-cli: hermetic backend parity
// (TestStartRun_BackendParity), a real oneshot `kiro-cli chat` echoing a
// sentinel planted in the materialized steering context back through a live
// model (J5's @live "Kiro" row), and --model HONOR — confirmed two
// independent ways: self-report matches the requested model across every
// model kiro-cli lists (auto, claude-*, deepseek, minimax, glm, qwen), and an
// unrecognized --model value is REJECTED before any chat runs ("Model '<x>'
// does not exist"), proving the flag reaches real model resolution rather
// than being accepted and ignored (cf. claude-code-acp's silent-ignore). NOT
// proven: the session history reader — internal/kiro/session.go is CONFIRMED
// BROKEN against real session files, see its own comment.
package kiro

import (
	"context"
	"io"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// defaultAgentName is the Kiro custom agent ctxloom materializes (.kiro/agents/
// <name>.json) and selects with --agent. Overridable via KiroConfig.Agent.
const defaultAgentName = "ctxloom"

// KiroConfig is kiro's typed LLM config. The backend owns this struct; the config
// package only carries the raw body that decodes into it.
type KiroConfig struct {
	Model      string            `mapstructure:"model"`
	BinaryPath string            `mapstructure:"binary_path"`
	Args       []string          `mapstructure:"args"`
	Env        map[string]string `mapstructure:"env"`
	// Effort maps to `--effort` (low|medium|high|xhigh|max).
	Effort string `mapstructure:"effort"`
	// Agent selects a materialized Kiro custom agent via `--agent` (default "ctxloom").
	Agent string `mapstructure:"agent"`
	// AgentEngine maps to `--agent-engine` (v1|v2|v3; Kiro's harness version).
	AgentEngine string `mapstructure:"agent_engine"`
}

// BackendType identifies the backend this config drives.
func (KiroConfig) BackendType() string { return "kiro" }

// Kiro implements the Backend interface for the Kiro CLI.
type Kiro struct {
	agent.LaunchBackend
	effort      string
	agentName   string
	agentEngine string
}

// NewKiro creates a new Kiro backend with default settings.
func NewKiro() *Kiro {
	b := &Kiro{agentName: defaultAgentName}
	b.BaseBackend = agent.NewBaseBackend("kiro", "1.0.0")
	b.BinaryPath = "kiro-cli"
	// kiro routes delivery through the cell seam. RawContext: Setup materializes the
	// content-addressed cache file (+ CTXLOOM_CONTEXT_FILE) as a pre-step, matching
	// the legacy lifecycle path. ContextHook stays false — kiro reads steering
	// rather than firing a SessionStart hook, so its context surface writes
	// .kiro/steering/ctxloom-context.md directly, and the merge hash is "".
	b.InitLaunch(
		agent.NewBaseLifecycle("kiro"),
		&KiroCommands{},
		agent.NewBaseContextProvider(),
		newKiroSessionHistory(),
		&agent.CellDelivery{Build: agent.BuildWellKnown(NewSurfaces), RawContext: true},
	)
	return b
}

// Configure applies a decoded kiro config (binary path, args, env, and the
// kiro-specific effort/agent/agent-engine overrides) to this backend.
func (b *Kiro) Configure(cfg agent.BackendConfig) {
	c, ok := cfg.(*KiroConfig)
	if !ok {
		return
	}
	agent.ApplyLocalCLIConfig(&b.BaseBackend, c.BinaryPath, c.Args, c.Env)
	if c.Effort != "" {
		b.effort = c.Effort
	}
	if c.Agent != "" {
		b.agentName = c.Agent
	}
	if c.AgentEngine != "" {
		b.agentEngine = c.AgentEngine
	}
}

// Execute runs the backend with the given request.
func (b *Kiro) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// Resolve the model: the role's labeled config supplies it via req.Model.
	// Kiro routes every model through Amazon Bedrock (Claude, Nova, open-weight),
	// so the provider is bedrock regardless of the model family — don't infer it
	// from the model name.
	modelName := req.Model
	modelInfo := &agent.ModelInfo{ModelName: modelName, Provider: "aws-bedrock"}

	// Auth is ambient (like claude): the user's `kiro-cli login` subscription,
	// or KIRO_API_KEY in the inherited env for headless — no auth env is set
	// here. The launch tail (trace/env/routing) is the shared ExecuteCLI.
	return b.ExecuteCLI(ctx, req, b.buildArgs(req, modelName), nil, modelInfo, stdout, stderr)
}

// buildArgs constructs the command-line arguments for `kiro-cli chat`.
//
// Oneshot uses `--no-interactive` and passes the prompt as the positional INPUT
// (Kiro prints the response and exits). Interactive passes the prompt as the
// first question and STAYS in the session, which needs the pty/stdin/resize
// wiring — hence the RunInteractive route in Execute.
func (b *Kiro) buildArgs(req *agent.ExecuteRequest, model string) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	args = append(args, "chat")

	// The ctxloom agent is materialized during Setup; SkipSetup (minimal/distill)
	// writes no agent, so don't select one — Kiro uses its default.
	if !req.SkipSetup && b.agentName != "" {
		args = append(args, "--agent", b.agentName)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if b.effort != "" {
		args = append(args, "--effort", b.effort)
	}
	if b.agentEngine != "" {
		args = append(args, "--agent-engine", b.agentEngine)
	}

	// SkipSetup (the minimal/distill path) gets no trust flag; bypass maps to
	// Kiro's blanket trust. Kiro has no read-only/edit-only tier, so every other
	// posture leaves its default prompting. Fine-grained
	// `--trust-tools=fs_read,fs_write` is a future refinement.
	if !req.SkipSetup && req.Permissions == agent.PermissionBypass {
		args = append(args, "--trust-all-tools")
	}

	prompt := agent.GetPromptContent(req.Prompt)
	if req.Mode == agent.ModeOneshot {
		args = append(args, "--no-interactive")
	}
	if prompt != "" {
		args = append(args, prompt)
	}

	return args
}
