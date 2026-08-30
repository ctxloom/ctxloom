package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
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

// recordSet is a fake SurfaceSet capturing the inputs its Build closure received
// and, when delivered, logging each surface's cleanup into a shared order slice so
// a test can assert LIFO teardown. It records which delivery method (isolated vs
// SharedRealization) the cell used and the dir it targeted. contextErr forces the
// FIRST surface (context) to fail its delivery, exercising the injection-hook
// fallback.
type recordSet struct {
	order      *[]string
	contextErr error
	mcpErr     error

	inputs      SurfaceInputs
	isolatedDir string

	usedShared  bool
	deliverDirs []string
}

// surfaces returns the five fake surfaces in a stable order (context first).
func (s *recordSet) surfaces() []*recordSurface {
	return []*recordSurface{
		{set: s, label: "context"},
		{set: s, label: "mcp"},
		{set: s, label: "settings"},
		{set: s, label: "commands"},
		{set: s, label: "skills"},
	}
}

func (s *recordSet) Deliveries() []Delivery {
	out := make([]Delivery, 0, 5)
	for _, sf := range s.surfaces() {
		out = append(out, sf)
	}
	return out
}

// SupportedApproaches advertises every kind at UnsafeFile (the fake surfaces
// additionally offer DeliverIsolated — like claude's flag-backed surfaces), so
// WithEverything selects all four.
func (s *recordSet) SupportedApproaches(SurfaceKind) []Approach {
	return []Approach{ApproachUnsafeFile}
}

// DefaultApproach reports UnsafeFile for every kind — the fake set's only approach.
func (s *recordSet) DefaultApproach(SurfaceKind) (Approach, bool) { return ApproachUnsafeFile, true }

// SurfaceFor resolves kind to the matching fake surface (fresh, sharing the set so
// its Deliver/DeliverIsolated record into the same slices).
func (s *recordSet) SurfaceFor(kind SurfaceKind, _ Approach) (Delivery, error) {
	for _, sf := range s.surfaces() {
		if sf.Kind() == kind {
			return sf, nil
		}
	}
	return nil, fmt.Errorf("no fake surface for %s", kind)
}

// SharedRealization reports the fake surface's isolated path for every (kind,
// approach) pair — mirroring a fully flag-backed backend (claude) so
// DeliverShared always converts rather than falling back to a loud well-known
// write.
func (s *recordSet) SharedRealization(kind SurfaceKind, _ Approach) (func() (Delivered, error), bool) {
	s.usedShared = true
	for _, sf := range s.surfaces() {
		if sf.Kind() == kind {
			return sf.DeliverIsolated, true
		}
	}
	return nil, false
}

func (s *recordSet) handle(label string) Delivered {
	return recordDelivered{order: s.order, label: label}
}

// recordSurface is one fake surface implementing Delivery (isolated cell) and
// additionally offering DeliverIsolated (the shape a SharedRealization closure
// wraps), so the same double works whichever way a cell delivers it.
type recordSurface struct {
	set   *recordSet
	label string
}

func (s *recordSurface) Deliver(dir string) (Delivered, error) {
	s.set.deliverDirs = append(s.set.deliverDirs, dir)
	if s.set.contextErr != nil && s.label == "context" {
		return nil, s.set.contextErr
	}
	if s.set.mcpErr != nil && s.label == "mcp" {
		return nil, s.set.mcpErr
	}
	return s.set.handle(s.label), nil
}

func (s *recordSurface) DeliverIsolated() (Delivered, error) {
	if s.set.contextErr != nil && s.label == "context" {
		return nil, s.set.contextErr
	}
	if s.set.mcpErr != nil && s.label == "mcp" {
		return nil, s.set.mcpErr
	}
	return s.set.handle(s.label), nil
}

// noContextRecordSet wraps a *recordSet to report NO supported approaches for
// SurfaceContext — mirroring a backend with no distinct context surface (its
// context rides another surface entirely) — so cells.go's Build() skips
// SurfaceContext and the resolved surface list's first entry is something
// else (MCP). It is the double this regression test needs: the shared-
// cwd delivery loop identified the context surface by INDEX 0 rather than by
// KIND, so on a backend shaped like this an MCP delivery failure was
// misidentified as a context failure and silently "recovered" via the
// context-injection-hook fallback instead of returning an error.
type noContextRecordSet struct {
	*recordSet
}

func (s *noContextRecordSet) SupportedApproaches(kind SurfaceKind) []Approach {
	if kind == SurfaceContext {
		return nil
	}
	return s.recordSet.SupportedApproaches(kind)
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

// newCellBackend wires a LaunchBackend onto a fake CellDelivery whose Build
// returns set (recording the inputs it was handed). rawContext/contextHook mirror
// the per-engine CellDelivery flags. The lifecycle is a real BaseLifecycle so
// mergedState resolves the merged hooks/MCP the surface inputs carry.
func newCellBackend(set *recordSet, rawContext, contextHook bool) *LaunchBackend {
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(NewBaseLifecycle("test"), NewBaseContextProvider(), nil,
		&CellDelivery{
			Build: func(in SurfaceInputs, isolatedDir string) SurfaceSet {
				set.inputs = in
				set.isolatedDir = isolatedDir
				return set
			},
			RawContext:  rawContext,
			ContextHook: contextHook,
		})
	return b
}

// ---- empty-set path (acp) + nil delivery ------------------------------------

// TestSetup_EmptySurfaceSet_MergesNoFiles proves the protocol-only path (acp):
// an EmptySurfaceSet still runs MergeManaged (so ManagedChatMCPServers has the
// merged servers to inject over the wire) but materializes no files — no Provide,
// no delivered handles.
func TestSetup_EmptySurfaceSet_MergesNoFiles(t *testing.T) {
	rec := &recordLifecycle{}
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(rec, NewBaseContextProvider(), nil, &CellDelivery{
		Build: func(SurfaceInputs, string) SurfaceSet { return EmptySurfaceSet{} },
	})

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "project rules"}},
		Managed:   &ManagedConfig{},
	}))

	assert.True(t, rec.merged, "MergeManaged runs so ManagedChatMCPServers has the merged set")
	assert.Empty(t, rec.contextHash, "no Provide: context rides the ACP protocol, not a cache file")
	assert.Empty(t, b.delivered, "an empty surface set materializes no files")
}

// TestSetup_NilDelivery_ErrorsRatherThanPanicking pins the fix: Setup used
// to return nil (full success) when b.delivery is nil, even though the doc
// comment right above it calls that exact case "a misconfigured backend". Not
// panicking is right (a nil delivery is recoverable), but reporting SUCCESS
// while setting up nothing is the "exit 0, zero bytes delivered" failure
// shape this codebase is watched for — every real backend supplies a
// CellDelivery at InitLaunch (acp an empty-set one), so a nil delivery here
// is never legitimate "nothing to do".
func TestSetup_NilDelivery_ErrorsRatherThanPanicking(t *testing.T) {
	b, rec := newLegacyBackend()
	require.Nil(t, b.delivery)
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
	b := newCellBackend(set, false, false)

	ephem, err := paths.HarpEphemeralDir("perky-same-chevy")
	require.NoError(t, err)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
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

	assert.Equal(t, "project rules", set.inputs.Context, "assembled context string routed to Build")
	assert.Equal(t, ephem, set.isolatedDir, "the out-of-cwd dir is the harp's PRIVATE ephemeral dir")
	assert.True(t, set.usedShared, "a SharedCell delivers via the shared-cwd (race-safe) set")
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
	b := newCellBackend(set, false, false)
	work := t.TempDir()

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   work,
		Fragments: []*Fragment{{Content: "rules"}},
		CellKind:  CellKindDirectoryIsolated,
		Managed:   &ManagedConfig{},
	}))

	assert.False(t, set.usedShared, "an isolated cell must use the well-known Deliveries set")
	assert.Equal(t, work, set.isolatedDir, "an isolated cell targets the private working dir")
	for _, dir := range set.deliverDirs {
		assert.Equal(t, work, dir, "each well-known surface lands in the private working dir")
	}
	require.Len(t, b.delivered, 5, "all five surfaces collected")
}

// ---- cell path: RawContext (codex/kiro) ---------------------------------

// TestSetup_RawContext_WritesCacheFileAndKeysHook proves the RawContext pre-step:
// the content-addressed cache file is materialized (setting the env path), and for
// a hook engine (ContextHook) the merge hash is the cache file's hash so the
// SessionStart injection hook is keyed correctly.
func TestSetup_RawContext_WritesCacheFileAndKeysHook(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	// Use a recordLifecycle to capture the hash handed to MergeManaged.
	rec := &recordLifecycle{}
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(rec, NewBaseContextProvider(), nil, &CellDelivery{
		Build:       func(in SurfaceInputs, _ string) SurfaceSet { set.inputs = in; return set },
		RawContext:  true,
		ContextHook: true,
	})

	work := t.TempDir()
	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   work,
		Fragments: []*Fragment{{Content: "project rules"}},
		CellKind:  CellKindDirectoryIsolated,
		Managed:   &ManagedConfig{},
	}))

	assert.NotEmpty(t, rec.contextHash, "a hook engine keys the injection hook to the cache file hash")
	assert.NotEmpty(t, b.context.GetContextFilePath(), "RawContext sets the CTXLOOM_CONTEXT_FILE path")
	// The cache file is on disk under the working dir (so MergeManaged's chunk read
	// sees it).
	cachePath := filepath.Join(work, SCMContextSubdir, rec.contextHash+".md")
	require.FileExists(t, cachePath, "the raw cache file must be materialized before MergeManaged")
}

// TestSetup_RawContext_ManagedNilShortCircuits proves a nil managed payload
// short-circuits after the RawContext pre-step: the cache file stands alone (as in
// the legacy Flush no-op) and no surfaces are built or delivered.
func TestSetup_RawContext_ManagedNilShortCircuits(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	b := newCellBackend(set, true, false)

	work := t.TempDir()
	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   work,
		Fragments: []*Fragment{{Content: "project rules"}},
		CellKind:  CellKindDirectoryIsolated,
		Managed:   nil,
	}))

	hash := b.context.GetContextHash()
	require.NotEmpty(t, hash)
	assert.FileExists(t, filepath.Join(work, SCMContextSubdir, hash+".md"),
		"the raw cache file is still written")
	assert.Empty(t, b.delivered, "no surfaces are delivered when there is no managed payload")
	assert.Nil(t, set.inputs.Fragments, "Build is never invoked with no managed payload")
}

// ---- cell path: context-delivery fallback (item 6) --------------------------

// TestSetup_SharedCell_ContextFailureFallsBackToHook proves the CLAUDE.md fault
// tolerance for a flag-context backend: a SharedCell context DeliverIsolated error
// keeps context alive via the legacy hook — Provide the raw cache file and append
// the injection hook onto the shared merged hooks (which the settings surface then
// writes) — while the failed context handle is skipped and the remaining surfaces
// still deliver.
func TestSetup_SharedCell_ContextFailureFallsBackToHook(t *testing.T) {
	var order []string
	set := &recordSet{order: &order, contextErr: errors.New("disk full")}
	b := newCellBackend(set, false, false)

	work := t.TempDir()
	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   work,
		Fragments: []*Fragment{{Content: "project rules"}},
		CellKind:  CellKindShared,
		Managed:   &ManagedConfig{},
	}))

	hash := b.context.GetContextHash()
	assert.NotEmpty(t, hash, "the fallback materializes the raw cache file and keeps its hash")
	// The injection hook was appended onto the shared merged hooks (the settings
	// surface's own *wire.HooksConfig).
	hooks, _, ok := b.mergedState()
	require.True(t, ok)
	require.NotNil(t, hooks)
	var injected bool
	for _, h := range hooks.Unified.SessionStart {
		if h.ContextHash == hash {
			injected = true
		}
	}
	assert.True(t, injected, "the SessionStart injection hook is re-appended to the merged hooks")
	// The failed context handle is not recorded, but the remaining surfaces are.
	assert.Len(t, b.delivered, 4, "context handle skipped; mcp/settings/commands/skills still delivered")
}

// TestSetup_SharedCell_RecoveryOnlyFiresForContextSurface pins the fix: the
// context-recovery fallback used to fire whenever the FIRST resolved surface
// failed (`i == 0`), coupling launch_backend.go to cells.go's surfaceOrder by
// position rather than by the surface's actual kind. For a backend with no
// distinct context surface (noContextRecordSet — cells.go's Build() skips a
// kind with no supported approaches), the first resolved surface is MCP, not
// context. An MCP delivery failure must be reported as a real failure, not
// silently swallowed by a fallback meant only for the context surface.
func TestSetup_SharedCell_RecoveryOnlyFiresForContextSurface(t *testing.T) {
	var order []string
	inner := &recordSet{order: &order, mcpErr: fmt.Errorf("mcp write failed")}
	fake := &noContextRecordSet{recordSet: inner}

	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(NewBaseLifecycle("test"), NewBaseContextProvider(), nil, &CellDelivery{
		Build: func(in SurfaceInputs, isolatedDir string) SurfaceSet {
			inner.inputs = in
			inner.isolatedDir = isolatedDir
			return fake
		},
	})

	err := b.Setup(context.Background(), &SetupRequest{
		WorkDir:  t.TempDir(),
		CellKind: CellKindShared,
		Managed:  &ManagedConfig{},
	})
	require.Error(t, err, "an MCP delivery failure on a backend with no context surface must not be swallowed by the context-recovery fallback")
}

// TestSetup_SharedCell_ContextFailureWarningNamesCause pins the fix: the
// context-recovery warning used to go straight to os.Stderr with
// fmt.Fprintf(os.Stderr, ...) — bypassing this package's own Warn/clidiag
// sink (the seam every other warning in this package, and the mechanism a
// session that owns the terminal uses to redirect warnings away from a
// live TUI frame it is painting) — and named no cause at all, a fixed
// string regardless of WHY the context delivery failed. clidiag.SetSink
// redirects Warn's destination; if the warning still bypasses it, this
// buffer stays empty.
func TestSetup_SharedCell_ContextFailureWarningNamesCause(t *testing.T) {
	var order []string
	cause := errors.New("disk full")
	set := &recordSet{order: &order, contextErr: cause}
	b := newCellBackend(set, false, false)

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "project rules"}},
		CellKind:  CellKindShared,
		Managed:   &ManagedConfig{},
	}))

	assert.Contains(t, buf.String(), "disk full",
		"the recovery warning must route through the package's Warn sink and name the triggering error")
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
	b.InitLaunch(noAccessorLifecycle{}, NewBaseContextProvider(), nil, &CellDelivery{
		Build: func(in SurfaceInputs, isolatedDir string) SurfaceSet {
			set.inputs = in
			set.isolatedDir = isolatedDir
			return set
		},
	})

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
	b := newCellBackend(set, false, false)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
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
	b := newCellBackend(set, false, false)

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
	b := newCellBackend(set, false, false)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:  t.TempDir(),
		Managed:  &ManagedConfig{},
		CellKind: CellKindShared,
	}))
	assert.NotEmpty(t, b.delivered, "no context configured is not a reason to skip the other surfaces")
}

// A CellDelivery that sets ContextHook WITHOUT RawContext is a misconfigured
// backend and must be refused loudly. CellDelivery.ContextHook's own doc says
// "Requires RawContext (the hook reads the cache file)", but nothing enforced
// it: setupViaCells reads ContextHook only INSIDE the RawContext arm, so the
// pair {ContextHook: true, RawContext: false} silently left contextHash empty,
// MergeManaged appended no SessionStart injection hook, and Setup reported
// success — a backend author asking for hook-delivered context got a session
// with NO context and no diagnostic. What is measurable here is a silent
// DROP, not a bogus hook.
func TestSetup_ContextHookWithoutRawContext_IsRefused(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	rec := &recordLifecycle{}
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(rec, NewBaseContextProvider(), nil, &CellDelivery{
		Build:       func(in SurfaceInputs, _ string) SurfaceSet { set.inputs = in; return set },
		RawContext:  false,
		ContextHook: true,
	})

	// The fixture must be hostile from setupViaCells' point of view BEFORE any
	// behaviour is asserted: this is exactly the pair the doc forbids.
	require.True(t, b.delivery.ContextHook && !b.delivery.RawContext,
		"the fixture must actually hold the forbidden pair, or this test proves nothing")

	err := b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "project rules"}},
		CellKind:  CellKindDirectoryIsolated,
		Managed:   &ManagedConfig{},
	})

	require.Error(t, err, "a hook engine that never materializes the cache file must not report success")
	assert.Contains(t, err.Error(), "ContextHook")
	assert.Contains(t, err.Error(), "RawContext")
	assert.False(t, rec.merged, "the refusal must precede the merge, not leave a half-configured lifecycle")
	assert.Empty(t, b.delivered, "nothing may be delivered for a backend that cannot deliver its context")
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

// The two LEGAL pairings are unchanged — the characterization half, green before
// and after. RawContext without ContextHook (kiro: context rides
// AGENTS.md/steering, hash stays "") and both false (claude: context rides the
// append flag) both still Setup cleanly.
func TestSetup_LegalContextHookPairingsStillSucceed(t *testing.T) {
	for _, tc := range []struct{ raw, hook bool }{{true, false}, {false, false}} {
		var order []string
		set := &recordSet{order: &order}
		b := newCellBackend(set, tc.raw, tc.hook)
		require.NoError(t, b.Setup(context.Background(), &SetupRequest{
			WorkDir:   t.TempDir(),
			Fragments: []*Fragment{{Content: "project rules"}},
			CellKind:  CellKindDirectoryIsolated,
			Managed:   &ManagedConfig{},
		}), "RawContext=%v ContextHook=%v is a legal pairing", tc.raw, tc.hook)
	}
}
