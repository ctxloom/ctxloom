package delivery_test

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// planEngines is every engine shape the placement core must plan for: both
// shipped definitions and the mock with a dynamic approach.
func planEngines(t *testing.T) map[string]engine.Base {
	t.Helper()
	c, err := claude.Build()
	require.NoError(t, err)
	return map[string]engine.Base{
		"claude":       c.Root(),
		"mock":         mock.New().Root(),
		"mock-dynamic": mock.New(mock.WithDynamic()).Root(),
	}
}

// fullPackage holds an item of every kind.
func fullPackage(t *testing.T) composite.Package {
	t.Helper()
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"), compositetest.WithCommand("go", "go now"), compositetest.WithSkill("greet"), compositetest.WithMCP("tasks", wire.MCPServer{Command: "tasks"}))
	pkg.Hooks.Unified.PreTool = []wire.Hook{{Type: "command", Command: "true"}}
	pkg.Statusline = true
	return pkg
}

// TestPlanFor_ReproducesProjectPlanAtTheProjectRoot (materialize test 52,
// characterization): over a project-only cell, nil roots and nil kinds,
// PlanFor is ProjectPlan, for every engine shape; with kinds, the entries
// and losses outside them are dropped.
func TestPlanFor_ReproducesProjectPlanAtTheProjectRoot(t *testing.T) {
	pkg := fullPackage(t)
	paths := present.ProjectOnHost("/p").Paths()
	for name, root := range planEngines(t) {
		t.Run(name, func(t *testing.T) {
			items := pkg.EngineItems(root.Name)
			want, err := delivery.ProjectPlan(root, items, "/p")
			require.NoError(t, err)
			got, err := delivery.PlanFor(root, items, paths, nil, nil, false)
			require.NoError(t, err)
			require.Equal(t, want, got)

			scoped, err := delivery.PlanFor(root, items, paths, nil, []present.Kind{present.Context, present.Skills}, false)
			require.NoError(t, err)
			var kinds []present.Kind
			for _, it := range scoped.Static {
				kinds = append(kinds, it.Kind)
			}
			require.Equal(t, []present.Kind{present.Context, present.Skills}, kinds)
			for _, l := range scoped.Losses {
				require.Contains(t, []present.Kind{present.Context, present.Skills}, l.Kind)
			}
		})
	}
}

// TestPlanFor_AcceptsTheLossOfEveryUncarriedKind: a kind the Definition
// does not carry is an accepted loss, never a refusal — the rule ProjectPlan
// and the launch preference both stated — and a kinds filter drops a loss
// outside it.
func TestPlanFor_AcceptsTheLossOfEveryUncarriedKind(t *testing.T) {
	lossy := mock.New(mock.Without(present.Hooks)).Root()
	pkg := fullPackage(t)
	plan, err := delivery.PlanFor(lossy, pkg.EngineItems(lossy.Name), present.ProjectOnHost("/p").Paths(), nil, nil, false)
	require.NoError(t, err)
	require.Equal(t, []delivery.Loss{{Kind: present.Hooks}}, plan.Losses)
	plan, err = delivery.PlanFor(lossy, pkg.EngineItems(lossy.Name), present.ProjectOnHost("/p").Paths(), nil, []present.Kind{present.Context}, false)
	require.NoError(t, err)
	require.Empty(t, plan.Losses)
}

// TestPlanFor_PlansTheSessionEndpointOnlyWhereOneIsServed: ctxloom's own MCP
// endpoint is session-scoped (owner constraint, 2026-10-07). A placement
// that serves no endpoint (at rest) plans no MCP for an engine whose only
// MCP is that endpoint; one with declared servers still plans them; a
// session placement keeps today's plan.
func TestPlanFor_PlansTheSessionEndpointOnlyWhereOneIsServed(t *testing.T) {
	dynamic := mock.New(mock.WithDynamic()).Root()
	bare := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"))
	bare.MCP = map[string]wire.MCPServer{"ctxloom": {ServedBy: wire.ServedBySessionEndpoint}}
	paths := present.ProjectOnHost("/p").Paths()

	atRest, err := delivery.PlanFor(dynamic, bare.EngineItems(dynamic.Name), paths, nil, nil, false)
	require.NoError(t, err)
	for _, it := range atRest.Static {
		require.NotEqual(t, present.MCP, it.Kind, "nothing but the session endpoint asks for MCP, and at rest there is none")
	}

	live, err := delivery.PlanFor(dynamic, bare.EngineItems(dynamic.Name), paths, nil, nil, true)
	require.NoError(t, err)
	require.Contains(t, staticKinds(live), present.MCP, "a session placement plans its endpoint")

	declared := fullPackage(t)
	withServers, err := delivery.PlanFor(dynamic, declared.EngineItems(dynamic.Name), paths, nil, nil, false)
	require.NoError(t, err)
	require.Contains(t, staticKinds(withServers), present.MCP, "declared servers are planned at rest")
}

func staticKinds(p delivery.Plan) []present.Kind {
	var out []present.Kind
	for _, it := range p.Static {
		out = append(out, it.Kind)
	}
	return out
}

// TestTargetFor_IsTheOneTargetConstructor (materialize test 54): a session's
// target is today's Launch.Target literal; a project family with kinds
// speaks for each kind under its own tag.
func TestTargetFor_IsTheOneTargetConstructor(t *testing.T) {
	rec := newRecord(t, afero.NewMemMapFs())
	cell := present.New(present.OnHost(present.Paths{SessionHome: present.Root{Host: "/h", Engine: "/h"}, ProjectRoot: present.Root{Host: "/p", Engine: "/p"}}))
	require.Equal(t,
		delivery.Target{Root: cell, Ownership: rec, Writer: delivery.SessionWriter("h")},
		delivery.TargetFor(cell, rec, delivery.SessionWriter("h"), nil))

	fam := delivery.ProjectWriterFor("mock")
	project := delivery.TargetFor(present.ProjectOnHost("/p"), rec, fam, delivery.AllKinds())
	require.Equal(t, delivery.ProjectTarget("/p", rec).Root, project.Root)
	require.Len(t, project.Writers(), 6)
	require.Equal(t, fam.Of(present.MCP), project.Writers()[1])
}
