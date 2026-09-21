package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// TestPreferPlanRoots_ProjectRouteSelectsTheProjectForm: a kind the plan
// roots under the project root is delivered by the engine's project form;
// a session-home route leaves the engine's default alone; a kind the
// binding already named keeps its name.
func TestPreferPlanRoots_ProjectRouteSelectsTheProjectForm(t *testing.T) {
	managed := &ManagedConfig{Surfaces: map[SurfaceKind]string{SurfaceMCP: "mcp-config"}}
	PreferPlanRoots(managed, delivery.Plan{Static: []delivery.StaticItem{
		{Kind: SurfaceContext, Root: present.RootProjectRoot},
		{Kind: SurfaceSettings, Root: present.RootSessionHome},
		{Kind: SurfaceMCP, Root: present.RootProjectRoot},
		{Kind: SurfaceSkills, Root: present.RootWorkDir},
	}})
	assert.Equal(t, map[SurfaceKind]string{
		SurfaceContext: ApproachUnsafeFile,
		SurfaceMCP:     "mcp-config",
		SurfaceSkills:  ApproachUnsafeFile,
	}, managed.Surfaces)
}

// TestPreferPlanRoots_SessionOnlyPlanSelectsNothing: the default plan —
// every kind under the session home — names no approach, so the engine's
// declared default stands for every kind.
func TestPreferPlanRoots_SessionOnlyPlanSelectsNothing(t *testing.T) {
	managed := &ManagedConfig{}
	PreferPlanRoots(managed, delivery.Plan{Static: []delivery.StaticItem{
		{Kind: SurfaceContext, Root: present.RootSessionHome},
		{Kind: SurfaceCommands, Root: present.RootSessionHome},
	}})
	assert.Empty(t, managed.Surfaces)
	PreferPlanRoots(nil, delivery.Plan{})
}
