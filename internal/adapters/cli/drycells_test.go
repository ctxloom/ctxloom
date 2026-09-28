package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// TestDryCells_SessionHomeIsTheOneRule: the preview cell advises exactly the
// session home launch.SessionHome names, for an engine that relocates its
// home and for one that relocates nothing, in both home modes — so a
// preview routes the session-home kinds where the real run puts them.
func TestDryCells_SessionHomeIsTheOneRule(t *testing.T) {
	sessionDir := t.TempDir()
	for _, name := range []engine.Name{"claude-code", "mock"} {
		eng, ok := engines.Registry().Lookup(name)
		require.True(t, ok, "%s is registered", name)
		for _, mode := range []launch.HomeMode{launch.HomeModeSession, launch.HomeModeHost} {
			cell, err := dryCells{}.Prepare(context.Background(), launch.CellRequest{Engine: eng, HomeMode: mode, ProjectRoot: t.TempDir(), SessionDir: sessionDir})
			require.NoError(t, err)
			want, _ := launch.SessionHome(sessionDir, eng, agents.HomeMode(mode))
			require.Equal(t, want, cell.Paths.Paths().SessionHome.Host, "%s, engine_home %s", name, mode)
		}
	}
}
