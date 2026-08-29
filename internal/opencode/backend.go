//go:build parked_engines

// PARKED: this package builds only with -tags parked_engines, which nothing in
// this tree passes, so it is out of the default build. ctxloom supports one
// engine (claude) while a shared delivery-interface refactor (SurfaceSet,
// Deliver, SurfaceKind, Approach) proceeds — parking removes the mechanical
// tax of updating every engine's SurfaceSet implementation for an interface
// change that has not migrated it yet. See internal/lm/backends/registry.go
// for the (commented) registration this package returns to. Find every
// parked site with: grep -rn parked_engines.
//
// Package opencode implements the ctxloom Backend for opencode (the `opencode`
// CLI), driven over its first-party `opencode acp` mode — no third-party ACP
// adapter. This is the HOST-only chat spine (slice 1): structured chat + the
// headless oneshot projection of it, both riding the generic ACP driver in
// internal/acp. opencode has no `--model` flag on its acp subcommand; the model
// is delivered through a project-local opencode.json in the run's cwd (see
// chat.go), which opencode reads and validates strictly.
//
// LIVE-VERIFIED against opencode 1.18.1 authenticated to OpenRouter: a real
// oneshot chat over `opencode acp` round-tripped a requested nonce back through
// meta-llama/llama-3.3-70b-instruct:free, proving model delivery via
// opencode.json reaches real model resolution.
//
// Later slices layer native config onto the model key: MCP servers, a read-only
// `permission` for plan mode, and assembled context via `instructions` (slice 2),
// plus custom commands (bundle prompt/skill exports -> .opencode/command/, slice
// 3), plus Agent Skill packages (bundle skill exports -> .opencode/skill/ +
// `skills.paths`, Part B4). On the live path all of it rides transiently in Chat
// and is reverted after the run; the persistent `profile materialize` path uses
// the descriptor surfaces.
// The session-history reader (capabilities.go) drives opencode's own `session
// list`/`export` commands; interactive PTY launch rides opencode's TUI through
// the injected pty launcher (interactive.go).
package opencode

import (
	"context"
	"io"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// OpencodeConfig is opencode's typed LLM config. The backend owns this struct;
// the config package only carries the raw body that decodes into it.
type OpencodeConfig struct {
	Model      string            `mapstructure:"model"`
	BinaryPath string            `mapstructure:"binary_path"`
	Args       []string          `mapstructure:"args"`
	Env        map[string]string `mapstructure:"env"`
	// Thinking is the normalized cross-engine reasoning/thinking-budget
	// level (off|low|medium|high). opencode has NO wired mechanism for it
	// (verified by reading chat.go: no ACP config-option/env-var equivalent
	// found) — this field is read ONLY to detect an explicit setting and
	// warn, an honest documented no-op rather than a silent swallow.
	Thinking string `mapstructure:"thinking"`
}

// BackendType identifies the backend this config drives.
func (OpencodeConfig) BackendType() string { return "opencode" }

// Opencode implements the Backend interface for the opencode CLI over ACP.
type Opencode struct {
	agent.LaunchBackend
	model string
	// pendingCommands holds the host-assembled command exports captured during
	// Setup's surface build, so the LIVE chat path can materialize them
	// transiently. opencode's live delivery does not use the cell/surface path
	// (Setup writes no persistent surfaces — see NewOpencode); the CellDelivery
	// Build closure is the only place these host-resolved exports reach the
	// backend, so it stashes them here for Chat rather than delivering a surface.
	pendingCommands []agent.CommandExport
	// pendingContext is the assembled context string stashed at the same seam, so
	// the interactive TUI launch (interactive.go) can materialize it transiently as
	// the .opencode/ctxloom-context.md that opencode.json's `instructions` points at.
	pendingContext string
	// pendingSkills mirrors pendingCommands for the Agent Skills surface: the
	// host-assembled skill package exports captured during Setup, materialized
	// transiently by the LIVE chat path (chat.go) via the SAME
	// reconcileSkillsSurface function the persistent surfaces.go path binds.
	pendingSkills []agent.SkillExport
	// setupRan records that the CellDelivery seam above actually fired, i.e.
	// that Setup ran on THIS backend instance. Chat/launchInteractive depend
	// on Setup having run — the assembled context, commands and skills reach
	// them ONLY through that seam — and nothing asserted the order, so a
	// caller that skipped Setup got a run that delivered nothing and looked
	// entirely normal. assertSetupRan is that assertion.
	setupRan bool
}

// NewOpencode creates a new opencode backend with default settings.
func NewOpencode() *Opencode {
	b := &Opencode{}
	b.BaseBackend = agent.NewBaseBackend("opencode", "1.0.0")
	// Default binary name; a configured binary_path (this host's opencode is not
	// on PATH) overrides it via Configure/ApplyLocalCLIConfig.
	b.BinaryPath = "opencode"
	// The live run/oneshot path delivers everything (model, MCP, read-only
	// permission, and now commands) TRANSIENTLY in Chat, not via persistent
	// Setup surfaces — so the empty CellDelivery still runs the lifecycle merge but
	// writes no files. Its Build closure is, however, the seam where the
	// host-assembled command exports (inputs.Commands) reach this backend: it
	// stashes them for Chat to materialize transiently, then returns an empty
	// surface set so Setup itself writes nothing. (The persistent `profile
	// materialize` path is separate — it uses the descriptor's newSurfaces
	// builder, which DOES carry a commands surface; see surfaces.go.)
	b.InitLaunch(
		agent.NewBaseLifecycle("opencode"),
		agent.NewBaseContextProvider(),
		newOpencodeSessionHistory(b),
		&agent.CellDelivery{Build: func(in agent.SurfaceInputs, _ string) agent.SurfaceSet {
			b.setupRan = true
			b.pendingCommands = in.Commands
			b.pendingContext = in.Context
			b.pendingSkills = in.Skills
			return agent.EmptySurfaceSet{}
		}},
	)
	return b
}

// Configure applies a decoded opencode config (binary path, args, env, model).
func (b *Opencode) Configure(cfg agent.BackendConfig) {
	c, ok := cfg.(*OpencodeConfig)
	if !ok {
		return
	}
	agent.ApplyLocalCLIConfig(&b.BaseBackend, c.BinaryPath, c.Args, c.Env)
	if c.Model != "" {
		b.model = c.Model
	}
	// The normalized thinking knob has no wired mechanism on this backend
	// (see OpencodeConfig.Thinking's doc) — warn rather than silently
	// swallow an explicit setting.
	if c.Thinking != "" {
		clidiag.Warn("ctxloom", "opencode config declares thinking %q, but opencode has no wired reasoning-level mechanism; it is ignored", c.Thinking)
	}
}

// SupportedModes reports both modes: oneshot rides the `opencode acp` structured
// turn (chat.go), interactive launches opencode's TUI through the pty launcher
// (interactive.go). This is the BaseBackend default, spelled out here for clarity.
func (b *Opencode) SupportedModes() []agent.ExecutionMode {
	return []agent.ExecutionMode{agent.ModeInteractive, agent.ModeOneshot}
}

// Execute runs a ONESHOT prompt as a single structured ACP turn: one Chat
// session, one message, the assistant's streamed text rendered to stdout — a
// one-message projection of the StructuredChat path (chat.go), so the two paths
// cannot diverge. Model delivery (opencode.json) happens inside Chat.
func (b *Opencode) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// The provider is decided by opencode's own resolution of the openrouter/...
	// model string; "opencode" is honest, not a placeholder.
	modelInfo := &agent.ModelInfo{ModelName: req.Model, Provider: "opencode"}

	if req.DryRun {
		return &agent.ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
	}
	// Interactive launches opencode's TUI (interactive.go): model, MCP, context, and
	// the read-only permission ride a transient opencode.json overlay; bypass adds
	// --auto. Oneshot below is the `opencode acp` structured turn.
	if req.Mode == agent.ModeInteractive {
		return b.launchInteractive(ctx, req, modelInfo, stdout, stderr)
	}

	workDir := req.WorkDir
	if workDir == "" {
		workDir = b.WorkDir()
	}

	// This used to inline its own send/drain loop with no empty-prompt check
	// and no diagnostic for a textless turn (exit 0, zero bytes, silent) —
	// reprise flagged it byte-for-byte identical to internal/acp/execute.go's
	// Execute. Both now share this one plumbing.
	//
	// req.SkipSetup is an INTERNAL invocation (distillation/compaction —
	// see internal/acp/execute.go's identical fix for the full defect this
	// closes: an internal Chat-routed Execute used to leak the caller's own
	// CTXLOOM_SESSION_HARP down to the spawned `opencode acp` adapter, whose
	// SessionStart hook then rebound the caller's real session). Scrub it so
	// the hook still fires but binds nothing; a real delegated child or an
	// interactive session never sets SkipSetup, so it is unaffected.
	env := req.Env
	if req.SkipSetup {
		env = agent.ScrubInternalIdentityEnv(env)
	}
	return agent.RunOneshotTurn(req.Prompt, modelInfo, req.Verbosity, stdout, stderr,
		func(in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
			return b.Chat(ctx, agent.ChatRequest{
				WorkDir:     workDir,
				Model:       req.Model,
				Env:         env,
				Permissions: req.Permissions,
				MCPServers:  b.ManagedChatMCPServers(req.Env[agent.MCPCommandOverrideEnv]),
			}, in, out)
		})
}

// assertSetupRan is the assertion behind the Setup→Chat/launchInteractive
// execution-order dependency. It fires only in the shape that is actually a
// silent no-op: Setup never ran AND this seam is therefore holding nothing to
// deliver. It warns rather than refusing, because an ACP-hosted session
// legitimately skips Setup and carries its context in the lead turn instead —
// but it says so, which is the whole point.
func (b *Opencode) assertSetupRan(where string) {
	if b.setupRan {
		return
	}
	if b.pendingContext != "" || len(b.pendingCommands) > 0 || len(b.pendingSkills) > 0 {
		return
	}
	clidiag.Warn("ctxloom", "opencode %s: ctxloom Setup did not run for this backend, so no assembled context, commands or skills were delivered through the opencode overlay — this run sees only what opencode finds on its own", where)
}
