package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// TestCellKindOf_FollowsTheCell: the writers' cell kind is a projection of
// the cell, decided nowhere else.
func TestCellKindOf_FollowsTheCell(t *testing.T) {
	shared := launch.Cell{Placement: launch.Placement{Paths: present.OnHost(present.Paths{ProjectRoot: present.Root{Host: "/proj"}})}, Workspace: "/proj"}
	require.Equal(t, agent.CellKindShared, cellKindOf(shared))

	apart := shared
	apart.Workspace = "/elsewhere"
	require.Equal(t, agent.CellKindDirectoryIsolated, cellKindOf(apart))

	boxed := shared
	boxed.Container = &launch.ContainerCell{Runtime: launch.RuntimeRootless}
	require.Equal(t, agent.CellKindProcessIsolated, cellKindOf(boxed))
}
