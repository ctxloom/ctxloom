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
// model (J000400's @live "Kiro" row), and --model HONOR — confirmed two
// independent ways: self-report matches the requested model across every
// model kiro-cli lists (auto, claude-*, deepseek, minimax, glm, qwen), and an
// unrecognized --model value is REJECTED before any chat runs ("Model '<x>'
// does not exist"), proving the flag reaches real model resolution rather
// than being accepted and ignored (cf. claude-code-acp's silent-ignore).
//
// The session-history reader (formerly internal/kiro/session.go) was DELETED
// outright, not demoted to a vendor reader — the user's explicit decision. It was
// CONFIRMED BROKEN against real session files (tall-grab): it read the v1
// sessions/cli/*.jsonl store, but a real `kiro-cli chat --no-interactive`
// oneshot — the mode ctxloom's own oneshot Execute path uses — persists into
// a structurally different v2 SQLite blob this reader never parsed. Kiro's
// structured Chat driver already streams the real conversation through ACP,
// captured canonically instead (see internal/transcript); Kiro's
// Backend.History() now returns nil.
package kiro

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	// Effort maps to `--effort` (low|medium|high|xhigh|max). This is kiro's
	// OWN native knob for the DIRECT interactive/oneshot CLI path
	// (buildArgs) — distinct from the normalized cross-engine Thinking
	// field below, which this backend does NOT wire anywhere (see Thinking's
	// own doc).
	Effort string `mapstructure:"effort"`
	// Agent selects a materialized Kiro custom agent via `--agent` (default "ctxloom").
	Agent string `mapstructure:"agent"`
	// AgentEngine maps to `--agent-engine` (v1|v2|v3; Kiro's harness version).
	AgentEngine string `mapstructure:"agent_engine"`
	// Thinking is the normalized cross-engine reasoning/thinking-budget level
	// (off|low|medium|high). Kiro has NO wired mechanism for it: chat.go's
	// ACP structured-chat path (kiro-cli acp) sets no ModelConfigKey/
	// ModelEnvVar-equivalent for reasoning, and this field is read ONLY to
	// detect an explicit setting and warn — an honest documented no-op, not
	// a silent swallow. Use kiro's own native Effort above instead.
	Thinking string `mapstructure:"thinking"`
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
		agent.NewBaseContextProvider(),
		nil, // SessionHistory: kiro's sessions/cli/*.jsonl scraper deleted — canonical capture is the only transcript source now
		&agent.CellDelivery{
			// A plain agent.BuildWellKnown(NewSurfaces) has no way to
			// see this backend's configured agent-name override — SurfaceInputs
			// is built generically in setupViaCells with no knowledge of any
			// concrete backend's fields. This closure captures b and reads
			// b.agentName at BUILD time (i.e. after Configure has run), so the
			// materialized .kiro/agents/<name>.json agrees with whatever name
			// buildArgs launches with.
			Build: func(in agent.SurfaceInputs, _ string) agent.SurfaceSet {
				in.AgentName = b.agentName
				return NewSurfaces(in, nil)
			},
			RawContext: true,
		},
	)
	return b
}

// Configure applies a decoded kiro config (binary path, args, env, and the
// kiro-specific effort/agent/agent-engine overrides) to this backend.
func (b *Kiro) Configure(cfg agent.BackendConfig) {
	c, ok := cfg.(*KiroConfig)
	if !ok {
		// Never silently: with no KiroConfig applied, EVERY override (binary
		// path, args, env, effort, agent, agent-engine) is dropped and the run
		// launches on defaults — a mis-wiring that would otherwise surface a
		// whole session later, naming neither the cause nor the config that
		// caused it. Nil is the same class and must not panic while saying so.
		declared := "<nil>"
		if cfg != nil {
			declared = cfg.BackendType()
		}
		clidiag.Warn("ctxloom", "kiro: ignoring a %q config this backend cannot read (want *kiro.KiroConfig) — the kiro backend is left unconfigured and every override in it is dropped", declared)
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
	// The normalized thinking knob has no wired mechanism on this backend
	// (see KiroConfig.Thinking's doc) — warn rather than silently swallow an
	// explicit setting, so a user who set it learns it did nothing instead
	// of wrongly assuming it worked.
	if c.Thinking != "" {
		clidiag.Warn("ctxloom", "kiro config declares thinking %q, but kiro has no wired reasoning-level mechanism; it is ignored — use kiro's own \"effort\" instead", c.Thinking)
	}
}

// Execute runs the backend with the given request.
func (b *Kiro) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// A oneshot gets exactly one turn, so an empty prompt asks nothing at all:
	// `kiro-cli chat --no-interactive` with no INPUT positional and no stdin is
	// a launch that cannot produce an answer. Refuse before spawning, the same
	// floor the shared ACP-shaped turn applies (agent.RunOneshotTurn). An
	// INTERACTIVE launch with no prompt is legitimate — it opens a session.
	// A dry run is a preview of the argv, not a turn, so it stays exempt.
	if !req.DryRun && req.Mode == agent.ModeOneshot &&
		strings.TrimSpace(agent.GetPromptContent(req.Prompt)) == "" {
		return nil, errors.New("kiro: nothing to run — a one-shot gets exactly one turn and this one carries no prompt")
	}

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

	// Map the generalized permission posture onto kiro's --trust-tools /
	// --trust-all-tools flags (this used to be bypass-only — read-only
	// silently collapsed to kiro's own prompting, the worst-collapsed engine
	// of the three under-mapped ones). SkipSetup forces the read-only arm
	// regardless of a requested bypass, matching codex's identical
	// SkipSetup-wins switch shape (a distillation run must never widen to
	// bypass just because the label's configured posture happens to be
	// bypass).
	//
	// LIVE VERIFIED 2026-07-15 (authenticated kiro-cli 2.12.1): a headless
	// sentinel-write probe under `--trust-tools=fs_read` left the target file
	// byte-unchanged, with kiro-cli printing "Command fs_write is rejected
	// because it matches one or more rules on the denied list: - non-
	// interactive mode (no user to approve)"; `--trust-tools=fs_read,fs_write`
	// and `--trust-all-tools` (positive controls) both let the identical write
	// land. So fs_read/fs_write are confirmed as kiro's real tool-name
	// vocabulary (also grepped directly from kiro-cli's own bundled tui.js
	// alongside "execute_bash", the third tool the allowlist implicitly
	// excludes here).
	switch {
	case req.SkipSetup, req.Permissions == agent.PermissionPlan:
		args = append(args, "--trust-tools=fs_read")
	case req.Permissions == agent.PermissionAcceptEdits:
		args = append(args, "--trust-tools=fs_read,fs_write")
	case req.Permissions == agent.PermissionBypass:
		args = append(args, "--trust-all-tools")
	}
	// PermissionDefault: no flag — kiro's own prompting.

	prompt := agent.GetPromptContent(req.Prompt)
	if req.Mode == agent.ModeOneshot {
		args = append(args, "--no-interactive")
	}
	if prompt != "" {
		args = append(args, prompt)
	}

	return args
}
