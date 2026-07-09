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
