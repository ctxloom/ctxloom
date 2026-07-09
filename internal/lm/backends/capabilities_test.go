package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMock_History(t *testing.T) {
	backend := NewMock()
	history := backend.History()
	// Mock returns a NilSessionHistory (stub that returns empty/nil for all methods)
	assert.NotNil(t, history)
}

// TestEnforcesReadOnlyPlan pins which backends map PermissionPlan to a genuine
// read-only, non-prompting mode. Only claude-code (--permission-mode plan) and
// codex (--sandbox read-only --ask-for-approval never) do; the rest have no
// read-only tier, so the run resolver must not treat plan as honored for them.
func TestEnforcesReadOnlyPlan(t *testing.T) {
	assert.True(t, EnforcesReadOnlyPlan("claude-code"), "claude enforces read-only plan")
	assert.True(t, EnforcesReadOnlyPlan("codex"), "codex enforces read-only plan")
	assert.False(t, EnforcesReadOnlyPlan("antigravity"), "antigravity has no read-only tier")
	assert.False(t, EnforcesReadOnlyPlan("kiro"), "kiro has no read-only tier")
	assert.False(t, EnforcesReadOnlyPlan("acp"), "acp only distinguishes bypass")
	assert.False(t, EnforcesReadOnlyPlan("unknown"), "unregistered backend cannot enforce anything")
}
