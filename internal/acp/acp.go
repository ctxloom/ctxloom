package acp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
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

const (
	// claudeEngineName is the agent_engine value naming claude (matched
	// case-insensitively — see ACPConfig.AgentEngine's kiro/claude/codex/agy
	// vocabulary). It keys the automatic CLAUDECODE strip below, and mirrors
	// the same alias other ctxloom engine-name tables use for claude (e.g.
	// internal/ltk/engine.engineAliases).
	claudeEngineName = "claude"
	// claudeGuardEnv is claude's nested-session guard variable (see
	// internal/claude/chat.go's chatACPConfig doc comment for the full
	// story): claude 2.x refuses to start with it set, and it leaks into a
	// delegated child as pure process-tree lineage regardless of which
	// backend spawned the child.
	claudeGuardEnv = "CLAUDECODE"
	// claudeModelEnvVar is the claude SDK's own model-selector env var (see
	// internal/claude/chat.go's chatACPConfig doc comment): claude-code-acp
	// 0.16.2 silently ignores the driver's --model argv, so this is the
	// load-bearing delivery. A generic entry driving claude (agent_engine)
	// gets it defaulted the same way claude-code's own backend does.
	claudeModelEnvVar = "ANTHROPIC_MODEL"
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
	// ModelEnvVar, when set, ALSO delivers the request's model into the
	// spawned agent's environment under this variable name (e.g. claude's
	// SDK selector ANTHROPIC_MODEL). Real adapters vary in whether the
	// `--model` argv is honored at all — claude-code-acp 0.16.2 silently
	// ignores it and runs the session on the user's saved interactive
	// default, which the resolveChatModel gate exists to prevent — so the
	// engine-native env var is the load-bearing delivery where the
	// embedding backend knows one.
	ModelEnvVar string `mapstructure:"model_env_var"`
	// ModelConfigKey, when set, delivers the request's model via a
	// `-c <key>=<value>` config-override flag INSTEAD OF the generic
	// `--model` flag (mutually exclusive with it — see chatArgv). Wave C3
	// finding: codex-acp 0.16.0 has NO `--model` flag at all — verified
	// live, it REJECTS the argv outright at CLI parse (exit 2, "unexpected
	// argument '--model' found"), which would break every codex chat spawn
	// with a non-empty model, not just leave it silently ignored like
	// claude-code-acp. codex-acp's own `-c key=value` override (its
	// config.toml dotted-path convention, e.g. `-c model="o3"`, confirmed
	// live) is the only mechanism that works.
	ModelConfigKey string `mapstructure:"model_config_key"`
	// StripEnv names inherited environment variables REMOVED from the spawned
	// agent's env. The embedding backend uses it to drop a variable whose
	// inherited presence would be a false signal to the child engine — e.g.
	// claude's CLAUDECODE nested-session guard, which the delegated-child
	// topology inherits from the parent engine's process tree even though the
	// child is a deliberately-launched independent engine (see
	// internal/claude/chat.go).
	StripEnv []string `mapstructure:"strip_env"`

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

	// config knobs applied by Configure.
	command        string
	agentName      string
	agentEngine    string
	model          string
	modelEnvVar    string
	modelConfigKey string
	stripEnv       []string

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
	b := NewACP()
	b.Configure(&cfg)
	return b
}

// NewACP constructs a generic ACP client backend.
func NewACP() *ACP {
	b := &ACP{}
	b.BaseBackend = agent.NewBaseBackend("acp", "1.0.0")
	b.BinaryPath = "" // resolved from ACPConfig.Command / BinaryPath at Configure time
	b.InitLaunch(
		agent.NewBaseLifecycle("acp"),
		&acpCommands{},
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
	if c.ModelEnvVar != "" {
		b.modelEnvVar = c.ModelEnvVar
	}
	if c.ModelConfigKey != "" {
		b.modelConfigKey = c.ModelConfigKey
	}
	if len(c.StripEnv) > 0 {
		b.stripEnv = c.StripEnv
	}
	// A generic ACP entry whose agent_engine names claude drives the SAME
	// claude engine claude-code's own backend does (internal/claude/chat.go's
	// chatACPConfig) — and inherits CLAUDECODE from the parent claude's
	// process tree as pure lineage under agentcoord delegation exactly like
	// that path, since claude 2.x refuses to start with the nested-session
	// guard set regardless of which ctxloom backend spawned it. claude-code's
	// own backend strips it unconditionally; a GENERIC entry only knows
	// it is driving claude via agent_engine, so key the strip on that here —
	// in addition to, never instead of, any user-configured strip_env.
	if strings.EqualFold(b.agentEngine, claudeEngineName) {
		if !slices.Contains(b.stripEnv, claudeGuardEnv) {
			b.stripEnv = append(b.stripEnv, claudeGuardEnv)
		}
		// Wave C3 sibling of the strip above (mauve-plop item 2 / c5917a6
		// precedent): a generic entry driving claude needs the SAME
		// ANTHROPIC_MODEL delivery claude-code's own chatACPConfig sets,
		// for the identical reason (claude-code-acp 0.16.2 silently
		// ignores --model argv). Only DEFAULTS it — an explicit
		// model_env_var in the entry (already applied above) always wins.
		if b.modelEnvVar == "" {
			b.modelEnvVar = claudeModelEnvVar
		}
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
// hence they are only appended when set. Confirmed live per Wave C3: kiro-cli
// acp accepts --agent/--model/--agent-engine at CLI-parse (its own documented
// flags); codex-acp accepts NONE of the three — it exits 2 ("unexpected
// argument") on any of them — which is exactly why ModelConfigKey exists
// below as model's alternate delivery and why codex's embedding config
// (internal/codex/chat.go) never sets Agent/AgentEngine.
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
		if b.modelConfigKey != "" {
			// codex-acp shape: `-c model="<value>"` — the value is parsed as
			// TOML by the adapter itself (README: `-c model="o3"`; verified
			// live), so it is quoted, never passed bare. Mutually exclusive
			// with --model: an adapter that needs this rejects --model too.
			args = append(args, "-c", fmt.Sprintf("%s=%q", b.modelConfigKey, model))
		} else {
			// kiro-cli path: HONORED — confirmed live over this exact `kiro-cli
			// acp` JSON-RPC session. An invalid --model value surfaces as a
			// genuine mid-session RPC error ("model '<x>' is not available"),
			// and a valid one changes both the self-reported model and the
			// turn's wall-clock duration (cf. claude-code-acp's silent-ignore,
			// which does neither).
			args = append(args, "--model", model)
		}
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
	cmd.Env = spawnEnv(os.Environ(), b.stripEnv, env)
	// setpgid puts the spawned agent in its OWN, fresh process group so
	// close's killProcessGroup can reap the whole tree — not just the
	// immediate child — on teardown. codex-acp (moral-scorn) double-forks a
	// worker that survives a plain Process.Kill on just the spawned pid
	// (the worker reparents to PPID=1 but stays in the same process group);
	// see procgroup_unix.go/procgroup_windows.go.
	setpgid(cmd)
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
				// Kill the WHOLE process group (not just cmd.Process) — this is
				// the moral-scorn fix: a plain Process.Kill here left codex-acp's
				// detached worker running indefinitely after every Chat().
				_ = killProcessGroup(cmd)
			}
			return cmd.Wait()
		},
	}, nil
}

// spawnEnv builds the spawned agent's environment: the inherited base minus
// the configured StripEnv variables, then the per-launch overlay appended
// (os/exec dedupes on key, last wins). Stripping happens on the BASE only: an
// overlay entry always lands, even for a stripped key — the caller set it
// deliberately.
func spawnEnv(base, strip []string, overlay map[string]string) []string {
	out := make([]string, 0, len(base)+len(overlay))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(strip, k) {
			out = append(out, kv)
		}
	}
	for k, v := range overlay {
		out = append(out, k+"="+v)
	}
	return out
}

// warnf routes a diagnostic through ctxloom's standard "ctxloom: warning:" path.
// Centralized here so the codec (jsonrpc.go) stays free of the agent import.
func warnf(format string, args ...any) { agent.Warn(format, args...) }
