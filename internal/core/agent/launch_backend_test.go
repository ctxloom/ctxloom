package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordLifecycle captures the contextHash MergeManaged receives so Setup tests
// can assert whether the SessionStart context-injection hook was suppressed (a
// "" hash ⇒ no hook appended). It DOES expose GetHooks/GetBundleMCP, folding
// the payload it was handed exactly as BaseLifecycle does, so mergedState
// resolves ok=true — isolating the merge/hash contract from the surface
// plumbing without tripping setupViaCells' now-enforced accessor check.
// noAccessorLifecycle below is the double for that check itself.
type recordLifecycle struct {
	merged      bool
	contextHash string
	hooks       *wire.HooksConfig
	bundleMCP   map[string]wire.MCPServer
}

func (r *recordLifecycle) MergeManaged(m *ManagedConfig, _ string, contextHash string) {
	r.merged = true
	r.contextHash = contextHash
	if m != nil {
		r.hooks = m.Hooks
		r.bundleMCP = m.BundleMCP
	}
}

func (r *recordLifecycle) GetHooks() *wire.HooksConfig             { return r.hooks }
func (r *recordLifecycle) GetBundleMCP() map[string]wire.MCPServer { return r.bundleMCP }

// noAccessorLifecycle is a ManagedLifecycle that exposes ONLY MergeManaged —
// no GetHooks/GetBundleMCP — the shape this test needs: every REAL backend
// embeds BaseLifecycle (which has both), so this double is how the
// mergedState() ok=false branch gets exercised at all.
type noAccessorLifecycle struct{}

func (noAccessorLifecycle) MergeManaged(*ManagedConfig, string, string) {}

// ---- cell-seam test doubles --------------------------------------------------

// recordSet backs a fake Declaration (recordDeclaration): it captures the
// inputs its constructors received and, when delivered, logs each surface's
// cleanup into a shared order slice so a test can assert LIFO teardown. It
// records which form (well-known vs out-of-cwd) the cell used and the roots
// it targeted. contextErr forces the context surface to fail its delivery,
// exercising the launch's refusal.
type recordSet struct {
	order      *[]string
	contextErr error
	mcpErr     error

	// built counts constructor invocations: zero proves nothing was
	// constructed (the nil-payload gate); five proves an EMPTY payload still
	// reached every constructor.
	built  int
	inputs SurfaceInputs

	// deliverStarts / realizeStarts / presentStarts record the advised roots
	// each well-known Deliver / each out-of-cwd form / each PresentExisting
	// received, in delivery order. Keeping them apart is the whole assertion for
	// the launch forms: a form that PRESENTS must leave the first two empty.
	deliverStarts []present.Start
	realizeStarts []present.Start
	presentStarts []present.Start
	// presentErr forces PresentExisting to refuse, standing in for a session
	// surface that is not on disk.
	presentErr error
}

// construct returns the Construct for one fake surface, capturing the inputs.
func (s *recordSet) construct(label string) Construct {
	return func(in SurfaceInputs, _ afero.Fs) Approach {
		s.built++
		s.inputs = in
		return &recordSurface{set: s, label: label}
	}
}

// recordDeclaration declares every kind at ApproachUnsafeFile, each fake
// surface additionally offering the out-of-cwd form — like claude's
// flag-backed surfaces — so a SharedCell converts every one of them.
func recordDeclaration(s *recordSet) Declaration {
	return Declaration{
		SurfaceContext:  Presents("test", SurfaceContext, ApproachUnsafeFile, s.construct("context")),
		SurfaceMCP:      Presents("test", SurfaceMCP, ApproachUnsafeFile, s.construct("mcp")),
		SurfaceSettings: Presents("test", SurfaceSettings, ApproachUnsafeFile, s.construct("settings")),
		SurfaceCommands: Presents("test", SurfaceCommands, ApproachUnsafeFile, s.construct("commands")),
		SurfaceSkills:   Presents("test", SurfaceSkills, ApproachUnsafeFile, s.construct("skills")),
	}
}

func (s *recordSet) handle(label string) Delivered {
	return recordDelivered{order: s.order, label: label}
}

// recordSurface is one fake surface implementing Approach and additionally
// offering DeliverIsolated (the OutOfCwd form), so the same double works
// whichever way a cell delivers it.
type recordSurface struct {
	set   *recordSet
	label string
}

func (s *recordSurface) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(s.label).Build()
}

func (s *recordSurface) Deliver(start present.Start) (Delivered, error) {
	s.set.deliverStarts = append(s.set.deliverStarts, start)
	if s.set.contextErr != nil && s.label == "context" {
		return nil, s.set.contextErr
	}
	if s.set.mcpErr != nil && s.label == "mcp" {
		return nil, s.set.mcpErr
	}
	return s.set.handle(s.label), nil
}

func (s *recordSurface) DeliverIsolated(start present.Start) (Delivered, error) {
	s.set.realizeStarts = append(s.set.realizeStarts, start)
	if s.set.contextErr != nil && s.label == "context" {
		return nil, s.set.contextErr
	}
	if s.set.mcpErr != nil && s.label == "mcp" {
		return nil, s.set.mcpErr
	}
	return s.set.handle(s.label), nil
}

// PresentExisting records that this surface was NAMED rather than written, and
// returns a path without touching the filesystem — the Existing form.
func (s *recordSurface) PresentExisting(start present.Start) (string, error) {
	s.set.presentStarts = append(s.set.presentStarts, start)
	if s.set.presentErr != nil {
		return "", s.set.presentErr
	}
	return start.UnderScratch(s.label).Build().HostPath, nil
}

// noContextDeclaration is recordDeclaration WITHOUT a context kind —
// mirroring a backend with no distinct context surface (its context rides
// another surface entirely) — so Build skips SurfaceContext and the resolved
// surface list's first entry is something else (MCP). It was built for the
// regression where the shared-cwd delivery loop identified the context surface
// by INDEX 0 rather than by KIND, so on a backend shaped like this an MCP
// delivery failure was misidentified as a context failure and silently
// "recovered" via the context-injection-hook fallback instead of returning an
// error. That fallback is gone; the double stays because it is the only
// declaration here with no context surface at all.
func noContextDeclaration(s *recordSet) Declaration {
	d := recordDeclaration(s)
	delete(d, SurfaceContext)
	return d
}

// hookDeclaration is recordDeclaration with the context kind delivered by the
// shared HookCarriedContext (a Rider) — the shape of an engine whose context
// rides its hooks surface.
func hookDeclaration(s *recordSet) Declaration {
	d := recordDeclaration(s)
	d[SurfaceContext] = Presents("test", SurfaceContext, ApproachHook, HookCarriedContext)
	return d
}

// Kind maps the fake's label to its SurfaceKind so the SurfaceSelection the launch
// path now drives (WithEverything) includes it — mirroring the real surfaces, which
// are all KindedDelivery.
func (s *recordSurface) Kind() SurfaceKind {
	switch s.label {
	case "context":
		return SurfaceContext
	case "mcp":
		return SurfaceMCP
	case "settings":
		return SurfaceSettings
	case "skills":
		return SurfaceSkills
	default:
		return SurfaceCommands
	}
}

type recordDelivered struct {
	order *[]string
	label string
}

func (d recordDelivered) Cleanup() error {
	if d.order != nil {
		*d.order = append(*d.order, d.label)
	}
	return nil
}

// ---- helpers ----------------------------------------------------------------

func newLegacyBackend() (*LaunchBackend, *recordLifecycle) {
	rec := &recordLifecycle{}
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(rec, NewBaseContextProvider(), nil, nil)
	return b, rec
}

// newCellBackend wires a LaunchBackend onto the fake declaration over set
// (recording the inputs its constructors are handed). The lifecycle is a real
// BaseLifecycle so mergedState resolves the merged hooks/MCP the surface
// inputs carry.
func newCellBackend(set *recordSet) *LaunchBackend {
	return newDeclaredBackend(recordDeclaration(set))
}

// newDeclaredBackend wires a LaunchBackend onto decl with a real
// BaseLifecycle.
func newDeclaredBackend(decl Declaration) *LaunchBackend {
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(NewBaseLifecycle("test"), NewBaseContextProvider(), nil, decl)
	return b
}

// ---- empty-declaration path (protocol-only) + no declaration ----------------

// TestSetup_EmptyDeclaration_MergesNoFiles proves the protocol-only path: an
// empty Declaration still runs MergeManaged (so ManagedChatMCPServers has the
// merged servers to inject over the wire) but materializes no files — no
// Provide, no delivered handles.
func TestSetup_EmptyDeclaration_MergesNoFiles(t *testing.T) {
	rec := &recordLifecycle{}
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(rec, NewBaseContextProvider(), nil, Declaration{})

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Env:       map[string]string{SessionHarpEnv: "perky-same-chevy"},
		Fragments: []*Fragment{{Content: "project rules"}},
		Managed:   &ManagedConfig{},
	}))

	assert.True(t, rec.merged, "MergeManaged runs so ManagedChatMCPServers has the merged set")
	assert.Empty(t, rec.contextHash, "no Provide: context rides the ACP protocol, not a cache file")
	assert.Empty(t, b.delivered, "an empty declaration materializes no files")
}

// TestSetup_NoDeclaration_ErrorsRatherThanPanicking pins the fix: Setup used
// to return nil (full success) when the backend declared nothing at all,
// even though the doc comment right above it calls that exact case "a
// misconfigured backend". Not panicking is right (a nil declaration is
// recoverable), but reporting SUCCESS while setting up nothing is the "exit
// 0, zero bytes delivered" failure shape this codebase is watched for — every
// real backend supplies a Declaration at InitLaunch (a protocol-only one an
// empty one), so a nil declaration here is never legitimate "nothing to do".
func TestSetup_NoDeclaration_ErrorsRatherThanPanicking(t *testing.T) {
	b, rec := newLegacyBackend()
	require.Nil(t, b.surfaces)
	require.NotPanics(t, func() {
		err := b.Setup(context.Background(), &SetupRequest{
			WorkDir: t.TempDir(),
			Managed: &ManagedConfig{},
		})
		require.Error(t, err, "a nil delivery is a misconfigured backend and must fail Setup, not report success")
	})
	assert.False(t, rec.merged, "a nil delivery does nothing")
	assert.Empty(t, b.delivered)
}

// TestSharedScratchDir pins where a SharedCell's race-safe surfaces land: a
// valid harp resolves to that harp's ephemeral directory, and an empty harp
// refuses loudly (ErrSharedScratchNoHarp) rather than silently falling back to
// the OS temp dir — see ErrSharedScratchNoHarp's doc (taskloom urgent-staunch).
func TestSharedScratchDir(t *testing.T) {
	want, err := paths.HarpEphemeralDir("perky-same-chevy")
	require.NoError(t, err)
	got, err := sharedScratchDir("perky-same-chevy")
	require.NoError(t, err)
	assert.Equal(t, want, got)

	_, err = sharedScratchDir("")
	require.Error(t, err, "an empty harp is a programming error, not a case to absorb into a shared temp dir")
	assert.ErrorIs(t, err, ErrSharedScratchNoHarp)
}

// TestSetup_SharedCell_EmptyHarpRefusesLoudly proves the refusal reaches
// Setup itself: a SharedCell run whose Env carries no CTXLOOM_SESSION_HARP
// must fail Setup with ErrSharedScratchNoHarp rather than silently landing
// its race-safe surfaces in the OS temp dir.
func TestSetup_SharedCell_EmptyHarpRefusesLoudly(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	b := newCellBackend(set)

	err := b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "project rules"}},
		CellKind:  CellKindShared,
		Managed:   &ManagedConfig{},
	})
	require.Error(t, err, "a SharedCell run with no harp in Env must refuse rather than fall back to the OS temp dir")
	assert.ErrorIs(t, err, ErrSharedScratchNoHarp)
	assert.Empty(t, b.delivered, "no surface may be delivered once the run's scratch root could not be resolved")
}

// ---- cell path: shared cell (claude-like, RawContext=false) -----------------

// TestSetup_SharedCell_SuppressesHookRoutesMergedInputs proves the SharedCell
// path for a flag-context backend: MergeManaged is fed "" (no SessionStart
// context-injection hook — context rides the launch flag), the Build closure
// receives the assembled context string + merged state, the isolated dir is the
// harp's PRIVATE ephemeral dir, and delivery uses the shared-cwd (race-safe) set
// without touching Flush.
func TestSetup_SharedCell_SuppressesHookRoutesMergedInputs(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	b := newCellBackend(set)

	ephem, err := paths.HarpEphemeralDir("perky-same-chevy")
	require.NoError(t, err)

	work := t.TempDir()
	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   work,
		Env:       map[string]string{SessionHarpEnv: "perky-same-chevy"},
		Fragments: []*Fragment{{Content: "project rules"}},
		CellKind:  CellKindShared,
		Managed: &ManagedConfig{
			ManageStatusline: true,
			Commands:         []CommandExport{{Name: "demo"}},
			Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
				SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
			}},
			BundleMCP: map[string]wire.MCPServer{"bundle-srv": {Command: "brun"}},
		},
	}))

	assert.Equal(t, "project rules", set.inputs.Context, "assembled context string routed to the constructors")
	require.Len(t, set.realizeStarts, 5, "every surface realized through its out-of-cwd form")
	for _, start := range set.realizeStarts {
		assert.Equal(t, ephem, start.Paths().Scratch.Host, "the out-of-cwd root is the harp's PRIVATE ephemeral dir")
		assert.Equal(t, work, start.Paths().ProjectRoot.Host, "the project root is the live working dir")
	}
	assert.Empty(t, set.deliverStarts, "a SharedCell never runs the well-known write when a realization exists")
	assert.Equal(t, []CommandExport{{Name: "demo"}}, set.inputs.Commands, "commands routed through inputs")
	require.NotNil(t, set.inputs.Hooks, "merged hooks routed through inputs")
	assert.NotEmpty(t, set.inputs.Hooks.Unified.SessionStart, "merged hooks carry the session-bind hook")
	assert.True(t, set.inputs.ManageStatusline, "manageStatusline mirrors the managed config")
	assert.Equal(t, map[string]wire.MCPServer{"bundle-srv": {Command: "brun"}}, set.inputs.BundleMCP,
		"the merged bundle MCP set routed through inputs")
	for _, h := range set.inputs.Hooks.Unified.SessionStart {
		assert.Empty(t, h.ContextHash,
			"no context-injection hook: MergeManaged was fed an empty hash")
	}
	require.Len(t, b.delivered, 5, "all five surfaces collected via the seam")
}

// ---- cell path: isolated cell -----------------------------------------------

// TestSetup_IsolatedCell_UsesWellKnownSet proves an isolated cell delivers the
// plain (well-known) surface set into the private working dir — not the shared-cwd
// race-safe set — and records every handle.
func TestSetup_IsolatedCell_UsesWellKnownSet(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	b := newCellBackend(set)
	work := t.TempDir()

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   work,
		Fragments: []*Fragment{{Content: "rules"}},
		CellKind:  CellKindDirectoryIsolated,
		Managed:   &ManagedConfig{},
	}))

	assert.Empty(t, set.realizeStarts, "an isolated cell never runs an out-of-cwd form")
	require.Len(t, set.deliverStarts, 5, "every surface delivered through the well-known write")
	for _, start := range set.deliverStarts {
		assert.Equal(t, work, start.Paths().ProjectRoot.Host, "each well-known surface lands in the private working dir")
		assert.Equal(t, work, start.Paths().Scratch.Host, "an isolated cell's scratch IS its private working dir")
	}
	require.Len(t, b.delivered, 5, "all five surfaces collected")
}

// ---- cell path: hook-carried context, on EVERY cell ---------------------------

// TestSetup_HookContext_InstallsHookOnEveryCell proves that a context surface
// resolved at the hook approach (a Rider) gets its hook INSTALLED — the
// content-addressed cache file materialized and the SessionStart injection
// hook appended to the merged hooks the settings surface writes — on an
// ISOLATED cell as well as a shared one. It used to be installed only on the
// SharedCell arm, so a worktree or container launch pinned to the hook
// approach launched a context-less session while Setup reported success.
func TestSetup_HookContext_InstallsHookOnEveryCell(t *testing.T) {
	for _, cell := range []CellKind{CellKindDirectoryIsolated, CellKindProcessIsolated, CellKindShared} {
		t.Run(cell.String(), func(t *testing.T) {
			var order []string
			set := &recordSet{order: &order}
			b := newDeclaredBackend(hookDeclaration(set))

			work := t.TempDir()
			require.NoError(t, b.Setup(context.Background(), &SetupRequest{
				WorkDir:   work,
				Env:       map[string]string{SessionHarpEnv: "perky-same-chevy"},
				Fragments: []*Fragment{{Content: "project rules"}},
				CellKind:  cell,
				Managed:   &ManagedConfig{Hooks: &wire.HooksConfig{}},
			}))

			hash := b.context.GetContextHash()
			require.NotEmpty(t, hash, "the hook approach materializes the raw cache file")
			require.FileExists(t, filepath.Join(work, SCMContextSubdir, hash+".md"))
			assert.NotEmpty(t, b.context.GetContextFilePath(), "the CTXLOOM_CONTEXT_FILE path is set")
			hooks, _, ok := b.mergedState()
			require.True(t, ok)
			var injected bool
			for _, h := range hooks.Unified.SessionStart {
				if h.ContextHash == hash {
					injected = true
				}
			}
			assert.True(t, injected, "the SessionStart injection hook is appended to the merged hooks the settings surface writes")
			// Context is a Rider: no handle of its own; the other four deliver.
			assert.Len(t, b.delivered, 4)
		})
	}
}

// ---- NIL vs EMPTY payload -----------------------------------------------------

// TestSetup_ManagedNil_DeliversNothing pins the NIL half: a nil managed
// payload means the config failed to load and the run degraded through, so
// Setup returns without touching any surface — nothing merged, nothing
// constructed, nothing delivered, and no cache file written either.
func TestSetup_ManagedNil_DeliversNothing(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	rec := &recordLifecycle{}
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(rec, NewBaseContextProvider(), nil, hookDeclaration(set))

	work := t.TempDir()
	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   work,
		Fragments: []*Fragment{{Content: "project rules"}},
		CellKind:  CellKindDirectoryIsolated,
		Managed:   nil,
	}))

	assert.False(t, rec.merged, "a nil payload never reaches the merge")
	assert.Zero(t, set.built, "no approach is constructed with no managed payload")
	assert.Empty(t, b.delivered, "no surfaces are delivered when there is no managed payload")
	assert.Empty(t, b.context.GetContextHash(), "no cache file is written for a degraded run")
	assert.NoDirExists(t, filepath.Join(work, SCMContextSubdir))
}

// TestSetup_ManagedEmpty_ReachesTheWriters pins the EMPTY half, which is a
// DIFFERENT fact: an empty payload deliberately flows on to the writers, which
// reconcile to it and retract what ctxloom installed last round. Every
// constructor runs with the (empty) inputs and every surface delivers.
func TestSetup_ManagedEmpty_ReachesTheWriters(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	b := newCellBackend(set)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:  t.TempDir(),
		CellKind: CellKindDirectoryIsolated,
		Managed:  &ManagedConfig{},
	}))

	// 10 = 5 kinds x 2 constructions each. Selection probes each kind's current
	// approach once to ask whether THIS RUN can root it (ensureRootable), then
	// Build constructs the approach it settled on. It was 5 before that probe
	// existed; the doubling is the probe, not a second delivery.
	assert.Equal(t, 10, set.built, "every declared approach is constructed from the empty payload")
	assert.Empty(t, set.inputs.Commands)
	assert.Empty(t, set.inputs.BundleMCP)
	require.Len(t, b.delivered, 5, "every surface delivers — that is what retracts last round's install")
}

// ---- cell path: context-delivery refusal ------------------------------------

// TestSetup_SharedCell_ContextFailureRefusesTheLaunch is the guard on
// feeble-sway's ruling: a context-delivery failure REFUSES, and must never be
// converted into a different delivery mechanism.
//
// This test used to be TestSetup_SharedCell_ContextFailureFallsBackToHook and
// asserted the exact opposite — that a SharedCell context failure silently
// installed the SessionStart injection hook and let Setup report success. That
// is the cross-approach fallback the design forbids: the run received its
// context through a channel its isolation argument was never made against, and
// nothing in the exit code said so.
//
// The negative assertion is the load-bearing one. Asserting only that Setup
// errors would still pass if the hook were installed on the way out, which is
// the very leak being closed, so the hook's ABSENCE is asserted directly.
func TestSetup_SharedCell_ContextFailureRefusesTheLaunch(t *testing.T) {
	var order []string
	set := &recordSet{order: &order, contextErr: errors.New("disk full")}
	b := newCellBackend(set)

	err := b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Env:       map[string]string{SessionHarpEnv: "perky-same-chevy"},
		Fragments: []*Fragment{{Content: "project rules"}},
		CellKind:  CellKindShared,
		Managed:   &ManagedConfig{},
	})
	require.Error(t, err, "a context-delivery failure must refuse the launch, never report success having quietly delivered context another way")

	// No injection hook was appended onto the shared merged hooks: the failure
	// did not reroute the context through the hook surface.
	hooks, _, ok := b.mergedState()
	require.True(t, ok)
	require.NotNil(t, hooks)
	for _, h := range hooks.Unified.SessionStart {
		assert.Empty(t, h.ContextHash,
			"a failed context delivery must not install the SessionStart injection hook: that is the cross-approach fallback the refusal exists to prevent")
	}
}

// TestSetup_SharedCell_NonContextSurfaceFailureRefuses guards a delivery
// failure on a surface that is NOT context. It outlives the fallback whose
// aim it was originally written to correct: that fallback fired whenever the
// FIRST resolved surface failed (`i == 0`), coupling launch_backend.go to
// cells.go's surfaceOrder by POSITION rather than by the surface's actual
// kind, so for a backend with no distinct context surface (cells.go's Build()
// skips a kind with no supported approaches) an MCP failure was swallowed by
// recovery meant only for context.
//
// With the fallback deleted there is no kind-dispatch left to get wrong, and
// the case it covered — a non-context surface failing on a shared cell —
// still has to refuse. It is kept as the guard on that, not retired with the
// bug, because nothing else in this file exercises the no-context-surface
// declaration.
func TestSetup_SharedCell_NonContextSurfaceFailureRefuses(t *testing.T) {
	var order []string
	inner := &recordSet{order: &order, mcpErr: fmt.Errorf("mcp write failed")}

	b := newDeclaredBackend(noContextDeclaration(inner))

	err := b.Setup(context.Background(), &SetupRequest{
		WorkDir:  t.TempDir(),
		Env:      map[string]string{SessionHarpEnv: "perky-same-chevy"},
		CellKind: CellKindShared,
		Managed:  &ManagedConfig{},
	})
	require.Error(t, err, "an MCP delivery failure on a shared cell must refuse the launch; no surface's failure is recoverable by substituting another mechanism")
}

// TestSetup_SharedCell_ContextFailureErrorNamesCause keeps the property the
// old warning-sink test guarded — that the operator learns WHY the context
// delivery failed, not just that something did — now that the refusal carries
// it instead of a warning.
//
// The cause travels by wrapping, so errors.Is still finds it: a caller that
// wants to branch on the underlying failure can, and a caller that only prints
// gets the reason in the text. Before, the message was a fixed string on
// os.Stderr naming no cause at all.
func TestSetup_SharedCell_ContextFailureErrorNamesCause(t *testing.T) {
	var order []string
	cause := errors.New("disk full")
	set := &recordSet{order: &order, contextErr: cause}
	b := newCellBackend(set)

	err := b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Env:       map[string]string{SessionHarpEnv: "perky-same-chevy"},
		Fragments: []*Fragment{{Content: "project rules"}},
		CellKind:  CellKindShared,
		Managed:   &ManagedConfig{},
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, cause,
		"the refusal must wrap the triggering error, so a caller can still branch on the underlying cause")
	assert.Contains(t, err.Error(), "disk full",
		"the refusal must name why the context delivery failed, not merely that it did")
}

// TestSetup_LifecycleWithoutAccessors_ErrorsRatherThanWritingEmpty pins the
// fix: setupViaCells discarded mergedState's `ok`, so a lifecycle
// lacking the GetHooks/GetMCP accessors (recordLifecycle, engineered
// specifically to trigger this — every real backend embeds BaseLifecycle,
// which has both) fell through to building the surface set with nil
// hooks/nil MCP as if that were the correctly-merged state, silently
// materializing a settings file containing none of the configured hooks or
// servers instead of surfacing the misconfiguration.
func TestSetup_LifecycleWithoutAccessors_ErrorsRatherThanWritingEmpty(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(noAccessorLifecycle{}, NewBaseContextProvider(), nil, recordDeclaration(set))

	err := b.Setup(context.Background(), &SetupRequest{
		WorkDir:  t.TempDir(),
		CellKind: CellKindDirectoryIsolated,
		Managed:  &ManagedConfig{Hooks: &wire.HooksConfig{}},
	})
	require.Error(t, err, "a lifecycle without GetHooks/GetBundleMCP must fail Setup, not silently deliver an empty merged state")
	assert.Empty(t, b.delivered, "nothing should be materialized when the merged state could not be read")
}

// ---- cleanup ----------------------------------------------------------------

// TestCleanup_RunsDeliveredHandlesLIFO proves Cleanup reverses every delivered
// surface in last-in-first-out order and clears the handle set.
func TestCleanup_RunsDeliveredHandlesLIFO(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	b := newCellBackend(set)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Env:       map[string]string{SessionHarpEnv: "perky-same-chevy"},
		Fragments: []*Fragment{{Content: "rules"}},
		CellKind:  CellKindShared,
		Managed:   &ManagedConfig{},
	}))

	require.NoError(t, b.Cleanup(context.Background()))
	// Delivery order was context, mcp, settings, commands, skills → LIFO reverses it.
	assert.Equal(t, []string{"skills", "commands", "settings", "mcp", "context"}, order, "Cleanup runs handles LIFO")
	assert.Empty(t, b.delivered, "Cleanup clears the handle set")
}

// TestCleanup_AttemptsAllJoinsEveryError pins the fix: Cleanup used to keep
// ONLY the first teardown error, silently discarding every subsequent
// failure even though every handle is still attempted. Both errors below
// must be recoverable from the returned error (errors.Is), not just the
// first one encountered in LIFO order.
func TestCleanup_AttemptsAllJoinsEveryError(t *testing.T) {
	boom := errors.New("boom")
	undoneLast := errors.New("undone-last")
	var ran int
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	// Cleanup undoes LIFO (last appended, first undone). The last-appended handle
	// errors → it is the first error encountered; a later handle also errors and
	// must NOT be discarded, and the clean handle must still run.
	b.delivered = []Delivered{
		deliveredFn(func() error { ran++; return undoneLast }), // undone last
		deliveredFn(func() error { ran++; return nil }),        // undone second
		deliveredFn(func() error { ran++; return boom }),       // undone first → first error
	}

	err := b.Cleanup(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, boom, "the first (LIFO) error must be present")
	assert.ErrorIs(t, err, undoneLast, "a later handle's error must not be discarded")
	assert.Equal(t, 3, ran, "Cleanup attempts every handle despite an error")
}

type deliveredFn func() error

func (d deliveredFn) Cleanup() error { return d() }

// ---- ExecuteEnv seam --------------------------------------------------------

// TestExecuteEnv_MergesExtraEnv proves the per-backend env contributor
// (SetExecuteEnv) is merged on top of the request env (the seam codex uses for
// its cell-scoped CODEX_HOME), and wins on a key clash.
func TestExecuteEnv_MergesExtraEnv(t *testing.T) {
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.SetExecuteEnv(func(req *ExecuteRequest) map[string]string {
		return map[string]string{"CODEX_HOME": filepath.Join(req.WorkDir, ".codex")}
	})

	env := b.ExecuteEnv(&ExecuteRequest{WorkDir: "/w", Env: map[string]string{"KEEP": "1"}})
	assert.Equal(t, "1", env["KEEP"], "request env is preserved")
	assert.Equal(t, filepath.Join("/w", ".codex"), env["CODEX_HOME"], "the contributor's env is merged in")
}

// TestSetup_SurfaceContext_FragmentsAssemblingToNothingIsLoud pins the guard on
// the SURFACE context path. The raw-cache path has always refused this input
// (Provide → WriteContextFile → ErrNoContext), on the stated grounds that "the
// user configured no context" and "every fragment the user configured resolved
// to nothing" are different facts. A surface-delivering backend read the same
// empty string, built its surface set around it, wrote no context file, emitted
// no launch flag and returned nil — the two paths disagreed about the same fact.
func TestSetup_SurfaceContext_FragmentsAssemblingToNothingIsLoud(t *testing.T) {
	order := []string{}
	set := &recordSet{order: &order}
	b := newCellBackend(set)

	err := b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Name: "rules", Content: ""}, {Name: "style", Content: " \n\t"}},
		Managed:   &ManagedConfig{},
		CellKind:  CellKindShared,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoContext, "the surface path must report the same fact the raw-cache path does")
	assert.Empty(t, b.delivered, "no surface may be delivered off a context the backend could not assemble")
}

// TestSetup_SurfaceContext_NoFragmentsStillSetsUp is the other half: a project
// that configured NO context is not an error, and every non-context surface is
// still delivered.
func TestSetup_SurfaceContext_NoFragmentsStillSetsUp(t *testing.T) {
	order := []string{}
	set := &recordSet{order: &order}
	b := newCellBackend(set)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:  t.TempDir(),
		Env:      map[string]string{SessionHarpEnv: "perky-same-chevy"},
		Managed:  &ManagedConfig{},
		CellKind: CellKindShared,
	}))
	assert.NotEmpty(t, b.delivered, "no context configured is not a reason to skip the other surfaces")
}

// ---- ExecuteCLI: stdin cleanup relay ----------------------------------------

// newSpecCapturingBackend returns a LaunchBackend whose launcher records the
// LaunchSpec it was handed instead of execing anything, so a test can read what
// ExecuteCLI actually assembled for the runtime.
func newSpecCapturingBackend() (*LaunchBackend, *LaunchSpec) {
	var captured LaunchSpec
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.BinaryPath = "/bin/true"
	b.SetLauncher(func(_ context.Context, spec LaunchSpec, _ io.Reader, _, _ io.Writer, _ <-chan WindowSize) (int32, error) {
		captured = spec
		return 0, nil
	})
	return b, &captured
}

// TestExecuteCLI_InteractiveRelaysStdinCleanup pins the middle of the stdin
// ownership chain, which nothing else reaches. The cleanup travels
// GRPCServer.Run (which makes the io.Pipe and alone may close it) →
// ExecuteRequest.StdinCleanup → ExecuteCLI → RunInteractive → LaunchSpec →
// ptyrunner. The grpc suite pins the top of that chain and the ptyrunner suite
// pins the bottom, but ExecuteCLI is the shared exec tail EVERY exec-style
// backend funnels through, and it had no test at all — so replacing
// req.StdinCleanup with a hardcoded nil here passed the whole suite while
// silently restoring the wedge one layer below where it was fixed.
//
// Asserting non-nil alone would not be enough: it survives a relay that
// fabricates some other closure. The assertion is that the caller's OWN
// cleanup is what reaches the spec, observed by its effect.
func TestExecuteCLI_InteractiveRelaysStdinCleanup(t *testing.T) {
	b, captured := newSpecCapturingBackend()

	released := false
	_, err := b.ExecuteCLI(context.Background(), &ExecuteRequest{
		Mode:         ModeInteractive,
		Stdin:        bytes.NewReader(nil),
		StdinCleanup: func() { released = true },
	}, nil, nil, nil, io.Discard, io.Discard)
	require.NoError(t, err)

	require.NotNil(t, captured.StdinCleanup,
		"the caller's stdin cleanup must reach the launch spec: without it nothing retires the wire stdin and the gRPC stream pump parks forever on its next write")
	captured.StdinCleanup()
	assert.True(t, released,
		"the spec must carry the CALLER's cleanup, not a substitute — only the layer that created the reader knows whether closing it is legal")
}

// TestExecuteCLI_NonInteractiveCarriesNoStdinCleanup is the other half, and it
// is a decision rather than an omission: without a pty the reader is handed
// straight to the child and drained to EOF, so no copier goroutine ever parks
// on it and no writer waits on a reader that left. Supplying a cleanup here
// would close a reader that is still legitimately in use.
func TestExecuteCLI_NonInteractiveCarriesNoStdinCleanup(t *testing.T) {
	b, captured := newSpecCapturingBackend()

	_, err := b.ExecuteCLI(context.Background(), &ExecuteRequest{
		Mode:         ModeOneshot,
		StdinCleanup: func() { t.Error("a non-interactive run must never release the caller's stdin") },
	}, nil, bytes.NewReader(nil), nil, io.Discard, io.Discard)
	require.NoError(t, err)

	assert.Nil(t, captured.StdinCleanup,
		"a non-interactive launch owns no pty copier, so there is nothing to release and no reader it may close")
}

// ---- cell path: the engine home root ----------------------------------------

// TestSetup_EngineHomeRoot_ResolvesFromTheDeclaredVar proves the run's
// EngineHome root reaches every writer through the same advised Start the
// project root does: the engine names the env var that relocates its config
// home (SetEngineHomeVar), and setupViaCells reads THAT run's value of it.
// A run with no such var set — no controlled home — advises no EngineHome at
// all, so an approach that needs one refuses rather than guessing.
//
// Every cell kind, because the home is a property of the RUN and not of the
// cell: a shared cell's writers root at the same relocated home an isolated
// cell's do, and the only thing the cell decides is where Scratch lands. The
// shared case is the one delivery flips onto for a plain `ctxloom run`, and
// it must not depend on which branch of setupViaCells resolved Scratch.
func TestSetup_EngineHomeRoot_ResolvesFromTheDeclaredVar(t *testing.T) {
	const homeVar = "TEST_ENGINE_HOME"
	for _, cell := range []CellKind{CellKindShared, CellKindDirectoryIsolated, CellKindProcessIsolated} {
		for _, tc := range []struct {
			name string
			env  map[string]string
			want string
		}{
			{"declared and set", map[string]string{homeVar: "/proj/.ctxloom/state/h/home/test"}, "/proj/.ctxloom/state/h/home/test"},
			{"declared but unset", map[string]string{}, ""},
		} {
			t.Run(cell.String()+"/"+tc.name, func(t *testing.T) {
				set := &recordSet{}
				b := newCellBackend(set)
				b.SetEngineHomeVar(homeVar)
				// The shared cell derives Scratch from the session harp; the
				// isolated cells ignore it. Present on every case so the
				// three differ in CellKind alone.
				env := map[string]string{SessionHarpEnv: "perky-same-chevy"}
				maps.Copy(env, tc.env)
				require.NoError(t, b.Setup(context.Background(), &SetupRequest{
					WorkDir:   t.TempDir(),
					Fragments: []*Fragment{{Content: "rules"}},
					Env:       env,
					CellKind:  cell,
					Managed:   &ManagedConfig{},
				}))
				// A shared cell runs each surface's out-of-cwd form and an
				// isolated cell its well-known write; the root under test
				// must reach the writer down either route.
				starts := append(append([]present.Start{}, set.deliverStarts...), set.realizeStarts...)
				require.Len(t, starts, 5)
				for _, start := range starts {
					assert.Equal(t, tc.want, start.Paths().EngineHome.Host)
				}
			})
		}
	}
}

// An engine that never declares its home var advises no EngineHome, even
// when the run env happens to carry one: the root is a fact the ENGINE
// states, never inferred from whatever variables are in the environment.
func TestSetup_EngineHomeRoot_UndeclaredEngineAdvisesNone(t *testing.T) {
	set := &recordSet{}
	b := newCellBackend(set)
	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "rules"}},
		Env:       map[string]string{"CLAUDE_CONFIG_DIR": "/somewhere"},
		CellKind:  CellKindDirectoryIsolated,
		Managed:   &ManagedConfig{},
	}))
	require.Len(t, set.deliverStarts, 5)
	for _, start := range set.deliverStarts {
		assert.Equal(t, "", start.Paths().EngineHome.Host)
	}
}
