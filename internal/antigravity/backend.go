package antigravity

import (
	"context"
	"io"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// AntigravityConfig is antigravity's typed LLM config. The backend owns this
// struct; the config package only carries the raw body that decodes into it.
type AntigravityConfig struct {
	Model      string            `mapstructure:"model"`
	BinaryPath string            `mapstructure:"binary_path"`
	Args       []string          `mapstructure:"args"`
	Env        map[string]string `mapstructure:"env"`
}

// BackendType identifies the backend this config drives.
func (AntigravityConfig) BackendType() string { return "antigravity" }

// Antigravity implements the Backend interface for Antigravity CLI (agy). The
// shared launch core (capability wiring, accessors, Setup/Cleanup) lives in
// the embedded agent.LaunchBackend; this type adds only the agy-specific
// Configure/Execute.
type Antigravity struct {
	agent.LaunchBackend
	// convMap is agy's own workDir->conversation-UUID cache reader
	// (capabilities.go). NOT a SessionHistory — that scraper was deleted in
	// tough-cloud S5 (tall-grab: it mis-keyed the global brain store). convMap
	// is a live continuation lookup chat.go's resolveChatConversationID
	// depends on every oneshot turn, unrelated to historical transcript
	// reads.
	convMap agyConversationMap
}

// NewAntigravity creates a new Antigravity backend with default settings.
func NewAntigravity() *Antigravity {
	b := &Antigravity{convMap: newAgyConversationMap()}
	b.BaseBackend = agent.NewBaseBackend("antigravity", "1.0.0")
	b.BinaryPath = "agy"
	// agy routes delivery through the cell seam. RawContext: Setup materializes the
	// content-addressed cache file (+ CTXLOOM_CONTEXT_FILE) as a pre-step, matching
	// the legacy lifecycle path. ContextHook stays false — agy fires no SessionStart
	// hook, so its context surface writes .agents/AGENTS.md (auto-read) directly,
	// and the merge hash is "" (no injection hook).
	b.InitLaunch(
		agent.NewBaseLifecycle("antigravity"),
		agent.NewBaseContextProvider(),
		nil, // SessionHistory: agy's transcript_full.jsonl scraper deleted, tough-cloud S5 — canonical capture is the only transcript source now
		&agent.CellDelivery{Build: agent.BuildWellKnown(NewSurfaces), RawContext: true},
	)
	return b
}

// Configure applies a decoded antigravity config (binary path, args, env) to
// this backend. Without the Configurable type-assertion matching this
// signature, a labeled antigravity entry's overrides would never take effect.
func (b *Antigravity) Configure(cfg agent.BackendConfig) {
	if c, ok := cfg.(*AntigravityConfig); ok {
		agent.ApplyLocalCLIConfig(&b.BaseBackend, c.BinaryPath, c.Args, c.Env)
	}
}

// Execute runs the backend with the given request.
func (b *Antigravity) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// Resolve the model: explicit request (the role's labeled config supplies
	// it) or agy's own configured default. Unlike gemini, no fallback model is
	// forced here — agy is closed-source and fast-moving, so its current
	// default tier is the safer choice when nothing is pinned.
	modelName := req.Model
	modelInfo := &agent.ModelInfo{ModelName: modelName, Provider: "google"}

	// buildArgs routes on mode alone: ModeInteractive with an initial prompt
	// builds `-i <prompt>` (agy runs the prompt then STAYS in the session),
	// which needs the pty/stdin/resize wiring just as much as a bare
	// interactive launch — running it non-interactively would leave a dead
	// session. The launch tail (trace/env/routing) is the shared ExecuteCLI.
	return b.ExecuteCLI(ctx, req, b.buildArgs(req, modelName), nil, modelInfo, stdout, stderr)
}

// buildArgs constructs the command-line arguments for agy.
func (b *Antigravity) buildArgs(req *agent.ExecuteRequest, model string) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	if model != "" {
		args = append(args, "--model", model)
	}

	// agy 1.1.2 DOES have --mode plan / --mode accept-edits (the stale "v1.0.7
	// has no plan mode" claim this comment used to make is FALSE — VERIFIED via
	// `agy --help`, current install is 1.1.2). SkipSetup maps to plan like every
	// other engine's minimal/distill path (codex SkipSetup -> read-only), not to
	// agy's own prompting.
	//
	// LIVE FINDING (2026-07-15, authenticated agy 1.1.2, real model turns): the
	// --mode plan flag is NOT enforced in headless `-p` execution. A live
	// sentinel-write probe under `--mode plan` (and again with `--mode plan
	// --sandbox`) overwrote the target file exactly like the bypass positive
	// control, and the model self-reported "I am not currently in plan mode or
	// read-only mode" when asked directly. So this flag is passed as the best
	// available, documented mapping (and may still gate agy's INTERACTIVE `-i`
	// TUI, which was not exercised here), but it must NOT be trusted as a
	// genuine read-only boundary for headless runs — see
	// backends.EnforcesReadOnlyPlan, which deliberately does NOT list
	// "antigravity" so ctxloom's own resolver keeps collapsing plan to a safer
	// posture rather than trusting a flag proven not to enforce.
	switch {
	case req.SkipSetup, req.Permissions == agent.PermissionPlan:
		args = append(args, "--mode", "plan")
	case req.Permissions == agent.PermissionAcceptEdits:
		args = append(args, "--mode", "accept-edits")
	case req.Permissions == agent.PermissionBypass:
		args = append(args, "--dangerously-skip-permissions")
	}
	// PermissionDefault: no flag — agy's own prompting (unreachable headless).

	if prompt := agent.GetPromptContent(req.Prompt); prompt != "" {
		// Oneshot: -p runs headless and exits. Interactive: -i runs the
		// prompt then stays in the session.
		if req.Mode == agent.ModeOneshot {
			args = append(args, "-p", prompt)
		} else {
			args = append(args, "-i", prompt)
		}
	}

	return args
}
