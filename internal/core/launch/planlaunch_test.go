package launch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// TestPlanLaunch_IsTodaysRouteThroughPlanFor (materialize test 53,
// characterization): the launch's plan through delivery.PlanFor is the plan
// the launch built before it — Route under a hand-built Preference (the
// binding's roots, every uncarried kind an accepted loss) — for a binding
// with and without roots:, for an engine with and without a dynamic
// approach and one that carries fewer kinds.
func TestPlanLaunch_IsTodaysRouteThroughPlanFor(t *testing.T) {
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"), compositetest.WithCommand("go", "go now"), compositetest.WithSkill("greet"), compositetest.WithMCP("tasks", wire.MCPServer{Command: "tasks"}))
	pkg.Hooks.Unified.PreTool = []wire.Hook{{Type: "command", Command: "true"}}
	pkg.Statusline = true
	cell := present.Paths{SessionHome: present.Root{Host: "/h", Engine: "/h"}, ProjectRoot: present.Root{Host: "/p", Engine: "/p"}}
	engines := map[string]engine.Base{
		"mock":         mock.New().Root(),
		"mock-dynamic": mock.New(mock.WithDynamic()).Root(),
		"mock-lossy":   mock.New(mock.Without(present.Hooks, present.Skills)).Root(),
	}
	bindings := map[string]map[string]string{
		"no roots":       nil,
		"context rooted": {"context": "project-root", "mcp": "session-home"},
	}
	for en, def := range engines {
		for bn, roots := range bindings {
			t.Run(en+"/"+bn, func(t *testing.T) {
				pref := delivery.Preference{Root: map[present.Kind]present.RootKind{}, AcceptLoss: map[present.Kind]bool{}}
				for name, label := range roots {
					k, _ := present.ParseKind(name)
					r, _ := present.ParseRootKind(label)
					pref.Root[k] = r
				}
				for _, k := range []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills} {
					if !def.Carries(k) {
						pref.AcceptLoss[k] = true
					}
				}
				want, err := delivery.Route(itemsOf(pkg, def.Name), def, pref, cell)
				require.NoError(t, err)
				got, err := planLaunch(def, pkg, roots, cell)
				require.NoError(t, err)
				require.Equal(t, want, got)
			})
		}
	}
}

// TestPlanLaunch_RefusesARootLabelByName: a binding root that no longer
// parses is refused by name, as before.
func TestPlanLaunch_RefusesARootLabelByName(t *testing.T) {
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"))
	_, err := planLaunch(mock.New().Root(), pkg, map[string]string{"context": "attic"}, present.Paths{})
	require.ErrorIs(t, err, ErrBindingRoots)
	_, err = planLaunch(mock.New().Root(), pkg, map[string]string{"widgets": "project-root"}, present.Paths{})
	require.ErrorIs(t, err, ErrBindingRoots)
}
