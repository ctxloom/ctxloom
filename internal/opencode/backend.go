// Package opencode implements the ctxloom Backend for opencode (the `opencode`
// CLI). This is a HOST-only, INTERACTIVE-only spine: opencode's TUI is launched
// through the injected pty launcher (interactive.go). opencode exposes no
// native non-interactive turn, so oneshot is not offered — see SupportedModes.
//
// opencode has no `--model` flag, so the model is delivered through a
// project-local opencode.json in the run's cwd, which opencode reads and
// validates strictly. Layered onto that same key: MCP servers, a read-only
// `permission` for plan mode, assembled context via `instructions`, custom
// commands (bundle prompt/skill exports -> .opencode/command/) and Agent Skill
// packages (-> .opencode/skill/ + `skills.paths`). On the live path all of it
// rides a TRANSIENT overlay reverted after the run; the persistent
// `profile materialize` path uses the descriptor surfaces.
//
// The session-history reader (capabilities.go) drives opencode's own `session
// list`/`export` commands.
package opencode

import (
	"context"
	"fmt"
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
	// — this field is read ONLY to detect an explicit setting and
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

// SupportedModes reports INTERACTIVE ONLY: opencode's TUI through the pty
// launcher (interactive.go). opencode exposes no native non-interactive turn —
// its oneshot ran as a structured `opencode acp` turn, and that transport is
// gone — so oneshot is not advertised rather than being silently served by
// something that is not one.
func (b *Opencode) SupportedModes() []agent.ExecutionMode {
	return []agent.ExecutionMode{agent.ModeInteractive}
}

// Execute launches opencode's TUI (interactive.go): model, MCP, context, and
// the read-only permission ride a transient opencode.json overlay; bypass adds
// --auto.
//
// Any other mode is REFUSED, not approximated. opencode has no native
// non-interactive turn (see SupportedModes), and quietly launching a TUI for a
// caller that asked for a scripted oneshot would hand back a session no script
// can drive while reporting success.
func (b *Opencode) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// The provider is decided by opencode's own resolution of the openrouter/...
	// model string; "opencode" is honest, not a placeholder.
	modelInfo := &agent.ModelInfo{ModelName: req.Model, Provider: "opencode"}

	if req.DryRun {
		return &agent.ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
	}
	if req.Mode != agent.ModeInteractive {
		return nil, fmt.Errorf("opencode supports interactive runs only; run it without a oneshot prompt, or choose an engine that has a non-interactive mode (`ctxloom llm list`)")
	}
	return b.launchInteractive(ctx, req, modelInfo, stdout, stderr)
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
