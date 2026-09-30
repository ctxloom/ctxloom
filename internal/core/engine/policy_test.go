package engine_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The ranking a ceiling is measured on: every posture, narrowest first. A
// posture is within a ceiling exactly when it sits at or below it here.
var byReach = []engine.PermissionMode{
	engine.PermissionPlan,
	engine.PermissionDontAsk,
	engine.PermissionDefault,
	engine.PermissionAcceptEdits,
	engine.PermissionAuto,
	engine.PermissionBypass,
}

func TestPermissionMode_Within_OrdersEveryPostureByReach(t *testing.T) {
	for i, m := range byReach {
		for j, ceiling := range byReach {
			assert.Equalf(t, i <= j, m.Within(ceiling), "%s within %s", m, ceiling)
		}
	}
}

func TestPermissionMode_Within_NotRequestedIsNoPosture(t *testing.T) {
	for _, m := range byReach {
		assert.Falsef(t, engine.PermissionNotRequested.Within(m), "the zero value is not a posture, so it is within nothing (%s)", m)
		assert.Falsef(t, m.Within(engine.PermissionNotRequested), "nothing is within an unrequested ceiling (%s)", m)
	}
	assert.False(t, engine.PermissionMode(99).Within(engine.PermissionBypass), "an out-of-range value is within nothing")
}

func TestApprover_ZeroValueIsTheHuman(t *testing.T) {
	var a engine.Approver
	assert.Equal(t, engine.ApproverHuman, a, "ruled default: uncovered requests escalate to the root human")
	assert.Equal(t, "human", engine.ApproverHuman.String())
	assert.Equal(t, "none", engine.ApproverNone.String())
}

func TestParseApprover(t *testing.T) {
	for in, want := range map[string]engine.Approver{"human": engine.ApproverHuman, " None ": engine.ApproverNone} {
		got, ok := engine.ParseApprover(in)
		require.Truef(t, ok, "%q", in)
		assert.Equal(t, want, got)
	}
	for _, in := range []string{"", "agent", "humans", "root"} {
		_, ok := engine.ParseApprover(in)
		assert.Falsef(t, ok, "%q is not an approver", in)
	}
	assert.Equal(t, []string{"human", "none", "reviewer"}, engine.ApproverNames())
}

func TestApprovalTimeoutBounds(t *testing.T) {
	assert.Equal(t, 15*time.Minute, engine.DefaultApprovalTimeout)
	assert.Equal(t, 60*time.Minute, engine.MaxApprovalTimeout)
}

func TestWorkspaceTrust_ZeroValueIsUntrusted(t *testing.T) {
	var tr engine.WorkspaceTrust
	assert.Equal(t, engine.TrustUntrusted, tr)
}
