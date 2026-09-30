package spawn

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// The launching run's ceiling rides the child's launch as its cap; a child
// of the root session carries none.
func TestChildSource_CarriesTheParentCeiling(t *testing.T) {
	plan := &coord.SpawnPlan{AgentName: "worker", ParentCeiling: engine.PermissionAcceptEdits, Snapshot: &config.Snapshot{Config: config.NewFixture(config.Fixture{})}}
	start := coord.SpawnStart{Identity: sessions.Identity{Harp: "h", Depth: 2}, Prompt: "p"}
	src := childSource(plan, start, "/proj")
	assert.Equal(t, engine.PermissionAcceptEdits, src.ParentCeiling)
	assert.Equal(t, launch.WorkspaceWorktree, src.Workspace, "a child defaults to its own worktree")
	assert.Equal(t, "worker", src.Agent)

	plan.ParentCeiling = engine.PermissionNotRequested
	assert.Equal(t, engine.PermissionNotRequested, childSource(plan, start, "/proj").ParentCeiling)
}
