package acp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/stderrtail"
)

// clientInfo identity advertised in the initialize request (recommended by
// the spec) — see api.InitializeRequest.ClientInfo, built in session.go's
// setup.
const (
	clientName    = "ctxloom"
	clientVersion = "1.0.0"
)

const (
	// claudeEngineName is the agent_engine value naming claude (matched
	// case-insensitively — see ACPConfig.AgentEngine's kiro/claude/codex
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
	// AgentEngine names the target agent (kiro/claude/codex). Drives the
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
	// ReasoningConfigKey, when set, delivers a resolved reasoning/thinking
	// value via an ADDITIONAL `-c <key>=<value>` override — independent of
	// ModelConfigKey; both render together when both are set (chatArgv).
	// Unlike Model there is no per-request override: the embedding backend
	// (internal/codex/chat.go) resolves the concrete value from the agent's
	// normalized thinking level (off|low|medium|high) BEFORE constructing
	// this config, from the labeled config's own static `thinking` field —
	// so ReasoningEffort is already the literal string to send, not a
	// request-time lookup. e.g. codex-acp's "model_reasoning_effort".
	ReasoningConfigKey string `mapstructure:"reasoning_config_key"`
	// ReasoningEffort is the resolved value ReasoningConfigKey delivers.
	// Empty means "no override" even if ReasoningConfigKey is set (never
	// renders `-c key=""`, which would overwrite the adapter's own default
	// with an explicit empty string).
	ReasoningEffort string `mapstructure:"reasoning_effort"`
	// ReasoningSummaryConfigKey/ReasoningSummary are a companion override in
	// the same shape as ReasoningConfigKey/ReasoningEffort — codex's
	// model_reasoning_summary, sent alongside model_reasoning_effort so an
	// enabled reasoning level actually surfaces a summary in the stream.
	ReasoningSummaryConfigKey string `mapstructure:"reasoning_summary_config_key"`
	ReasoningSummary          string `mapstructure:"reasoning_summary"`
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

	// reasoningConfigKey/reasoningEffort and their summary counterparts mirror
	// modelConfigKey's shape for the normalized thinking-level knob (see
	// ACPConfig.ReasoningConfigKey). Unlike model there is no per-request
	// override to layer on top in chatArgv — these are already the resolved,
	// static values the embedding backend computed at Configure time.
	reasoningConfigKey        string
	reasoningEffort           string
	reasoningSummaryConfigKey string
	reasoningSummary          string

	// openTransport spawns (or, in tests, fakes) the ACP subprocess transport.
	// nil → spawnTransport (a real `<agent> acp` process).
	openTransport transportFunc
	// now stamps chat entries that arrive without a timestamp (ACP session/update
	// carries no per-event time). Injected for deterministic tests; nil = time.Now.
	now func() time.Time

	// containerImage, when non-empty, overrides the container spec's
	// resolved image (isolation.Container.WithImage) instead of the
	// engine's real one. Test-only seam: the docker-gated container
	// transport proof points this at a minimal harness image instead of a
	// real (credentialed, multi-hundred-MB) engine image; every production
	// construction path leaves it empty.
	containerImage string

	// shutdownGrace bounds how long a transport's close() waits for the spawned
	// agent to exit ON ITS OWN (after stdin EOF) before force-killing it — its
	// process group on the host, the container itself under runtime:container. tidy-gush: claude-code-acp writes its own
	// NATIVE session transcript asynchronously; a zero-grace immediate kill
	// (the pre-fix behavior) raced that flush and truncated it — reproduced
	// by bypassing ctxloom entirely and hitting the identical symptom
	// against the real binary, so this is a teardown-timing bug, not a
	// ctxloom capture bug. Zero (every production construction path) means
	// DefaultShutdownGrace; test-only seam to keep tests fast.
	shutdownGrace time.Duration
}

// DefaultShutdownGrace is how long a spawned ACP agent gets to exit on its
// own after stdin closes before teardown force-kills its process group. The
// container transport applies the same bound to the container's teardown
// (isolation.AttachedContainer.ShutdownGrace, whose own default matches this
// one). Long
// enough to cover an async native-transcript flush (tidy-gush's manual
// repro: "sleep a few seconds" before terminate let the flush complete
// cleanly every time); short enough that a hung/misbehaving agent doesn't
// meaningfully delay teardown.
const DefaultShutdownGrace = 3 * time.Second

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
		agent.NewBaseContextProvider(),
		// SessionHistory: ACP exposes conversation state over the live
		// session/update stream, not a client-side transcript, and this
		// increment persists none — so acp DECLARES it has none rather than
		// answering an empty list nobody can tell from "genuinely none".
		// grpc/sessionhistory.go and operations.HistoryForBackend already turn
		// nil into "backend acp has no session history".
		nil,
		// acp materializes no files (its loadout rides the ACP protocol, see
		// doc.go's "surface-delivery seam" section), so it delivers an EMPTY
		// surface set: Setup still runs the lifecycle merge that populates
		// ManagedChatMCPServers, but writes nothing.
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
		// Never silently: with no ACPConfig applied this backend keeps an empty
		// command AND an empty BinaryPath, and the mis-wiring only surfaces a
		// whole session later as a spawn of "" — a symptom that names neither
		// the cause nor the config that caused it.
		warnf("acp: ignoring a %q config this backend cannot read (want *acp.ACPConfig) — the acp backend is left unconfigured and cannot spawn an agent", cfg.BackendType())
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
	if c.ReasoningConfigKey != "" {
		b.reasoningConfigKey = c.ReasoningConfigKey
	}
	if c.ReasoningEffort != "" {
		b.reasoningEffort = c.ReasoningEffort
	}
	if c.ReasoningSummaryConfigKey != "" {
		b.reasoningSummaryConfigKey = c.ReasoningSummaryConfigKey
	}
	if c.ReasoningSummary != "" {
		b.reasoningSummary = c.ReasoningSummary
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
	// The reasoning overrides render independently of the model one above —
	// they are their own `-c key=value` pair(s), not mutually exclusive with
	// --model/-c model=. Always quoted for the same reason as the model's
	// codex-acp form (its own TOML parser reads the value). Only when both a
	// key AND a resolved value are set — never `-c key=""`.
	if b.reasoningConfigKey != "" && b.reasoningEffort != "" {
		args = append(args, "-c", fmt.Sprintf("%s=%q", b.reasoningConfigKey, b.reasoningEffort))
	}
	if b.reasoningSummaryConfigKey != "" && b.reasoningSummary != "" {
		args = append(args, "-c", fmt.Sprintf("%s=%q", b.reasoningSummaryConfigKey, b.reasoningSummary))
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
	// stderrTail reads the bounded tail of what the spawned agent wrote to
	// STDERR. This is the driver's only channel for a death that happens
	// BELOW the protocol: an adapter that dies in its module loader, or is
	// killed by the OOM killer, never writes a JSON-RPC frame at all, so
	// every error this driver can otherwise report ("acp: connection
	// closed", "write |1: file already closed") names the symptom and not
	// one byte of the cause. The 2026-07-24 incident was exactly that shape
	// and took 49 minutes.
	//
	// Nil for a test transport that spawns no process (engineStderrTail
	// tolerates it).
	stderrTail func() string
}

// engineStderrTail reads tr's captured stderr tail, tolerating a transport
// that captures none. Only ever called while reporting a failure, so it must
// never itself be a failure.
func engineStderrTail(tr *transport) string {
	if tr == nil || tr.stderrTail == nil {
		return ""
	}
	return tr.stderrTail()
}

// withEngineStderr wraps err with the engine's dying words when there are
// any. It is deliberately a WRAP (%w preserved) so errors.As/Is on the
// underlying transport or *jsonrpc.Error keeps working — callers that match
// on the protocol error (authRequiredErr's -32000 rewrite, the coordinator's
// own classification) must not be broken by a diagnostic annotation.
//
// A nil error is returned unchanged: a healthy conversation whose engine
// happened to chatter on stderr is not a failure and must not be reported as
// one.
func withEngineStderr(err error, tr *transport) error {
	if err == nil {
		return nil
	}
	tail := engineStderrTail(tr)
	if tail == "" {
		return err
	}
	return fmt.Errorf("%w (engine stderr tail: %s)", err, tail)
}

// transportRequest is one ACP conversation's launch: the argv, the env overlay,
// the working directory, and the runtime axis that chooses host or container
// (agent.ChatRequest.Runtime — "" = host, agent.RuntimeContainer = container).
// The axis travels WITH the launch rather than on *ACP, so which transport runs
// is a property of the call and not of what a previous argv build left behind.
type transportRequest struct {
	argv    []string
	env     map[string]string
	workDir string
	runtime string
}

type transportFunc func(ctx context.Context, req transportRequest) (*transport, error)

// spawnTransport launches the real `<agent> acp` process with piped stdio, on
// the HOST or inside a CONTAINER per the launch's runtime axis. This is the ACP
// client driver's transport seam (ISO1): JSON-RPC is transport-agnostic, so
// routing here is the ONLY place that changes — the driver, the protocol
// mapping, and everything above this function are untouched regardless of which
// branch runs.
func (b *ACP) spawnTransport(ctx context.Context, req transportRequest) (*transport, error) {
	if req.runtime == agent.RuntimeContainer {
		return b.containerTransport(ctx, req.argv, req.env, req.workDir)
	}
	return b.spawnHostTransport(ctx, req.argv, req.env, req.workDir)
}

// spawnHostTransport is spawnTransport's unchanged pre-ISO1 body: the real
// `<agent> acp` process with piped stdio (no pty). stderr passes through for
// diagnostics, matching the claude chat driver.
func (b *ACP) spawnHostTransport(ctx context.Context, argv []string, env map[string]string, workDir string) (*transport, error) {
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
	stdin, stdout, ring, err := startHostStdio(cmd)
	if err != nil {
		return nil, err
	}

	// waitErr/processDone let close() below observe the natural exit without
	// racing a second cmd.Wait() call (only ever one caller of Wait — Go's
	// os/exec forbids more than one) and without blocking teardown
	// indefinitely on an agent that never reacts to stdin closing.
	var waitErr error
	processDone := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(processDone)
	}()

	grace := b.shutdownGrace
	if grace <= 0 {
		grace = DefaultShutdownGrace
	}

	return &transport{
		stdin:      stdin,
		stdout:     stdout,
		stderrTail: ring.Tail,
		close: func() error {
			_ = stdin.Close()
			if cmd.Process != nil {
				// tidy-gush: give the agent a bounded grace period to exit ON
				// ITS OWN after stdin EOF — long enough to cover an async
				// native-transcript flush — before force-killing. Killing the
				// instant stdin closes (the pre-fix behavior) raced exactly
				// that flush.
				select {
				case <-processDone:
					// exited on its own within the grace period.
				case <-time.After(grace):
					// Kill the WHOLE process group (not just cmd.Process) —
					// this is the moral-scorn fix: a plain Process.Kill here
					// left codex-acp's detached worker running indefinitely
					// after every Chat().
					_ = killProcessGroup(cmd)
					<-processDone
					// waitErr here is OUR signal, not the engine's own verdict
					// on its run — reporting it as an exit status would blame
					// the engine for a kill this process chose. What is worth
					// reporting is the fact it had to be killed.
					return fmt.Errorf("engine did not exit within %s of stdin close and its process group was killed", grace)
				}
			}
			return waitErr
		},
	}, nil
}

// startHostStdio wires cmd's stdio for a JSON-RPC conversation and starts it:
// piped stdin/stdout, plus a stderr TEE — rather than replace — because stderr
// passthrough is an existing operator diagnostic (and, inside a container, the
// run process's only log), so capture is ADDITIVE. Before that tee the adapter's
// stderr was INHERITED only, which is why a containerized child's dying words
// reached the container's stdout and nowhere the coordinator could ever read
// them, with the container force-removed moments later.
//
// It is a seam so the failure paths — which cannot be provoked through
// spawnHostTransport, whose Cmd it builds itself — are exercisable.
func startHostStdio(cmd *exec.Cmd) (io.WriteCloser, io.Reader, *stderrtail.Ring, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		// Both ends of the stdin pipe are this side's to release: os/exec closes
		// the parent ends of the pipes it created only from inside a Start that
		// failed, and this Cmd is abandoned before Start ever runs. StdinPipe
		// left the child's read end on cmd.Stdin, hence the second close.
		_ = stdin.Close()
		if child, ok := cmd.Stdin.(io.Closer); ok {
			_ = child.Close()
		}
		return nil, nil, nil, err
	}
	// The adapter's stderr is forwarded UP as a run-terminal reason (into the
	// child's transcript and its parent's mailbox), so it is deliberately a
	// bounded tail and not a stream: unbounded engine stderr in a transcript
	// is its own problem.
	ring, sink := stderrtail.TeeStderr(stderrtail.DefaultBytes)
	cmd.Stderr = sink
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	return stdin, stdout, ring, nil
}

// spawnEnv builds the spawned agent's environment: the inherited base minus
// the configured StripEnv variables, with the per-launch overlay taking
// priority for any key it names. Stripping happens on the BASE only: an
// overlay entry always lands, even for a stripped key — the caller set it
// deliberately (including forcing it EMPTY, the internal-invocation scrub's
// own shape — see agent.ScrubInternalIdentityEnv).
//
// Overlay entries replace their base counterpart in place rather than being
// appended after it: a naive append left two envp entries for the same key
// when the process already inherited it (exactly CTXLOOM_SESSION_HARP's
// case — every session sets it ambiently), and os/exec does NOT dedupe on
// exec — the underlying C runtime's getenv on a duplicate key is
// implementation-defined (typically FIRST match, not last), so a caller
// relying on "the overlay wins" could silently get the base value back. A
// key present ONLY in the overlay is appended, in a SORTED tail so two calls
// with the same inputs produce byte-identical output regardless of Go's
// randomized map iteration order.
func spawnEnv(base, strip []string, overlay map[string]string) []string {
	seen := make(map[string]bool, len(overlay))
	out := make([]string, 0, len(base)+len(overlay))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if slices.Contains(strip, k) {
			continue
		}
		if v, ok := overlay[k]; ok {
			out = append(out, k+"="+v)
			seen[k] = true
			continue
		}
		out = append(out, kv)
	}
	extra := make([]string, 0, len(overlay))
	for k := range overlay {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		out = append(out, k+"="+overlay[k])
	}
	return out
}

// warnf routes a diagnostic through ctxloom's standard "ctxloom: warning:" path.
func warnf(format string, args ...any) { agent.Warn(format, args...) }
