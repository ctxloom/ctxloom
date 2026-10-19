package cli

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/livecoord"
)

// parkOn parks a Bash ask on c's queue as a child of the root would, with a
// timeout far past the test's own wait: a parked request would hang the test.
func parkOn(c *coord.Coordinator) coord.ApprovalDecision {
	return c.Approvals().Park(context.Background(), coord.Identity{Harp: "child-harp", RunID: "run-1", Depth: 1},
		coord.PendingApproval{Ask: engine.PermissionAsk{Tool: "Bash", ToolUseID: "toolu_1"}}, 10*time.Minute)
}

// TestPrepareSessionIO_NoTerminalLayerRefusesApprovals: an interactive root
// whose terminal layer never engages (no tty here) has no presenter, so a
// child's approval request is refused at once with the reason, rather than
// parked for 15 minutes nobody can answer.
func TestPrepareSessionIO_NoTerminalLayerRefusesApprovals(t *testing.T) {
	home := testsupport.Isolate(t)
	projectDir := home + "/proj"
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	s := livecoord.New(t, projectDir, 0)
	prev := termIsTerminal
	termIsTerminal = func(int) bool { return false }
	t.Cleanup(func() { termIsTerminal = prev })

	st := &runState{ctx: context.Background(), sessionCoord: s.Coord, workDir: projectDir, activeHarp: livecoord.OwnerHarp}
	st.prepareSessionIO(st.frontendPort()).restore()

	d := parkOn(s.Coord)
	assert.False(t, d.Allow)
	assert.Equal(t, agent.DeciderRefused, d.Decider)
	assert.Equal(t, unattendedNoTerminalLayer, d.Message)
}

// TestMarkUnattended_NoCoordinatorIsANoop: a session that hosts no
// coordinator has no queue, and marking it changes nothing.
func TestMarkUnattended_NoCoordinatorIsANoop(t *testing.T) {
	assert.NotPanics(t, func() { markUnattended(nil, unattendedOneShot) })
}
