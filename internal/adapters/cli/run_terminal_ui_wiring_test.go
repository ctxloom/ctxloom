package cli

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/livecoord"
)

// The terminal UI's wiring onto a LIVE coordinating session, pinned end to
// end: what the split view's roster, the control verbs and the approval
// queue show and do when the session hosts a real coordinator holding real
// children. The split view has unit tests over fakes; these characterize the
// seam between it and the coordinator.

// liveTerminalUI is a live session with two children: one idle, one the
// human paused.
func liveTerminalUI(t *testing.T) (s *livecoord.Session, projectDir, idle, paused string) {
	t.Helper()
	home := testsupport.Isolate(t)
	projectDir = home + "/proj"
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	s = livecoord.New(t, projectDir, 0)
	idle = s.Delegate(t, "stay idle")
	paused = s.Delegate(t, "be paused")
	_, err := s.Coord.Control(context.Background(), coord.ControlInitiator{Kind: coord.InitiatorHuman}, coord.ControlRequest{Verb: coord.ControlVerbPause, Harp: paused, Body: "looking"})
	require.NoError(t, err)
	return s, projectDir, idle, paused
}

func rowOf(t *testing.T, rows []tui.RosterRow, harp string) tui.RosterRow {
	t.Helper()
	i := slices.IndexFunc(rows, func(r tui.RosterRow) bool { return r.Harp == harp })
	require.GreaterOrEqual(t, i, 0, "no roster row for %s in %+v", harp, rows)
	return rows[i]
}

func TestTerminalUI_OverlayRosterOverALiveSession(t *testing.T) {
	s, projectDir, idle, paused := liveTerminalUI(t)
	rows := overlayRoster(t, s, projectDir)

	r := rowOf(t, rows, idle)
	assert.Equal(t, "worker", r.Agent, "the coordinator's agent enriches the index row")
	assert.Equal(t, livecoord.Engine, r.Engine, "the index row's engine is kept")
	assert.Equal(t, livecoord.OwnerHarp, r.Parent)
	assert.Equal(t, coord.StateIdle, r.State, "the coordinator's state wins over the lock's")
	assert.Nil(t, r.Hold)
	assert.Equal(t, 1, r.Depth, "under the session's own row, which the roster makes when the index does not list it")
	assert.Equal(t, livecoord.OwnerHarp, rows[0].Harp, "the session's own row first")

	p := rowOf(t, rows, paused)
	require.NotNil(t, p.Hold)
	assert.Equal(t, coord.HoldKindHuman, p.Hold.Kind)
}

func TestTerminalUI_ControlAndApprovalsOverALiveSession(t *testing.T) {
	s, projectDir, idle, _ := liveTerminalUI(t)
	control, approvals := terminalUIActions(t, s, projectDir)

	_, err := control(context.Background(), coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: idle, Body: "steered from the split view"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return slices.ContainsFunc(s.Prompts(idle), func(p string) bool { return strings.Contains(p, "steered from the split view") })
	}, liveWait, pollEvery, "the human's steer reaches the child as a turn")
	// A request is withdrawn when the turn that asked it ends, so ask only
	// once the steered turn is over.
	s.Settle(t, idle, 2)

	require.NotNil(t, approvals, "a hosted coordinator gives the split view its approval queue")
	s.Ask(t, idle, "Bash")
	require.Eventually(t, func() bool {
		return slices.ContainsFunc(approvals.Pending(), func(p coord.PendingApproval) bool { return p.From.Harp == idle })
	}, liveWait, pollEvery, "a child's request is what the split view's queue lists")
}
