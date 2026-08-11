package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func TestMock_History(t *testing.T) {
	backend := NewMock()
	history := backend.History()
	// Mock returns a NilSessionHistory (stub that returns empty/nil for all methods)
	assert.NotNil(t, history)
}

// TestEnforcesReadOnlyPlan pins which backends map PermissionPlan to a genuine
// read-only, non-prompting mode. claude-code (--permission-mode plan), codex
// (--sandbox read-only --ask-for-approval never), and kiro (--trust-tools=
// fs_read, LIVE VERIFIED 2026-07-15) do; acp only distinguishes bypass.
func TestEnforcesReadOnlyPlan(t *testing.T) {
	assert.True(t, EnforcesReadOnlyPlan("claude-code"), "claude enforces read-only plan")
	assert.True(t, EnforcesReadOnlyPlan("codex"), "codex enforces read-only plan")
	assert.True(t, EnforcesReadOnlyPlan("kiro"), "kiro --trust-tools=fs_read is a live-verified genuine read-only posture")
	assert.False(t, EnforcesReadOnlyPlan("acp"), "acp only distinguishes bypass")
	assert.False(t, EnforcesReadOnlyPlan("unknown"), "unregistered backend cannot enforce anything")
}

// TestACPTransportFor pins every registered backend's single ACP-transport
// declaration (agent.ACPTransport): claude-code/codex need a SEPARATE
// third-party adapter binary (ACPAdapter, with the exact Binary/InstallCmd
// literal each engine's own chat.go const carries, plus Zed Industries
// provenance derived from the adapter's own npm scope — see registry.go's
// claudeACPTransport/codexACPTransport docs); kiro/opencode speak ACP
// natively (ACPNative, the zero value, no adapter fields); an unregistered
// name reports the zero value (ACPNative, everything else empty) — "needs
// nothing" is the safe default.
func TestACPTransportFor(t *testing.T) {
	claudeT := ACPTransportFor("claude-code")
	assert.Equal(t, agent.ACPAdapter, claudeT.Kind, "claude-code has no native ACP mode")
	assert.Equal(t, "claude-code-acp", claudeT.Binary)
	assert.Equal(t, "npm install -g @zed-industries/claude-code-acp", claudeT.InstallCmd)
	assert.Equal(t, "Zed Industries", claudeT.Publisher)
	assert.Equal(t, "https://github.com/zed-industries/claude-code-acp", claudeT.SourceRepo)

	codexT := ACPTransportFor("codex")
	assert.Equal(t, agent.ACPAdapter, codexT.Kind, "codex has no native ACP mode")
	assert.Equal(t, "codex-acp", codexT.Binary)
	assert.Equal(t, "npm install -g @zed-industries/codex-acp", codexT.InstallCmd)
	assert.Equal(t, "Zed Industries", codexT.Publisher)
	assert.Equal(t, "https://github.com/zed-industries/codex-acp", codexT.SourceRepo)

	kiroT := ACPTransportFor("kiro")
	assert.Equal(t, agent.ACPNative, kiroT.Kind, "kiro-cli acp is native")
	assert.Empty(t, kiroT.Binary, "no separate adapter binary for a native engine")
	assert.Empty(t, kiroT.InstallCmd)

	opencodeT := ACPTransportFor("opencode")
	assert.Equal(t, agent.ACPNative, opencodeT.Kind, "opencode acp is native")
	assert.Empty(t, opencodeT.Binary)

	assert.Equal(t, agent.ACPTransport{}, ACPTransportFor("unknown"), "unregistered backend reports the zero value")
}
