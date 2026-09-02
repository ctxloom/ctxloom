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
// read-only, non-prompting mode. claude-code does, via --permission-mode plan.
//
// The mock row is the one that bites: it is REGISTERED and still must report
// false, so the predicate cannot degrade into "is this backend known?" — which
// would hand every future backend a read-only guarantee it never implemented.
func TestEnforcesReadOnlyPlan(t *testing.T) {
	assert.True(t, EnforcesReadOnlyPlan("claude-code"), "claude enforces read-only plan")
	assert.False(t, EnforcesReadOnlyPlan("mock"), "a registered backend that does not enforce it must not claim it")
	assert.False(t, EnforcesReadOnlyPlan("unknown"), "unregistered backend cannot enforce anything")
}
