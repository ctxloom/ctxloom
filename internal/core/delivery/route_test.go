package delivery_test

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// fileApproach offers the roots it is given, in order (the first is its
// default).
type fileApproach struct{ roots []present.RootKind }

func (a *fileApproach) Name() string { return "file" }
func (a *fileApproach) Traits() present.Traits {
	return present.Traits{Roots: a.roots, Channel: present.ChannelFile}
}

// root builds an engine root declaring the given kinds through fileApproach,
// with or without a dynamic approach.
func root(t *testing.T, name engine.Name, dynamic bool, roots ...present.RootKind) engine.Base {
	t.Helper()
	a := &ctxApproach{fileApproach{roots}}
	d := engine.Definition{Name: name, Distribution: engine.DistributionDefault, Modes: []engine.Mode{engine.Structured},
		Context: a, MCP: a, Settings: a, Hooks: a, Commands: a, Skills: a,
		CLI: []engine.CLIGrammar{{Mode: engine.Structured, Binary: "x"}}}
	if dynamic {
		d.Dynamic = &dynApproach{fileApproach{roots}}
	}
	b := engine.Base{Definition: d}
	require.NoError(t, b.Validate())
	return b
}

func hostRoots(project, session string) present.Paths {
	return present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}, SessionHome: present.Root{Host: session, Engine: session}}
}

// TestRoute_CarriedKind_LandsAtTheBindingsRoot: arm 1a — the binding selected
// a root the approach offers and the cell has.
func TestRoute_CarriedKind_LandsAtTheBindingsRoot(t *testing.T) {
	r := root(t, "e", false, present.RootSessionHome, present.RootProjectRoot)
	items := engine.Items{Fragments: []engine.FragmentItem{{Ref: "f", Body: []byte("x")}}}
	plan, err := delivery.Route(items, r, delivery.Preference{Root: map[present.Kind]present.RootKind{present.Context: present.RootProjectRoot}}, hostRoots("/p", "/s"))
	require.NoError(t, err)
	require.Len(t, plan.Static, 1) // Context only: no MCP items and no dynamic approach, so Delegate lists nothing else
	require.Equal(t, present.Context, plan.Static[0].Kind)
	require.Equal(t, present.RootProjectRoot, plan.Static[0].Root, "the shared root is a SELECTION, never a fallback")
	require.Equal(t, "file", plan.Static[0].Approach)
	require.Empty(t, plan.Losses)
}

// TestRoute_CarriedKind_FirstOfferedRootTheCellHas: arm 1b — no selection;
// the approach's first offered root the cell has.
func TestRoute_CarriedKind_FirstOfferedRootTheCellHas(t *testing.T) {
	r := root(t, "e", false, present.RootSessionHome, present.RootProjectRoot)
	items := engine.Items{Fragments: []engine.FragmentItem{{Ref: "f", Body: []byte("x")}}}
	plan, err := delivery.Route(items, r, delivery.Preference{}, hostRoots("/p", ""))
	require.NoError(t, err)
	require.Equal(t, present.RootProjectRoot, plan.Static[0].Root, "the session home is absent from this cell, so the next offered root lands")
}

// TestRoute_Refuses_ARootTheApproachDoesNotOffer: reroot is a refusal with
// the remedy naming the binding.
func TestRoute_Refuses_ARootTheApproachDoesNotOffer(t *testing.T) {
	r := root(t, "e", false, present.RootSessionHome)
	items := engine.Items{Fragments: []engine.FragmentItem{{Ref: "f", Body: []byte("x")}}}
	_, err := delivery.Route(items, r, delivery.Preference{Root: map[present.Kind]present.RootKind{present.Context: present.RootProjectRoot}}, hostRoots("/p", "/s"))
	require.ErrorIs(t, err, delivery.ErrUnrootable)
	var u delivery.Unrootable
	require.ErrorAs(t, err, &u)
	require.Equal(t, present.Context, u.Kind)
	require.Equal(t, present.RootProjectRoot, u.Needs)
	require.NotEmpty(t, u.Remedy())
}

// TestRoute_Refuses_ARootTheCellLacks: the approach offers only a root the
// cell does not have.
func TestRoute_Refuses_ARootTheCellLacks(t *testing.T) {
	r := root(t, "e", false, present.RootSessionHome)
	items := engine.Items{Fragments: []engine.FragmentItem{{Ref: "f", Body: []byte("x")}}}
	_, err := delivery.Route(items, r, delivery.Preference{}, hostRoots("/p", ""))
	require.ErrorIs(t, err, delivery.ErrUnrootable)
}

// TestRoute_UncarriedKind_RefusedUnlessTheLossIsAccepted: arm 2.
func TestRoute_UncarriedKind_RefusedUnlessTheLossIsAccepted(t *testing.T) {
	r := root(t, "e", false, present.RootSessionHome)
	r.Hooks = nil
	items := engine.Items{Hooks: []wire.Hook{{}}}
	_, err := delivery.Route(items, r, delivery.Preference{}, hostRoots("/p", "/s"))
	var uncarried delivery.ErrUncarried
	require.ErrorAs(t, err, &uncarried)
	require.Equal(t, engine.Name("e"), uncarried.Engine)
	require.Equal(t, present.Hooks, uncarried.Kind)

	plan, err := delivery.Route(items, r, delivery.Preference{AcceptLoss: map[present.Kind]bool{present.Hooks: true}}, hostRoots("/p", "/s"))
	require.NoError(t, err)
	require.Equal(t, []delivery.Loss{{Kind: present.Hooks}}, plan.Losses)
	for _, s := range plan.Static {
		require.NotEqual(t, present.Hooks, s.Kind, "an accepted loss is routed to NOTHING")
	}
}

// TestRoute_AppliesTheDelegation_NeverRedecides: preface items go to the
// dynamic half when the engine provides one; Route lists them and routes
// the rest statically.
func TestRoute_AppliesTheDelegation_NeverRedecides(t *testing.T) {
	r := root(t, "e", true, present.RootSessionHome)
	items := engine.Items{Fragments: []engine.FragmentItem{{Ref: "always", Body: []byte("x")}, {Ref: "when", Body: []byte("y"), Premise: "p"}}}
	plan, err := delivery.Route(items, r, delivery.Preference{}, hostRoots("/p", "/s"))
	require.NoError(t, err)
	require.Equal(t, []string{"when"}, plan.Dynamic)
	kinds := map[present.Kind]bool{}
	for _, s := range plan.Static {
		kinds[s.Kind] = true
	}
	require.True(t, kinds[present.Context], "the unconditional fragment goes static")
	require.True(t, kinds[present.MCP], "the endpoint rides the MCP file")
}

// ctxApproach and dynApproach make fileApproach satisfy the typed approach
// interfaces the Definition's fields need.
type ctxApproach struct{ fileApproach }

func (a *ctxApproach) Name() string           { return a.fileApproach.Name() }
func (a *ctxApproach) Traits() present.Traits { return a.fileApproach.Traits() }
func (*ctxApproach) DeliverContext(present.Start, present.RootKind, engine.ContextInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (*ctxApproach) DeliverMCP(present.Start, present.RootKind, engine.MCPInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (*ctxApproach) DeliverSettings(present.Start, present.RootKind, engine.SettingsInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (*ctxApproach) DeliverHooks(present.Start, present.RootKind, engine.HooksInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (*ctxApproach) DeliverCommands(present.Start, present.RootKind, engine.CommandsInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (*ctxApproach) DeliverSkills(present.Start, present.RootKind, engine.SkillsInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}

type dynApproach struct{ fileApproach }

func (a *dynApproach) Name() string                               { return "endpoint" }
func (a *dynApproach) Traits() present.Traits                     { return a.fileApproach.Traits() }
func (*dynApproach) Endpoint(ep sessions.Endpoint) wire.MCPServer { return engine.BearerEntry(ep) }
