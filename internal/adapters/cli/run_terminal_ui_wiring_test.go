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
	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/livecoord"
)

// The terminal UI's wiring onto a LIVE coordinating session, pinned end to
// end: what the overlay's roster, the surround bar's roster, the control
// verbs and the approval queue show and do when the session hosts a real
// coordinator holding real children. The overlay and the bar each have unit
// tests over fakes; these characterize the seam between them and the
// coordinator.

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
	assert.Zero(t, r.Depth, "a parent the roster does not list leaves the child at the root")

	p := rowOf(t, rows, paused)
	require.NotNil(t, p.Hold)
	assert.Equal(t, coord.HoldKindHuman, p.Hold.Kind)
}

func TestTerminalUI_SurroundRosterOverALiveSession(t *testing.T) {
	s, _, idle, paused := liveTerminalUI(t)
	rows := barRoster(t, s)
	require.Len(t, rows, 2, "the bar lists the coordinator's runs: %+v", rows)
	byHarp := map[string]termui.RosterEntry{}
	for _, r := range rows {
		byHarp[r.Harp] = r
	}
	assert.Equal(t, coord.StateIdle, byHarp[idle].State)
	assert.Positive(t, byHarp[idle].LastActivityUnix, "the bar knows when each child last moved")
	assert.Empty(t, byHarp[idle].Hold)
	assert.Equal(t, tui.HoldLabel(&coord.RunHold{Kind: coord.HoldKindHuman}), byHarp[paused].Hold, "a pause is worded as the overlay words it")
	assert.True(t, rows[0].Harp < rows[1].Harp, "the bar's rows are in harp order")
}

func TestTerminalUI_ControlAndApprovalsOverALiveSession(t *testing.T) {
	s, projectDir, idle, _ := liveTerminalUI(t)
	control, approvals := terminalUIActions(t, s, projectDir)

	_, err := control(context.Background(), coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: idle, Body: "steered from the overlay"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return slices.ContainsFunc(s.Prompts(idle), func(p string) bool { return strings.Contains(p, "steered from the overlay") })
	}, liveWait, pollEvery, "the human's steer reaches the child as a turn")
	// A request is withdrawn when the turn that asked it ends, so ask only
	// once the steered turn is over.
	s.Settle(t, idle, 2)

	require.NotNil(t, approvals, "a hosted coordinator gives the overlay its approval queue")
	s.Ask(t, idle, "Bash")
	require.Eventually(t, func() bool {
		return slices.ContainsFunc(approvals.Pending(), func(p coord.PendingApproval) bool { return p.From.Harp == idle })
	}, liveWait, pollEvery, "a child's request is what the overlay's queue lists")
}
