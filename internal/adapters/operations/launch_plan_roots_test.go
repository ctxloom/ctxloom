package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// TestPreferPlanRoots_ProjectRouteSelectsTheProjectForm: a kind the plan
// roots under the project root (or the work dir) is delivered by the
// engine's project form; a session-home route leaves the engine's default
// alone; a kind the binding already named keeps its name.
func TestPreferPlanRoots_ProjectRouteSelectsTheProjectForm(t *testing.T) {
	managed := &agent.ManagedConfig{Surfaces: map[agent.SurfaceKind]string{agent.SurfaceMCP: "mcp-config"}}
	PreferPlanRoots(managed, delivery.Plan{Static: []delivery.StaticItem{
		{Kind: agent.SurfaceContext, Root: present.RootProjectRoot},
		{Kind: agent.SurfaceSettings, Root: present.RootSessionHome},
		{Kind: agent.SurfaceMCP, Root: present.RootProjectRoot},
		{Kind: agent.SurfaceSkills, Root: present.RootWorkDir},
	}})
	assert.Equal(t, map[agent.SurfaceKind]string{
		agent.SurfaceContext: agent.ApproachUnsafeFile,
		agent.SurfaceMCP:     "mcp-config",
		agent.SurfaceSkills:  agent.ApproachUnsafeFile,
	}, managed.Surfaces)
}

// TestPreferPlanRoots_SessionOnlyPlanSelectsNothing: the default plan —
// every kind under the session home — names no approach, so the engine's
// declared default stands for every kind.
func TestPreferPlanRoots_SessionOnlyPlanSelectsNothing(t *testing.T) {
	managed := &agent.ManagedConfig{}
	PreferPlanRoots(managed, delivery.Plan{Static: []delivery.StaticItem{
		{Kind: agent.SurfaceContext, Root: present.RootSessionHome},
		{Kind: agent.SurfaceCommands, Root: present.RootSessionHome},
	}})
	assert.Empty(t, managed.Surfaces)
	PreferPlanRoots(nil, delivery.Plan{})
}
