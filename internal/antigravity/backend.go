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
	writeSettings agent.WriteSettingsFunc
}

// NewAntigravity creates a new Antigravity backend with default settings. The
// writeSettings dispatch is injected (the registry supplies it).
func NewAntigravity(writeSettings agent.WriteSettingsFunc) *Antigravity {
	b := &Antigravity{writeSettings: writeSettings}
	b.BaseBackend = agent.NewBaseBackend("antigravity", "1.0.0")
	b.BinaryPath = "agy"
	b.InitLaunch(
		agent.NewBaseLifecycle("antigravity", b.writeSettings),
		&AntigravitySkills{},
		agent.NewBaseContextProvider(),
		NewAntigravitySessionHistory(b),
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
	return b.ExecuteCLI(ctx, req, b.buildArgs(req, modelName), modelInfo, stdout, stderr)
}

// buildArgs constructs the command-line arguments for agy.
func (b *Antigravity) buildArgs(req *agent.ExecuteRequest, model string) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	if model != "" {
		args = append(args, "--model", model)
	}

	// agy v1.0.7 has no read-only/plan or edit-only approval mode, so only the
	// blanket bypass maps to a flag; every other posture (and SkipSetup, which
	// auto-approves baseline tools regardless) leaves agy's defaults.
	if !req.SkipSetup && req.Permissions == agent.PermissionBypass {
		args = append(args, "--dangerously-skip-permissions")
	}

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
