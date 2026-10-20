package cli

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
	"github.com/ctxloom/ctxloom/internal/testsupport/livecoord"
)

const (
	liveWait  = 30 * time.Second
	pollEvery = 10 * time.Millisecond
)

// livePort is the live session's frontend port, composed as `ctxloom run`
// composes it.
func livePort(s *livecoord.Session, projectDir string) frontend.Port {
	return (&runState{sessionCoord: s.Coord, workDir: projectDir, activeHarp: livecoord.OwnerHarp}).frontendPort()
}

// overlayRoster is the split view's roster as the CLI wires it.
func overlayRoster(t *testing.T, s *livecoord.Session, projectDir string) []tui.RosterRow {
	t.Helper()
	rows, err := tui.LoadRoster(context.Background(), livePort(s, projectDir), livecoord.OwnerHarp)
	require.NoError(t, err)
	return rows
}

// terminalUIActions are the split view's control verb and approval queue as the
// CLI wires them.
func terminalUIActions(t *testing.T, s *livecoord.Session, projectDir string) (func(context.Context, coord.ControlRequest) (coord.ControlResult, error), coord.ApprovalSource) {
	t.Helper()
	port := livePort(s, projectDir)
	return port.Control, port.Approvals()
}
