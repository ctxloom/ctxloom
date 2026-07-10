package acp

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
