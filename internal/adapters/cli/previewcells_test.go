package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// TestPreviewCells_SessionHomeIsTheOneRule: the --dry-run cell advises
// exactly the session home launch.SessionHome names, for an engine that
// relocates its home and for one that relocates nothing, in both home modes
// — so a preview routes the session-home kinds where the real run puts them
// — and creates none of it.
func TestPreviewCells_SessionHomeIsTheOneRule(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "sessions", "ugly-icy-squid")
	cells := operations.PreviewCells(operations.LaunchFacts{Engines: engines.Registry()})
	for _, name := range []engine.Name{"claude-code", "mock"} {
		eng, ok := engines.Registry().Lookup(name)
		require.True(t, ok, "%s is registered", name)
		for _, mode := range []launch.HomeMode{launch.HomeModeSession, launch.HomeModeHost} {
			cell, err := cells.Prepare(context.Background(), launch.CellRequest{
				Axes:     launch.Axes{Workspace: launch.WorkspaceNone, Runtime: launch.RuntimeHost},
				Engine:   eng,
				Identity: sessions.Identity{Harp: "ugly-icy-squid"},
				HomeMode: mode, ProjectRoot: t.TempDir(), SessionDir: sessionDir,
			})
			require.NoError(t, err)
			want, _ := launch.SessionHome(sessionDir, eng, agents.HomeMode(mode))
			require.Equal(t, want, cell.Paths.Paths().SessionHome.Host, "%s, engine_home %s", name, mode)
		}
	}
	require.NoDirExists(t, sessionDir, "a preview creates nothing on disk")
}
