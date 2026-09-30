package spawn

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// A child's launch source is its binding and the caller's orchestration
// traits; its permissions come from its own binding, never its caller.
func TestChildSource_IsTheBinding(t *testing.T) {
	plan := &coord.SpawnPlan{AgentName: "worker", Snapshot: &config.Snapshot{Config: config.NewFixture(config.Fixture{})}}
	start := coord.SpawnStart{Identity: sessions.Identity{Harp: "h", Depth: 2}, Prompt: "p"}
	src := childSource(plan, start, "/proj")
	assert.Equal(t, launch.WorkspaceWorktree, src.Workspace, "a child defaults to its own worktree")
	assert.Equal(t, "worker", src.Agent)
	assert.Empty(t, src.Permission, "no caller sets a child's permissions")
}
