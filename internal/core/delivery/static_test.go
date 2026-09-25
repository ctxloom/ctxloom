package delivery_test

// Part 4.2 test B — the delivery interface. Adapted from the decided
// architecture's body where this module's landed signatures differ from the
// design module's: Route takes engine.Items (pkg.EngineItems), the fixture
// helpers take bodies, the session home is present.Paths.Scratch, and a plan
// is routed over the roots of the target it is delivered to (Route refuses
// a root the cell lacks, so the unrootable case is a plan routed over a
// project root and delivered to a target without one).

import (
	"context"
	"errors"
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
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

var (
	sessionRoots = present.Paths{Scratch: present.Root{Host: "/s/home", Engine: "/s/home"}}
	projectRoots = present.Paths{ProjectRoot: present.Root{Host: "/p", Engine: "/p"}}
	tasks        = wire.MCPServer{Command: "tasks"}
)

func items(pkg composite.Package, eng engine.Engine) engine.Items {
	return pkg.EngineItems(eng.Root().Name)
}

// TestRoute_UncarriedKind_RefusesUnlessAccepted proposes the no-fallback
// rule: a kind the engine registered no approach for is an error naming the
// engine and the kind, and the ONLY way past it is a decision recorded on
// the binding — then the item is routed to nothing and listed as a loss.
func TestRoute_UncarriedKind_RefusesUnlessAccepted(t *testing.T) {
	eng := mock.New(mock.Without(present.Skills))
	pkg := compositetest.Fixture(t, compositetest.WithSkill("greet"))

	_, err := delivery.Route(items(pkg, eng), eng.Root(), delivery.Preference{}, sessionRoots)
	var uncarried delivery.ErrUncarried
	require.True(t, errors.As(err, &uncarried))
	require.Equal(t, delivery.ErrUncarried{Engine: "mock", Kind: present.Skills}, uncarried)

	plan, err := delivery.Route(items(pkg, eng), eng.Root(), delivery.Preference{AcceptLoss: map[present.Kind]bool{present.Skills: true}}, sessionRoots)
	require.NoError(t, err)
	require.Equal(t, []delivery.Loss{{Kind: present.Skills}}, plan.Losses)
	for _, it := range plan.Static {
		require.NotEqual(t, present.Skills, it.Kind, "an accepted loss is routed to nothing")
	}
}

// TestRoute_SelectedRootNotOffered_IsRefused_NeverTheDefault proposes arm
// 1a: a root the binding selects that the approach does not offer is
// refused with the remedy, never replaced by the approach's default.
func TestRoute_SelectedRootNotOffered_IsRefused_NeverTheDefault(t *testing.T) {
	eng := mock.New() // no mock approach offers the work dir
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"))
	_, err := delivery.Route(items(pkg, eng), eng.Root(), delivery.Preference{Root: map[present.Kind]present.RootKind{present.Context: present.RootWorkDir}}, present.Paths{ProjectRoot: present.Root{Host: "/p", Engine: "/p"}, Scratch: present.Root{Host: "/s", Engine: "/s"}})
	require.ErrorIs(t, err, delivery.ErrUnrootable)
	var u delivery.Unrootable
	require.True(t, errors.As(err, &u))
	require.Equal(t, present.RootWorkDir, u.Needs)
}

// TestRoute_MCPIsAlwaysStatic: an MCP item is routed to the engine's
// declared static approach (file or argv channel) at its default root, never
// to the dynamic set.
func TestRoute_MCPIsAlwaysStatic(t *testing.T) {
	eng := mock.New()
	pkg := compositetest.Fixture(t, compositetest.WithMCP("tasks", tasks))
	plan, err := delivery.Route(items(pkg, eng), eng.Root(), delivery.Preference{}, sessionRoots)
	require.NoError(t, err)
	var mcp []delivery.StaticItem
	for _, it := range plan.Static {
		if it.Kind == present.MCP {
			mcp = append(mcp, it)
		}
	}
	require.Len(t, mcp, 1)
	require.Equal(t, "mcp-config", mcp[0].Approach)
	require.Equal(t, present.RootSessionHome, mcp[0].Root, "the default root is the first the approach offers")
	require.Empty(t, plan.Dynamic)
}

func loadoutFor(t *testing.T, eng engine.Engine, roots present.Paths) delivery.Loadout {
	t.Helper()
	p := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"), compositetest.WithCommand("go", "go now"), compositetest.WithMCP("tasks", tasks))
	exports, err := eng.Exports(items(p, eng))
	require.NoError(t, err)
	plan, err := delivery.Route(items(p, eng), eng.Root(), delivery.Preference{}, roots)
	require.NoError(t, err)
	return delivery.Loadout{Plan: plan, Package: p, Exports: exports}
}

// TestStatic_SessionAndMaterialize_ShareWritersAndDifferOnlyInTarget
// proposes materialize as static delivery: the same package delivered under
// the session home and under the project root writes the same relative file
// set, and each writer's entries name exactly those files.
func TestStatic_SessionAndMaterialize_ShareWritersAndDifferOnlyInTarget(t *testing.T) {
	eng := mock.New()
	fs := afero.NewMemMapFs()
	static := fsstatic.New(fs)
	rec := deliverytest.NewOwnership(fs)
	sessionW, projectW := delivery.SessionWriter("harp-1"), delivery.ProjectWriter

	session := delivery.Target{Root: present.New(present.OnHost(sessionRoots)), Ownership: rec, Writer: sessionW}
	project := delivery.Target{Root: present.ProjectOnHost("/p"), Ownership: rec, Writer: projectW}

	d1, err := static.Deliver(context.Background(), loadoutFor(t, eng, sessionRoots), eng.Root(), session)
	require.NoError(t, err)
	lo := loadoutFor(t, eng, projectRoots)
	d2, err := static.Deliver(context.Background(), lo, eng.Root(), project)
	require.NoError(t, err)

	require.Equal(t, d1.Wrote, d2.Wrote)
	require.NotEmpty(t, deliverytest.RelativeFiles(fs, "/s/home"))
	require.Equal(t, deliverytest.RelativeFiles(fs, "/s/home"), deliverytest.RelativeFiles(fs, "/p"))
	require.ElementsMatch(t, rec.AllOwned(sessionW), deliverytest.RelativeFiles(fs, "/s/home"))
	require.ElementsMatch(t, rec.AllOwned(projectW), deliverytest.RelativeFiles(fs, "/p"))

	// Uninstall is delivering the EMPTY plan against the same target.
	empty := delivery.Loadout{Package: lo.Package}
	_, err = static.Deliver(context.Background(), empty, eng.Root(), project)
	require.NoError(t, err)
	require.Empty(t, deliverytest.RelativeFiles(fs, "/p"))
	require.Empty(t, rec.AllOwned(projectW))
	require.NotEmpty(t, deliverytest.RelativeFiles(fs, "/s/home"), "the session's delivery is untouched")
}

// TestStatic_TwoWritersOneTarget_ReconcileRemovesOnlyOwnEntries: a
// session's project-root delivery (the binding selected the shared root) and
// a materialize meet on one project-root file; each writer's
// reconcile-to-empty leaves the other's entries in place.
func TestStatic_TwoWritersOneTarget_ReconcileRemovesOnlyOwnEntries(t *testing.T) {
	eng := mock.New()
	fs := afero.NewMemMapFs()
	static := fsstatic.New(fs)
	rec := deliverytest.NewOwnership(fs)
	pkg := compositetest.Fixture(t, compositetest.WithMCP("tasks", tasks))
	plan, err := delivery.Route(items(pkg, eng), eng.Root(), delivery.Preference{Root: map[present.Kind]present.RootKind{present.MCP: present.RootProjectRoot}}, projectRoots)
	require.NoError(t, err)
	lo := delivery.Loadout{Plan: plan, Package: pkg}

	sessionT := delivery.Target{Root: present.ProjectOnHost("/p"), Ownership: rec, Writer: delivery.SessionWriter("harp-1")}
	projectT := delivery.Target{Root: present.ProjectOnHost("/p"), Ownership: rec, Writer: delivery.ProjectWriter}
	_, err = static.Deliver(context.Background(), lo, eng.Root(), sessionT)
	require.NoError(t, err)
	_, err = static.Deliver(context.Background(), lo, eng.Root(), projectT)
	require.NoError(t, err)

	// The project writer uninstalls; the session's entries survive.
	_, err = static.Deliver(context.Background(), delivery.Loadout{Package: pkg}, eng.Root(), projectT)
	require.NoError(t, err)
	require.Empty(t, rec.AllOwned(delivery.ProjectWriter))
	require.NotEmpty(t, rec.AllOwned(delivery.SessionWriter("harp-1")))
	require.NotEmpty(t, deliverytest.RelativeFiles(fs, "/p"), "the file another writer still owns entries in stays")
}

// TestStatic_ZeroTarget_Refused: the zero Target is refused
// by Deliver, never written under "".
func TestStatic_ZeroTarget_Refused(t *testing.T) {
	eng := mock.New()
	lo := loadoutFor(t, eng, sessionRoots)
	fs := afero.NewMemMapFs()
	_, err := fsstatic.New(fs).Deliver(context.Background(), lo, eng.Root(), delivery.Target{})
	require.ErrorIs(t, err, delivery.ErrNoRoot)
	require.Empty(t, deliverytest.RelativeFiles(fs, "/"))
}

// TestStatic_PreparesTheRecordOnceBeforeAnyWrite: Deliver prepares the
// ownership record first and once, before it reverses or writes anything, so
// the record store is owner-only before a delivery writes through it
// (delivery.Ownership.Prepare).
func TestStatic_PreparesTheRecordOnceBeforeAnyWrite(t *testing.T) {
	eng := mock.New()
	fs := afero.NewMemMapFs()
	static := fsstatic.New(fs)
	rec := deliverytest.NewOwnership(fs)
	target := delivery.Target{Root: present.New(present.OnHost(sessionRoots)), Ownership: rec, Writer: delivery.SessionWriter("harp-1")}
	lo := loadoutFor(t, eng, sessionRoots)

	for range 2 { // the second delivery reverses the first before writing
		_, err := static.Deliver(context.Background(), lo, eng.Root(), target)
		require.NoError(t, err)
	}

	calls := rec.Calls()
	require.Greater(t, len(calls), 2, "the delivery must have written through the record")
	firstApply := slices.Index(calls, deliverytest.CallApply)
	second := slices.Index(calls[1:], deliverytest.CallPrepare) + 1
	require.Equal(t, deliverytest.CallPrepare, calls[0])
	require.Greater(t, second, firstApply, "the first delivery prepares exactly once")
	require.NotContains(t, calls[firstApply:second], deliverytest.CallPrepare)
	require.NotContains(t, calls[second+1:], deliverytest.CallPrepare, "the second delivery prepares exactly once")
	require.Contains(t, calls[second+1:], deliverytest.CallApply)
}

var errPrepare = errors.New("prepare refused")

// refusingPrepare is a record whose storage cannot be made owner-only.
type refusingPrepare struct{ *deliverytest.Ownership }

func (refusingPrepare) Prepare(context.Context) error { return errPrepare }

// TestStatic_PrepareFails_NothingIsWritten: a record that cannot be prepared
// aborts the delivery before anything is reversed or written.
func TestStatic_PrepareFails_NothingIsWritten(t *testing.T) {
	eng := mock.New()
	fs := afero.NewMemMapFs()
	rec := deliverytest.NewOwnership(fs)
	target := delivery.Target{Root: present.New(present.OnHost(sessionRoots)), Ownership: refusingPrepare{rec}, Writer: delivery.SessionWriter("harp-1")}

	_, err := fsstatic.New(fs).Deliver(context.Background(), loadoutFor(t, eng, sessionRoots), eng.Root(), target)
	require.ErrorIs(t, err, errPrepare)
	require.Empty(t, rec.Calls(), "nothing is reversed or recorded")
	require.Empty(t, deliverytest.RelativeFiles(fs, "/"))
}

// TestStatic_UnrootableApproach_RefusesWithRemedy_NeverSubstitutes: an
// item whose planned root the target lacks is refused with the remedy;
// nothing is rerouted to another approach or another root.
func TestStatic_UnrootableApproach_RefusesWithRemedy_NeverSubstitutes(t *testing.T) {
	eng := mock.New()
	pkg := compositetest.Fixture(t, compositetest.WithMCP("tasks", tasks))
	plan, err := delivery.Route(items(pkg, eng), eng.Root(), delivery.Preference{Root: map[present.Kind]present.RootKind{present.MCP: present.RootProjectRoot}}, projectRoots)
	require.NoError(t, err)
	fs := afero.NewMemMapFs()
	noProject := delivery.Target{Root: present.New(present.OnHost(sessionRoots)), Ownership: deliverytest.NewOwnership(fs), Writer: delivery.SessionWriter("h")}
	_, err = fsstatic.New(fs).Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg}, eng.Root(), noProject)
	require.ErrorIs(t, err, delivery.ErrUnrootable)
	var u delivery.Unrootable
	require.True(t, errors.As(err, &u))
	require.Equal(t, "mcp-config", u.Approach)
	require.Equal(t, present.RootProjectRoot, u.Needs)
	require.NotEmpty(t, u.Remedy(), "the refusal names what the human changes")
	require.Empty(t, deliverytest.RelativeFiles(fs, "/s/home"), "nothing was written anywhere else instead")
}

// TestStatic_SharedRootIsASelection_NotAFallback: when the binding selects
// the project root for a kind whose approach offers it and the target has
// one, the project root IS the destination — delivered, owned by the writer,
// refused nowhere.
func TestStatic_SharedRootIsASelection_NotAFallback(t *testing.T) {
	eng := mock.New()
	pkg := compositetest.Fixture(t, compositetest.WithMCP("tasks", tasks))
	plan, err := delivery.Route(items(pkg, eng), eng.Root(), delivery.Preference{Root: map[present.Kind]present.RootKind{present.MCP: present.RootProjectRoot}}, present.Paths{ProjectRoot: present.Root{Host: "/p", Engine: "/p"}, Scratch: present.Root{Host: "/s", Engine: "/s"}})
	require.NoError(t, err)
	require.Equal(t, present.RootProjectRoot, plan.Static[0].Root)
	fs := afero.NewMemMapFs()
	rec := deliverytest.NewOwnership(fs)
	withProject := delivery.Target{Root: present.ProjectOnHost("/p"), Ownership: rec, Writer: delivery.SessionWriter("h")}
	d, err := fsstatic.New(fs).Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg}, eng.Root(), withProject)
	require.NoError(t, err)
	require.Equal(t, []present.Kind{present.MCP}, d.Wrote)
	require.ElementsMatch(t, rec.AllOwned(delivery.SessionWriter("h")), deliverytest.RelativeFiles(fs, "/p"))
}

// TestTarget_Validate_RefusesAnUnrootableTarget: a target needs a root, a
// record and a writer; each absence is ErrNoRoot.
func TestTarget_Validate_RefusesAnUnrootableTarget(t *testing.T) {
	rec := deliverytest.NewOwnership(afero.NewMemMapFs())
	full := delivery.Target{Root: present.ProjectOnHost("/p"), Ownership: rec, Writer: delivery.ProjectWriter}
	require.NoError(t, full.Validate())
	require.NoError(t, delivery.Target{Root: present.New(present.OnHost(sessionRoots)), Ownership: rec, Writer: delivery.SessionWriter("h")}.Validate())

	require.ErrorIs(t, delivery.Target{}.Validate(), delivery.ErrNoRoot)
	require.ErrorIs(t, delivery.Target{Root: present.New(present.OnHost(present.Paths{})), Ownership: rec, Writer: delivery.ProjectWriter}.Validate(), delivery.ErrNoRoot, "no root")
	require.ErrorIs(t, delivery.Target{Root: present.ProjectOnHost("/p"), Writer: delivery.ProjectWriter}.Validate(), delivery.ErrNoRoot, "no record")
	require.ErrorIs(t, delivery.Target{Root: present.ProjectOnHost("/p"), Ownership: rec}.Validate(), delivery.ErrNoRoot, "no writer")
	require.ErrorIs(t, delivery.Target{Root: present.ProjectOnHost("out"), Ownership: rec, Writer: delivery.ProjectWriter}.Validate(), delivery.ErrNoRoot, "a relative root records nothing anyone can find")
}

// TestInputsFor_ProjectsThePackageOnce: every kind's inputs come from the
// decoded package and the exports the loadout carries.
func TestInputsFor_ProjectsThePackageOnce(t *testing.T) {
	eng := mock.New()
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"), compositetest.WithCommand("go", "go now"), compositetest.WithSkill("greet"), compositetest.WithMCP("tasks", tasks))
	pkg.Hooks.Unified.PreTool = []wire.Hook{{Command: "echo pre", Type: "command"}}
	pkg.DenyTools = []string{"Task"}
	pkg.Statusline = true
	exports, err := eng.Exports(items(pkg, eng))
	require.NoError(t, err)
	in, err := delivery.InputsFor(delivery.Loadout{Package: pkg, Exports: exports}, nil)
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), in.Context.Text)
	require.Equal(t, map[string]wire.MCPServer{"tasks": tasks}, in.MCP.Servers)
	require.Equal(t, []string{"Task"}, in.Settings.DenyTools)
	require.True(t, in.Settings.Statusline)
	require.Equal(t, pkg.Hooks.Unified, in.Hooks.Hooks)
	require.Len(t, in.Commands.Commands, 1)
	require.Len(t, in.Skills.Skills, 1)
}

// sessionEndpointApproach is a dynamic approach for the test: it RECORDS the
// endpoint it was asked to render and answers in a shape of its own (the
// credential lands in Notes, not a header), so the test can tell the
// ENGINE's rendering from a name-keyed swap in core spelling
// engine.BearerEntry.
type sessionEndpointApproach struct{ rendered *[]sessions.Endpoint }

func (sessionEndpointApproach) Name() string { return "test-endpoint" }
func (sessionEndpointApproach) Traits() present.Traits {
	return present.Traits{Roots: []present.RootKind{present.RootSessionHome}}
}
func (a sessionEndpointApproach) Endpoint(ep sessions.Endpoint) wire.MCPServer {
	*a.rendered = append(*a.rendered, ep)
	rendered := wire.MCPServer{URL: ep.URL}
	rendered.Notes = "rendered by the engine with " + ep.Credential
	return rendered
}

// TestInputsFor_SessionEndpointEntry_IsOneMechanism proposes the ONE
// mechanism behind ctxloom's own MCP entry: the companion DECLARES the entry
// as served by the session's endpoint (wire.ServedBySessionEndpoint), and
// the engine's dynamic approach RENDERS it from the endpoint the loadout
// carries. Inside a session the entry is whatever the approach returns —
// URL + bearer, under the declared key; at rest (no endpoint bound) the entry
// renders NOTHING, because there is no session to inject; and an engine with
// no dynamic approach receives nothing for it either, on the same terms
// Base.Delegate already states (the session endpoint is the dynamic half's).
// Every other server is passed through as declared.
func TestInputsFor_SessionEndpointEntry_IsOneMechanism(t *testing.T) {
	declared := wire.MCPServer{ServedBy: wire.ServedBySessionEndpoint, Notes: "served by the session"}
	pkg := compositetest.Fixture(t, compositetest.WithMCP(wire.CtxloomServerName, declared), compositetest.WithMCP("tasks", tasks))
	ep := sessions.Endpoint{URL: "http://127.0.0.1:4242/mcp", Credential: "bearer-1"}

	t.Run("in a session, the engine's dynamic approach renders the endpoint under the declared key", func(t *testing.T) {
		var rendered []sessions.Endpoint
		in, err := delivery.InputsFor(delivery.Loadout{Package: pkg, MCP: ep}, sessionEndpointApproach{&rendered})
		require.NoError(t, err)
		require.Equal(t, []sessions.Endpoint{ep}, rendered, "the approach renders the endpoint the loadout carries, once")
		require.Equal(t, map[string]wire.MCPServer{
			wire.CtxloomServerName: {URL: ep.URL, Notes: "rendered by the engine with bearer-1"},
			"tasks":                tasks,
		}, in.MCP.Servers)
	})
	t.Run("at rest, the entry renders nothing", func(t *testing.T) {
		var rendered []sessions.Endpoint
		in, err := delivery.InputsFor(delivery.Loadout{Package: pkg}, sessionEndpointApproach{&rendered})
		require.NoError(t, err)
		require.Empty(t, rendered, "nothing is rendered when no endpoint is bound")
		require.Equal(t, map[string]wire.MCPServer{"tasks": tasks}, in.MCP.Servers)
	})
	t.Run("an engine with no dynamic approach receives nothing for it", func(t *testing.T) {
		in, err := delivery.InputsFor(delivery.Loadout{Package: pkg, MCP: ep}, nil)
		require.NoError(t, err)
		require.Equal(t, map[string]wire.MCPServer{"tasks": tasks}, in.MCP.Servers)
	})
	t.Run("a stdio entry under ctxloom's key is NOT swapped by name", func(t *testing.T) {
		stdio := wire.MCPServer{Command: "ctxloom", Args: []string{"mcp", "serve"}}
		pkg := compositetest.Fixture(t, compositetest.WithMCP(wire.CtxloomServerName, stdio))
		var rendered []sessions.Endpoint
		in, err := delivery.InputsFor(delivery.Loadout{Package: pkg, MCP: ep}, sessionEndpointApproach{&rendered})
		require.NoError(t, err)
		require.Empty(t, rendered)
		require.Equal(t, map[string]wire.MCPServer{wire.CtxloomServerName: stdio}, in.MCP.Servers, "the declaration, not the key, selects the dynamic rendering")
	})
}
