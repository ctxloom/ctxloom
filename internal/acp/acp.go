package acp

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// clientInfo identity advertised in the initialize request (recommended by the
// spec). The SDK's api.InitializeRequest predates the clientInfo field, so the
// initialize params are assembled locally (see initializeParams in session.go).
const (
	clientName    = "ctxloom"
	clientVersion = "1.0.0"
)

// ACPConfig is the generic ACP client's typed LLM config. Unlike a per-engine
// config it does not name a fixed binary: `command` is the ACP-mode invocation of
// whichever agent this entry drives (e.g. "kiro-cli acp", "claude-agent-acp"),
// and agent_engine records which target that is (for the future settings-writer
// delegation — see doc.go). The backend owns this struct; the config package only
// carries the raw body that decodes into it.
type ACPConfig struct {
	// Command is the agent's ACP-mode invocation, whitespace-split into the
	// binary and its leading args (e.g. "kiro-cli acp" → kiro-cli + ["acp"]).
	Command string `mapstructure:"command"`
	// Agent selects a named agent on the target CLI via `--agent`.
	Agent string `mapstructure:"agent"`
	// AgentEngine names the target agent (kiro/claude/codex/agy). Drives the
	// future config-materialization delegation; also passed as `--agent-engine`.
	AgentEngine string `mapstructure:"agent_engine"`
	// Model is passed to the spawned agent via `--model` when set.
	Model string `mapstructure:"model"`

	BinaryPath string            `mapstructure:"binary_path"`
	Args       []string          `mapstructure:"args"`
	Env        map[string]string `mapstructure:"env"`
}

// BackendType identifies the backend this config drives.
func (ACPConfig) BackendType() string { return "acp" }

// ACP is the generic Agent Client Protocol client backend. It embeds the shared
// launch core for identity/config plumbing, but its real work is the
// StructuredChat driver (see session.go): it does not run an interactive pty
// itself — it spawns `<agent> acp` and drives it over JSON-RPC.
type ACP struct {
	agent.LaunchBackend
	writeSettings agent.WriteSettingsFunc

	// config knobs applied by Configure.
	command     string
	agentName   string
	agentEngine string
	model       string

	// openTransport spawns (or, in tests, fakes) the ACP subprocess transport.
	// nil → spawnTransport (a real `<agent> acp` process).
	openTransport transportFunc
	// now stamps chat entries that arrive without a timestamp (ACP session/update
	// carries no per-event time). Injected for deterministic tests; nil = time.Now.
	now func() time.Time
}

// NewChatDriver builds an ACP client for EMBEDDING inside a target agent's own
// backend: kiro/codex implement agent.StructuredChat by delegating to this
// driver with their agent-specific ACP command (e.g. "kiro-cli acp",
// "codex-acp"). The embedding backend owns setup/materialization — this driver
// only speaks the protocol, which is exactly why the delegation keeps the
// target's native config correct (see doc.go).
func NewChatDriver(cfg ACPConfig) *ACP {
	b := NewACP(nil)
	b.Configure(&cfg)
	return b
}

// NewACP constructs a generic ACP client backend. The writeSettings dispatch is
// injected for parity with the other backends (nil for an embedded chat driver,
// whose owner runs its own lifecycle — see NewChatDriver).
func NewACP(writeSettings agent.WriteSettingsFunc) *ACP {
	b := &ACP{writeSettings: writeSettings}
	b.BaseBackend = agent.NewBaseBackend("acp", "1.0.0")
	b.BinaryPath = "" // resolved from ACPConfig.Command / BinaryPath at Configure time
	b.InitLaunch(
		agent.NewBaseLifecycle("acp", b.writeSettings),
		&acpSkills{},
		agent.NewBaseContextProvider(),
		&acpSessionHistory{},
		// acp materializes no files (its loadout rides the ACP protocol, see
		// surfaces.go), so it delivers an EMPTY surface set: Setup still runs the
		// lifecycle merge that populates ManagedChatMCPServers, but writes nothing.
		&agent.CellDelivery{Build: func(agent.SurfaceInputs, string) agent.SurfaceSet {
			return agent.EmptySurfaceSet{}
		}},
	)
	return b
}

// Configure applies a decoded ACP config to this backend.
func (b *ACP) Configure(cfg agent.BackendConfig) {
	c, ok := cfg.(*ACPConfig)
	if !ok {
		return
	}
	agent.ApplyLocalCLIConfig(&b.BaseBackend, c.BinaryPath, c.Args, c.Env)
	if c.Command != "" {
		b.command = c.Command
	}
	if c.Agent != "" {
		b.agentName = c.Agent
	}
	if c.AgentEngine != "" {
		b.agentEngine = c.AgentEngine
	}
	if c.Model != "" {
		b.model = c.Model
	}
	// If Command names the binary (the common case) and no explicit binary_path
	// was given, adopt Command's first field as the binary so BaseBackend can run
	// it. chatArgv still prepends Command's trailing args.
	if c.BinaryPath == "" && b.BinaryPath == "" {
		if bin, _ := splitCommand(b.command); bin != "" {
			b.BinaryPath = bin
		}
	}
}

// clock returns the timestamp source for chat entries, defaulting to time.Now.
func (b *ACP) clock() func() time.Time {
	if b.now != nil {
		return b.now
	}
	return time.Now
}

// chatArgv builds the argv for the spawned ACP subprocess: Command's trailing
// args (its ACP subcommand), then configured Args, then the target-agent flags.
//
// The `--agent` / `--model` / `--agent-engine` flags mirror the kiro backend's
// direct-CLI flags. They are a pragmatic first cut: real ACP agents vary in which
// flags they accept, so per-engine flag mapping (keyed on agent_engine) is a
// later refinement. An agent that rejects an unknown flag would fail to spawn —
// hence they are only appended when set.
func (b *ACP) chatArgv(req agent.ChatRequest) []string {
	_, cmdArgs := splitCommand(b.command)

	args := make([]string, 0, len(cmdArgs)+len(b.Args)+6)
	args = append(args, cmdArgs...)
	args = append(args, b.Args...)

	if b.agentName != "" {
		args = append(args, "--agent", b.agentName)
	}
	model := b.model
	if req.Model != "" {
		model = req.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if b.agentEngine != "" {
		args = append(args, "--agent-engine", b.agentEngine)
	}
	return args
}

// splitCommand whitespace-splits a command string into its binary and leading
// args, e.g. "kiro-cli acp" → ("kiro-cli", ["acp"]).
func splitCommand(command string) (bin string, args []string) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], fields[1:]
}

// --- subprocess transport ---

// transport is the I/O seam for one ACP conversation: the child's stdin (we write
// JSON-RPC frames here), the child's stdout (we read frames here), and teardown.
// Tests inject in-memory pipes so they never spawn a process.
type transport struct {
	stdin  io.WriteCloser
	stdout io.Reader
	close  func() error
}

type transportFunc func(ctx context.Context, argv []string, env map[string]string, workDir string) (*transport, error)

// spawnTransport launches the real `<agent> acp` process with piped stdio (no
// pty). stderr passes through for diagnostics, matching the claude chat driver.
func (b *ACP) spawnTransport(ctx context.Context, argv []string, env map[string]string, workDir string) (*transport, error) {
	cmd := exec.CommandContext(ctx, b.BinaryPath, argv...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &transport{
		stdin:  stdin,
		stdout: stdout,
		close: func() error {
			_ = stdin.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			return cmd.Wait()
		},
	}, nil
}

// warnf routes a diagnostic through ctxloom's standard "ctxloom: warning:" path.
// Centralized here so the codec (jsonrpc.go) stays free of the agent import.
func warnf(format string, args ...any) { agent.Warn(format, args...) }
