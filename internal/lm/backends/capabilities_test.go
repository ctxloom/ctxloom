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
// fs_read, LIVE VERIFIED) do; acp only distinguishes bypass.
func TestEnforcesReadOnlyPlan(t *testing.T) {
	assert.True(t, EnforcesReadOnlyPlan("claude-code"), "claude enforces read-only plan")
	assert.True(t, EnforcesReadOnlyPlan("codex"), "codex enforces read-only plan")
	assert.True(t, EnforcesReadOnlyPlan("kiro"), "kiro --trust-tools=fs_read is a live-verified genuine read-only posture")
	assert.False(t, EnforcesReadOnlyPlan("acp"), "acp only distinguishes bypass")
	assert.False(t, EnforcesReadOnlyPlan("unknown"), "unregistered backend cannot enforce anything")
}

