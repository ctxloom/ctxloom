package cli

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport/livecoord"
)

const (
	liveWait  = 30 * time.Second
	pollEvery = 10 * time.Millisecond
)

// overlayRoster is the overlay's roster as the CLI wires it.
func overlayRoster(t *testing.T, s *livecoord.Session, projectDir string) []tui.RosterRow {
	t.Helper()
	rows, err := terminalUISources(s.Coord, projectDir, livecoord.OwnerHarp).Roster(context.Background())
	require.NoError(t, err)
	return rows
}

// barRoster is the surround bar's roster as the CLI wires it.
func barRoster(t *testing.T, s *livecoord.Session) []termui.RosterEntry {
	t.Helper()
	rows, err := surroundRoster(s.Coord)
	require.NoError(t, err)
	return rows
}

// terminalUIActions are the overlay's control verb and approval queue as the
// CLI wires them.
func terminalUIActions(t *testing.T, s *livecoord.Session, projectDir string) (func(context.Context, coord.ControlRequest) (coord.ControlResult, error), coord.ApprovalSource) {
	t.Helper()
	src := terminalUISources(s.Coord, projectDir, livecoord.OwnerHarp)
	return src.Control, src.Approvals
}
