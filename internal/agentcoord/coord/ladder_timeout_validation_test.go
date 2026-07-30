package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// U023-F09: buildLadder REJECTS a role: on an auto_accept/auto_decline rung
// ("role is not meaningful for action") but silently DROPPED a timeout: on the
// same rungs. An operator writing
//
//	escalation:
//	  - action: auto_accept
//	    timeout: 5m
//
// is describing something the ladder cannot do — auto_accept resolves the
// request immediately, there is nothing to wait 5 minutes for — and got no
// error, no warning, and a ladder that does not match the config they wrote.
// A meaningless field is rejected the same way whichever one it is.
func TestBuildLadder_RejectsTimeoutOnImmediateActions(t *testing.T) {
	for _, action := range []LadderAction{ActionAutoAccept, ActionAutoDecline} {
		_, err := buildLadder("worker", []agents.EscalationRung{
			{Action: string(action), Timeout: "5m"},
		}, agent.PermissionBypass)
		if !assert.Error(t, err, "a timeout on %q must be refused, not silently dropped: nothing waits", action) {
			continue
		}
		assert.Contains(t, err.Error(), "timeout",
			"the error must name the offending field so the operator can find it")
		assert.Contains(t, err.Error(), string(action))
	}
}

// TestBuildLadder_ImmediateActionsWithoutTimeoutStillBuild keeps the tightened
// validation from rejecting the ordinary case.
func TestBuildLadder_ImmediateActionsWithoutTimeoutStillBuild(t *testing.T) {
	l, err := buildLadder("worker", []agents.EscalationRung{
		{Action: "auto_decline", Kinds: []string{"FILE_CHANGE"}},
		{Action: "auto_accept"},
	}, agent.PermissionBypass)
	require.NoError(t, err)
	require.Len(t, l, 2)
	assert.Zero(t, l[0].Timeout, "an immediate rung carries no timeout")
	assert.Zero(t, l[1].Timeout)
}
