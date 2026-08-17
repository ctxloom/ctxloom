package acp

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestNewChatDriver_TargetDelegation pins the embedding contract the target
// backends (kiro/codex/claude) rely on: the config's Command resolves the
// binary + leading args, and the agent/engine/model knobs land in the argv.
func TestNewChatDriver_TargetDelegation(t *testing.T) {
	drv := NewChatDriver(ACPConfig{
		Command:     "kiro-cli acp",
		Agent:       "ctxloom",
		AgentEngine: "v3",
	})
	assert.Equal(t, "kiro-cli", drv.BinaryPath, "Command's first field becomes the binary")

	argv := drv.chatArgv(agent.ChatRequest{Model: "sonnet"})
	assert.Equal(t, []string{"acp", "--agent", "ctxloom", "--model", "sonnet", "--agent-engine", "v3"}, argv)
}

// TestNewChatDriver_AdapterBinary: a bare adapter command (claude-code-acp,
// codex-acp) yields the binary with no leading args and a clean argv.
func TestNewChatDriver_AdapterBinary(t *testing.T) {
	drv := NewChatDriver(ACPConfig{Command: "claude-code-acp"})
	assert.Equal(t, "claude-code-acp", drv.BinaryPath)
	assert.Empty(t, drv.chatArgv(agent.ChatRequest{}), "no knobs set → no flags")
}

// TestACPSupportedModes: the generic backend is oneshot-only (no TUI).
func TestACPSupportedModes(t *testing.T) {
	b := NewACP()
	assert.Equal(t, []agent.ExecutionMode{agent.ModeOneshot}, b.SupportedModes())
}

// TestSpawnEnv_StripAndOverlay pins the spawned agent's env assembly: the
// config's StripEnv variables are REMOVED from the inherited base (the fix
// for claude's CLAUDECODE nested-session guard leaking down the delegated-
// child process tree), while the per-launch overlay always lands — even for
// a stripped key, since the caller set it deliberately.
func TestSpawnEnv_StripAndOverlay(t *testing.T) {
	base := []string{"CLAUDECODE=1", "PATH=/usr/bin", "TERM=xterm"}

	env := spawnEnv(base, []string{"CLAUDECODE"}, map[string]string{"CTXLOOM_SESSION_HARP": "h"})
	assert.NotContains(t, env, "CLAUDECODE=1", "stripped from the inherited base")
	assert.Contains(t, env, "PATH=/usr/bin")
	assert.Contains(t, env, "TERM=xterm")
	assert.Contains(t, env, "CTXLOOM_SESSION_HARP=h", "overlay applies")

	assert.Equal(t, base, spawnEnv(base, nil, nil), "no strip, no overlay → base unchanged")
	assert.Contains(t, spawnEnv(base, []string{"CLAUDECODE"}, map[string]string{"CLAUDECODE": "1"}),
		"CLAUDECODE=1", "a deliberate overlay wins over its own strip")
}

// TestSpawnEnv_OverlayReplacesAmbientDuplicate pins the fix for a live defect
// (exposable-rental unit 2): an overlay key ALREADY present in the inherited
// base used to be appended a second time rather than replacing it, leaving
// two envp entries for the same key — os/exec does not dedupe on exec, and
// the C runtime's getenv on a duplicate is implementation-defined (typically
// first match, not last), so a caller trusting "the overlay wins" (the
// internal-invocation harp scrub, ScrubInternalIdentityEnv) could silently
// still hand the child the AMBIENT value.
func TestSpawnEnv_OverlayReplacesAmbientDuplicate(t *testing.T) {
	base := []string{"CTXLOOM_SESSION_HARP=real-harp", "PATH=/usr/bin"}
	env := spawnEnv(base, nil, map[string]string{"CTXLOOM_SESSION_HARP": ""})

	assert.Contains(t, env, "CTXLOOM_SESSION_HARP=", "overlay's empty value reaches the spawn")
	assert.NotContains(t, env, "CTXLOOM_SESSION_HARP=real-harp", "the ambient value must not survive alongside it")

	count := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "CTXLOOM_SESSION_HARP=") {
			count++
		}
	}
	assert.Equal(t, 1, count, "exactly one entry for the key — never a duplicate")
}

// TestConfigure_StripEnv pins that StripEnv survives Configure onto the driver.
func TestConfigure_StripEnv(t *testing.T) {
	drv := NewChatDriver(ACPConfig{Command: "claude-code-acp", StripEnv: []string{"CLAUDECODE"}})
	assert.Equal(t, []string{"CLAUDECODE"}, drv.stripEnv)
}

// TestConfigure_ClaudeAgentEngineStripsCLAUDECODE pins item 2: a GENERIC acp
// entry whose agent_engine names claude (`type: acp`, `command:
// "claude-code-acp"`, `agent_engine: claude`) drives the same claude engine
// claude-code's own backend does, and inherits CLAUDECODE from the parent
// claude's process tree exactly like it — but before this fix, only the
// claude-code backend's OWN chatACPConfig strip applied; a user-configured
// generic acp entry was left unstripped and died under a claude parent.
func TestConfigure_ClaudeAgentEngineStripsCLAUDECODE(t *testing.T) {
	drv := NewChatDriver(ACPConfig{Command: "claude-code-acp", AgentEngine: "claude"})
	assert.Contains(t, drv.stripEnv, "CLAUDECODE")

	// Case-insensitive, and a user-configured strip_env is preserved alongside it.
	drv2 := NewChatDriver(ACPConfig{Command: "claude-code-acp", AgentEngine: "Claude", StripEnv: []string{"FOO"}})
	assert.ElementsMatch(t, []string{"FOO", "CLAUDECODE"}, drv2.stripEnv)

	// A non-claude engine is untouched: only claude has this guard.
	drv3 := NewChatDriver(ACPConfig{Command: "kiro-cli acp", AgentEngine: "kiro"})
	assert.Empty(t, drv3.stripEnv)

	// Re-configuring doesn't duplicate the strip entry.
	drv.Configure(&ACPConfig{AgentEngine: "claude"})
	assert.Equal(t, []string{"CLAUDECODE"}, drv.stripEnv, "no duplicate strip entries across repeated Configure calls")
}

// TestChat_ModelEnvVar pins the model's ENV delivery (ACPConfig.ModelEnvVar):
// when the embedding backend names the engine's native model env var (claude:
// ANTHROPIC_MODEL), the requested model rides the spawned adapter's env —
// load-bearing because claude-code-acp 0.16.2 silently ignores the `--model`
// argv and would otherwise run the session on the user's saved interactive
// default (the -32603 failure the resolveChatModel gate exists to prevent).
func TestChat_ModelEnvVar(t *testing.T) {
	b := NewChatDriver(ACPConfig{Command: "claude-code-acp", ModelEnvVar: "ANTHROPIC_MODEL"})
	gotEnv := make(chan map[string]string, 1)
	b.openTransport = func(_ context.Context, req transportRequest) (*transport, error) {
		gotEnv <- req.env
		return nil, io.ErrUnexpectedEOF // spawn "fails": env capture is all this test needs
	}
	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent, 1)
	callerEnv := map[string]string{"FOO": "bar"}
	err := b.Chat(context.Background(), agent.ChatRequest{Model: "claude-sonnet-5", Env: callerEnv}, in, out)
	require.Error(t, err)
	env := <-gotEnv
	assert.Equal(t, "claude-sonnet-5", env["ANTHROPIC_MODEL"], "the requested model rides the adapter env")
	assert.Equal(t, "bar", env["FOO"], "the caller's env survives the overlay")
	assert.NotContains(t, callerEnv, "ANTHROPIC_MODEL", "the caller's map is copied, never mutated")

	// No ModelEnvVar configured → env passes through untouched. (Fresh
	// channels: Chat closes out on return, per the StructuredChat contract.)
	b2 := NewChatDriver(ACPConfig{Command: "kiro-cli acp"})
	gotEnv2 := make(chan map[string]string, 1)
	b2.openTransport = func(_ context.Context, req transportRequest) (*transport, error) {
		gotEnv2 <- req.env
		return nil, io.ErrUnexpectedEOF
	}
	_ = b2.Chat(context.Background(), agent.ChatRequest{Model: "some-model", Env: map[string]string{"FOO": "bar"}},
		make(chan agent.ChatMessage), make(chan agent.ChatEvent, 1))
	assert.NotContains(t, <-gotEnv2, "ANTHROPIC_MODEL")
}

// TestConfigure_ClaudeAgentEngineDefaultsModelEnvVar pins the sibling of
// the CLAUDECODE strip (TestConfigure_ClaudeAgentEngineStripsCLAUDECODE): a
// generic acp entry driving claude (agent_engine: claude) also needs
// ANTHROPIC_MODEL delivery for the same reason claude-code's own backend
// carries it (claude-code-acp silently ignores --model argv) —
// mauve-plop item 2's sibling.
func TestConfigure_ClaudeAgentEngineDefaultsModelEnvVar(t *testing.T) {
	drv := NewChatDriver(ACPConfig{Command: "claude-code-acp", AgentEngine: "claude"})
	assert.Equal(t, "ANTHROPIC_MODEL", drv.modelEnvVar)

	// Case-insensitive, mirroring the strip's own casing rule.
	drv2 := NewChatDriver(ACPConfig{Command: "claude-code-acp", AgentEngine: "Claude"})
	assert.Equal(t, "ANTHROPIC_MODEL", drv2.modelEnvVar)

	// An explicit model_env_var in the entry always wins over the default.
	drv3 := NewChatDriver(ACPConfig{Command: "claude-code-acp", AgentEngine: "claude", ModelEnvVar: "CUSTOM_MODEL"})
	assert.Equal(t, "CUSTOM_MODEL", drv3.modelEnvVar)

	// A non-claude engine gets no default: only claude has this shape.
	drv4 := NewChatDriver(ACPConfig{Command: "kiro-cli acp", AgentEngine: "kiro"})
	assert.Empty(t, drv4.modelEnvVar)
}

// TestChatArgv_ModelConfigKey pins the codex-acp finding: an adapter
// that has NO --model flag (codex-acp 0.16.0 rejects it outright, verified
// live — exit 2, "unexpected argument") delivers the model through a
// `-c key=value` config-override flag instead, quoted so the adapter's own
// TOML parser reads it as a string (codex-acp README: `-c model="o3"`,
// verified live). ModelConfigKey and the plain --model flag are mutually
// exclusive — an adapter configured with one never sees the other.
func TestChatArgv_ModelConfigKey(t *testing.T) {
	drv := NewChatDriver(ACPConfig{Command: "codex-acp", ModelConfigKey: "model"})
	argv := drv.chatArgv(agent.ChatRequest{Model: "o3"})
	assert.Equal(t, []string{"-c", `model="o3"`}, argv)
	assert.NotContains(t, argv, "--model", "ModelConfigKey suppresses the generic --model flag")

	// No model requested → no flag at all (never `-c model=""`, which would
	// override codex's own configured default with an empty string).
	drv2 := NewChatDriver(ACPConfig{Command: "codex-acp", ModelConfigKey: "model"})
	assert.Empty(t, drv2.chatArgv(agent.ChatRequest{}))

	// A backend with no ModelConfigKey is unaffected — plain --model, as before.
	drv3 := NewChatDriver(ACPConfig{Command: "kiro-cli acp"})
	assert.Equal(t, []string{"acp", "--model", "sonnet"}, drv3.chatArgv(agent.ChatRequest{Model: "sonnet"}))
}

// TestChatArgv_ReasoningConfigKey pins the normalized thinking-level
// delivery for codex-acp: an ADDITIONAL `-c key=value` pair, independent of
// (and combinable with) ModelConfigKey — both render together when both are
// configured, and neither is per-request (no ChatRequest field drives this;
// the embedding backend already resolved the concrete value at Configure
// time — see internal/codex/chat.go).
func TestChatArgv_ReasoningConfigKey(t *testing.T) {
	drv := NewChatDriver(ACPConfig{
		Command:            "codex-acp",
		ModelConfigKey:     "model",
		ReasoningConfigKey: "model_reasoning_effort",
		ReasoningEffort:    "medium",
	})
	argv := drv.chatArgv(agent.ChatRequest{Model: "o3"})
	assert.Equal(t, []string{"-c", `model="o3"`, "-c", `model_reasoning_effort="medium"`}, argv)

	// Both reasoning overrides render together, in order, when both are set.
	drv2 := NewChatDriver(ACPConfig{
		Command:                   "codex-acp",
		ReasoningConfigKey:        "model_reasoning_effort",
		ReasoningEffort:           "xhigh",
		ReasoningSummaryConfigKey: "model_reasoning_summary",
		ReasoningSummary:          "auto",
	})
	assert.Equal(t, []string{
		"-c", `model_reasoning_effort="xhigh"`,
		"-c", `model_reasoning_summary="auto"`,
	}, drv2.chatArgv(agent.ChatRequest{}))

	// Neither key configured -> no reasoning flags at all. A bare adapter
	// command (no trailing subcommand, like "codex-acp" itself) so an empty
	// chatArgv result actually means "no flags" rather than surfacing
	// Command's own leading args (cf. TestNewChatDriver_AdapterBinary).
	drv3 := NewChatDriver(ACPConfig{Command: "codex-acp"})
	assert.Empty(t, drv3.chatArgv(agent.ChatRequest{}))

	// A key with no resolved value never renders `-c key=""`.
	drv4 := NewChatDriver(ACPConfig{Command: "codex-acp", ReasoningConfigKey: "model_reasoning_effort"})
	assert.Empty(t, drv4.chatArgv(agent.ChatRequest{}))
}

// wrongConfig is any other backend's config, standing in for a mis-wired
// registry entry handing this backend a config it cannot read.
type wrongConfig struct{}

func (wrongConfig) BackendType() string { return "not-acp" }

// TestConfigure_WrongConfigTypeWarns pins the fail-loud on a config type this
// backend cannot read. Returning silently leaves the driver with no command and
// no BinaryPath, and the failure then surfaces a whole session later as an
// unexplained empty-binary spawn — naming the type here is the difference
// between one line and a debugging session.
func TestConfigure_WrongConfigTypeWarns(t *testing.T) {
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	b := NewACP()
	b.Configure(wrongConfig{})

	assert.Empty(t, b.BinaryPath, "an unreadable config configures nothing — that part is unchanged")
	assert.Contains(t, warnings.String(), "acp", "the warning names the backend")
	assert.Contains(t, warnings.String(), "not-acp", "and the config type it was handed")
}

// TestStartHostStdio_ReleasesPipesWhenStdoutWiringFails: the pipes belong to
// THIS side until the process starts. os/exec releases the parent ends of the
// pipes it created only from inside a Start that failed, and Start never runs on
// this path — so an abandoned Cmd must have both ends of the stdin pipe it
// already made released here, or every attempt burns two file descriptors until
// the finalizer happens to run. The realistic trigger is os.Pipe failing under
// fd exhaustion, precisely when leaking two more is least affordable.
func TestStartHostStdio_ReleasesPipesWhenStdoutWiringFails(t *testing.T) {
	const attempts = 8

	before := openFDCount(t)
	for range attempts {
		cmd := exec.Command("true")
		cmd.Stdout = io.Discard // makes StdoutPipe fail deterministically

		stdin, stdout, ring, err := startHostStdio(cmd)
		require.Error(t, err, "stdout wiring must fail for this test to mean anything")
		assert.Nil(t, stdin, "no half-wired transport is handed back")
		assert.Nil(t, stdout)
		assert.Nil(t, ring)
	}
	after := openFDCount(t)

	// A margin of 2 absorbs unrelated fd churn in the test process without
	// absorbing the leak itself, which is 2 descriptors PER attempt.
	assert.LessOrEqual(t, after, before+2,
		"%d abandoned wirings must not accumulate descriptors (grew by %d)", attempts, after-before)
}

// TestStartHostStdio_StartFailureIsOSExecsCleanup pins the guarantee this file
// relies on rather than duplicating: when Start itself fails, os/exec closes the
// parent ends of every pipe it created (Cmd.Start's deferred
// closeDescriptors(parentIOPipes) on the !started path). If a future toolchain
// stopped doing that, this fails and the cleanup belongs here instead.
func TestStartHostStdio_StartFailureIsOSExecsCleanup(t *testing.T) {
	const attempts = 8
	missing := filepath.Join(t.TempDir(), "no-such-acp-binary")

	before := openFDCount(t)
	for range attempts {
		_, _, _, err := startHostStdio(exec.Command(missing))
		require.Error(t, err, "the binary does not exist, so Start must fail")
	}
	after := openFDCount(t)

	assert.LessOrEqual(t, after, before+2,
		"os/exec must release the pipes of a Cmd that failed to start (grew by %d)", after-before)
}

// openFDCount counts this process's open file descriptors. Linux-only
// (/proc/self/fd); the fd-accounting assertions skip elsewhere.
func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd on this platform: %v", err)
	}
	return len(entries)
}
