package fsstatic_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/delivery/deliverytest"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// kindsFixture is the mock's every-kind package and its project-root plan
// over project, with the exports the plan's delivery needs.
func kindsFixture(t *testing.T, project string, contextBody string) (composite.Package, delivery.Plan, engine.Exports, engine.Base) {
	t.Helper()
	eng := mock.New()
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", contextBody), compositetest.WithCommand("go", "go now"), compositetest.WithSkill("greet"), compositetest.WithMCP("tasks", wire.MCPServer{Command: "tasks"}))
	pkg.Hooks.Unified.PreTool = []wire.Hook{{Type: "command", Command: "true"}}
	pkg.Statusline = true
	items := pkg.EngineItems(eng.Root().Name)
	exports, err := eng.Exports(items)
	require.NoError(t, err)
	pref := delivery.Preference{Root: map[present.Kind]present.RootKind{}}
	for _, k := range eng.Root().Static() {
		pref.Root[k] = present.RootProjectRoot
	}
	plan, err := delivery.Route(items, eng.Root(), pref, present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}})
	require.NoError(t, err)
	require.Len(t, plan.Static, 6)
	return pkg, plan, exports, eng.Root()
}

// only keeps the plan's items of the given kinds.
func only(plan delivery.Plan, kinds ...present.Kind) delivery.Plan {
	out := delivery.Plan{Static: []delivery.StaticItem{}}
	for _, it := range plan.Static {
		if slices.Contains(kinds, it.Kind) {
			out.Static = append(out.Static, it)
		}
	}
	return out
}

// TestDeliver_ATargetWithKindsRefusesAPlanItemOutsideThem (materialize test
// 6): a target speaks for exactly its kinds; an item of another kind would
// land under a writer the target never releases.
func TestDeliver_ATargetWithKindsRefusesAPlanItemOutsideThem(t *testing.T) {
	fs := afero.NewMemMapFs()
	project := t.TempDir()
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	pkg, plan, exports, root := kindsFixture(t, project, "hello")
	target := delivery.Target{Root: present.ProjectOnHost(project), Ownership: rec, Writer: delivery.ProjectWriterFor(root.Name), Kinds: []present.Kind{present.Context}}
	_, err = fsstatic.New(safefs.NewMem(fs)).Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg, Exports: exports}, root, target)
	require.ErrorIs(t, err, fsstatic.ErrKindNotTargeted)
	require.Empty(t, deliverytest.RelativeFiles(fs, project), "a refused delivery writes nothing")
}

// TestDeliver_PerKindWritersLeaveUnselectedKindsStanding: a full delivery
// under per-kind writers, then a context-only one with changed content,
// leaves every other kind claimed and on disk; a per-kind empty plan
// releases only that kind.
func TestDeliver_PerKindWritersLeaveUnselectedKindsStanding(t *testing.T) {
	fs := afero.NewMemMapFs()
	project := t.TempDir()
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	static := fsstatic.New(safefs.NewMem(fs))
	pkg, plan, exports, root := kindsFixture(t, project, "first")
	fam := delivery.ProjectWriterFor(root.Name)
	full := delivery.Target{Root: present.ProjectOnHost(project), Ownership: rec, Writer: fam, Kinds: delivery.AllKinds()}
	_, err = static.Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg, Exports: exports}, root, full)
	require.NoError(t, err)
	before := deliverytest.RelativeFiles(fs, project)
	require.Contains(t, before, ".mock/skills/greet/SKILL.md")

	pkg2, plan2, exports2, _ := kindsFixture(t, project, "second")
	ctxOnly := full
	ctxOnly.Kinds = []present.Kind{present.Context}
	_, err = static.Deliver(context.Background(), delivery.Loadout{Plan: only(plan2, present.Context), Package: pkg2, Exports: exports2}, root, ctxOnly)
	require.NoError(t, err)
	require.Equal(t, before, deliverytest.RelativeFiles(fs, project), "the context-only run left every other kind's files")
	for _, k := range []present.Kind{present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills} {
		targets, err := rec.Targets(fam.Of(k))
		require.NoError(t, err)
		require.NotEmpty(t, targets, "%v is still claimed", k)
	}
	body, err := afero.ReadFile(fs, filepath.Join(project, mock.ContextFileName))
	require.NoError(t, err)
	require.Contains(t, string(body), "second")
	require.NotContains(t, string(body), "first")

	skillsOnly := full
	skillsOnly.Kinds = []present.Kind{present.Skills}
	_, err = static.Deliver(context.Background(), delivery.Loadout{Package: pkg}, root, skillsOnly)
	require.NoError(t, err)
	after := deliverytest.RelativeFiles(fs, project)
	require.NotContains(t, after, ".mock/skills/greet/SKILL.md", "the skills release removed the skill")
	require.Contains(t, after, ".mock/commands/go.md", "and nothing else")
	require.Contains(t, after, mock.ContextFileName)
}
