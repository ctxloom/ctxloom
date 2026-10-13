package launch_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// A tool-less one-shot (marauding-magnolia, decision #5) is not plan: on an
// engine that implements engine.ToolLess it resolves with no posture and no
// approver, so none of the plan/approver gates is reached — even on an
// engine with no approval codec, where the same one-shot asked at plan is
// refused.
func TestResolve_ToolLessOnAMarkedEngineIsNotPlan(t *testing.T) {
	env := launchtest.Deps(t, launchtest.ToolLess(), launchtest.NoApprovals())
	src := launch.Source{Internal: true, Label: "fast", Mode: engine.Structured, ToolLess: true}
	l, _ := resolved(t, env, src)

	assert.True(t, l.ToolLess, "the launch says it is tool-less, so the runner binds it through the marker")
	assert.Empty(t, l.Permission.Posture.Document, "a tool-less launch carries no posture document")
	assert.Equal(t, launchtest.EngineName, l.Permission.Posture.Engine)
	assert.Equal(t, engine.ApproverNone, l.Permission.Approver, "nothing it can do needs approving")
	assert.False(t, l.RoutesApprovals(), "no approval route, and no approval hooks")

	// The gates are untouched: the same one-shot asked at plan on this
	// engine is still refused.
	plan := src
	plan.ToolLess, plan.Permission = false, "plan"
	plan.Identity, plan.WorkDir = env.Identity, env.Project
	_, err := launch.Resolve(context.Background(), env.Deps, plan)
	require.ErrorIs(t, err, launch.ErrPermissionUnhonoured, "a plan one-shot on an approver-less engine is refused as before")
}

// A tool-less launch delivers no MCP server: the package it carries has none,
// whatever the binding's profiles composed.
func TestResolve_ToolLessCarriesNoMCPServer(t *testing.T) {
	env := launchtest.Deps(t, launchtest.ToolLess(), launchtest.WithDynamic(), launchtest.AssembledMCP("companion"))
	l, _ := resolved(t, env, launch.Source{Agent: "distiller", Mode: engine.Structured, ToolLess: true})
	require.NotEmpty(t, env.Assembled().MCP, "the binding's profiles did compose a server")

	pkg, err := launch.Open(context.Background(), env.Deps, l)
	require.NoError(t, err)
	assert.Empty(t, pkg.MCP, "the carried package attaches no MCP server")
	assert.NotContains(t, kindsOf(l.Plan), present.MCP, "no MCP surface is planned, not even the session endpoint's entry")

	// Control: the same binding not tool-less carries it.
	ctl := launchtest.Deps(t, launchtest.ToolLess(), launchtest.WithDynamic(), launchtest.AssembledMCP("companion"))
	withMCP, _ := resolved(t, ctl, launch.Source{Agent: "distiller", Mode: engine.Structured, Permission: "plan"})
	assert.False(t, withMCP.ToolLess)
	assert.Contains(t, kindsOf(withMCP.Plan), present.MCP)
	ctlPkg, err := launch.Open(context.Background(), ctl.Deps, withMCP)
	require.NoError(t, err)
	assert.Contains(t, ctlPkg.MCP, "companion")
}

// kindsOf are the kinds a plan routes.
func kindsOf(p delivery.Plan) []present.Kind {
	var out []present.Kind
	for _, it := range p.Static {
		out = append(out, it.Kind)
	}
	return out
}

// On an engine that does not implement the marker a tool-less launch is
// refused, naming the engine: nothing would make its run tool-less.
func TestResolve_ToolLessOnAnUnmarkedEngineIsRefused(t *testing.T) {
	env := launchtest.Deps(t)
	src := launch.Source{Internal: true, Label: "fast", Mode: engine.Structured, ToolLess: true, Identity: env.Identity, WorkDir: env.Project}
	_, err := launch.Resolve(context.Background(), env.Deps, src)
	require.ErrorIs(t, err, launch.ErrToolLessUnsupported)
	assert.Contains(t, err.Error(), string(launchtest.EngineName))
}

// A tool-less source naming a posture contradicts itself, and is refused
// rather than one half silently winning.
func TestResolve_ToolLessTakesNoPosture(t *testing.T) {
	env := launchtest.Deps(t, launchtest.ToolLess())
	src := launch.Source{Internal: true, Label: "fast", Mode: engine.Structured, ToolLess: true, Permission: "plan", Identity: env.Identity, WorkDir: env.Project}
	_, err := launch.Resolve(context.Background(), env.Deps, src)
	require.ErrorIs(t, err, launch.ErrToolLessPosture)
}

// Launch.Instance binds a tool-less launch through the engine's marker, and
// refuses one whose engine does not implement it; a launch that is not
// tool-less binds through Instance as ever.
func TestLaunch_InstanceBindsAToolLessLaunchThroughTheMarker(t *testing.T) {
	env := launchtest.Deps(t, launchtest.ToolLess())
	marked, ok := env.Deps.Engines.Lookup(launchtest.EngineName)
	require.True(t, ok)
	l := launch.Launch{Engine: launchtest.EngineName, ToolLess: true, Prompt: "p"}

	inst, err := l.Instance(marked)
	require.NoError(t, err)
	got, ok := inst.(launchtest.ToolLessInstance)
	require.True(t, ok, "the marker's own instance, not Instance's")
	assert.Equal(t, "p", got.Session.Prompt, "bound to the launch's session")

	unmarked, ok := launchtest.Deps(t).Deps.Engines.Lookup(launchtest.EngineName)
	require.True(t, ok)
	_, err = l.Instance(unmarked)
	require.ErrorIs(t, err, launch.ErrToolLessUnsupported)

	l.ToolLess = false
	_, err = l.Instance(marked)
	var unsupported engine.ErrUnsupported
	require.True(t, errors.As(err, &unsupported), "a launch that is not tool-less binds through Instance (which the fixture refuses)")
	assert.Equal(t, "instance", unsupported.Capability)
}

// A live launch plans an MCP route for its session endpoint's entry even
// with no server declared; a tool-less one plans none — its endpoint is not
// attached to the engine as an MCP server.
func TestResolve_ToolLessPlansNoEndpointEntry(t *testing.T) {
	for name, tc := range map[string]struct {
		src     launch.Source
		wantMCP bool
	}{
		"tool-less": {launch.Source{Internal: true, Label: "fast", Mode: engine.Structured, ToolLess: true}, false},
		"plan":      {launch.Source{Internal: true, Label: "fast", Mode: engine.Structured, Permission: "plan"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			l, _ := resolved(t, launchtest.Deps(t, launchtest.ToolLess(), launchtest.WithDynamic()), tc.src)
			assert.Equal(t, tc.wantMCP, slices.Contains(kindsOf(l.Plan), present.MCP))
		})
	}
}
