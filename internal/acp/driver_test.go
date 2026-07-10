package acp

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
	b.openTransport = func(_ context.Context, _ []string, env map[string]string, _ string) (*transport, error) {
		gotEnv <- env
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
	b2.openTransport = func(_ context.Context, _ []string, env map[string]string, _ string) (*transport, error) {
		gotEnv2 <- env
		return nil, io.ErrUnexpectedEOF
	}
	_ = b2.Chat(context.Background(), agent.ChatRequest{Model: "some-model", Env: map[string]string{"FOO": "bar"}},
		make(chan agent.ChatMessage), make(chan agent.ChatEvent, 1))
	assert.NotContains(t, <-gotEnv2, "ANTHROPIC_MODEL")
}
