package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordLifecycle captures the contextHash MergeManaged receives so Setup tests
// can assert whether the SessionStart context-injection hook was suppressed (a
// "" hash ⇒ no hook appended). It deliberately does NOT expose GetHooks/GetMCP,
// so a mergedState probe reports ok=false — isolating the merge/hash contract
// from the surface plumbing.
type recordLifecycle struct {
	merged      bool
	contextHash string
	flushed     bool
}

func (r *recordLifecycle) MergeManaged(_ *ManagedConfig, _ string, contextHash string) {
	r.merged = true
	r.contextHash = contextHash
}
func (r *recordLifecycle) Flush(string) error { r.flushed = true; return nil }

type noopSkills struct{}

func (noopSkills) RegisterFromContent(string, []CommandExport) error { return nil }

// ---- cell-seam test doubles --------------------------------------------------

// recordSet is a fake SurfaceSet capturing the inputs its Build closure received
// and, when delivered, logging each surface's cleanup into a shared order slice so
// a test can assert LIFO teardown. It records which delivery method (isolated vs
// shared-cwd) the cell used and the dir it targeted. contextErr forces the FIRST
// surface (context) to fail its delivery, exercising the injection-hook fallback.
type recordSet struct {
	order      *[]string
	contextErr error

	inputs      SurfaceInputs
	isolatedDir string

	usedShared  bool
	usedDir     string // dir SharedCwdDeliveries was called with
	deliverDirs []string
}

// surfaces returns the four fake surfaces in a stable order (context first).
func (s *recordSet) surfaces() []*recordSurface {
	return []*recordSurface{
		{set: s, label: "context"},
		{set: s, label: "mcp"},
		{set: s, label: "settings"},
		{set: s, label: "skills"},
	}
}

func (s *recordSet) Deliveries() []Delivery {
	out := make([]Delivery, 0, 4)
	for _, sf := range s.surfaces() {
		out = append(out, sf)
	}
	return out
}

func (s *recordSet) SharedCwdDeliveries(dir string) []RaceSafeDelivery {
	s.usedShared = true
	s.usedDir = dir
	out := make([]RaceSafeDelivery, 0, 4)
	for _, sf := range s.surfaces() {
		out = append(out, sf)
	}
	return out
}

func (s *recordSet) handle(label string) Delivered {
	return recordDelivered{order: s.order, label: label}
}

// recordSurface is one fake surface implementing both Delivery (isolated cell) and
// RaceSafeDelivery (shared cell), so the same double works in every cell.
type recordSurface struct {
	set   *recordSet
	label string
}

func (s *recordSurface) Deliver(dir string) (Delivered, error) {
	s.set.deliverDirs = append(s.set.deliverDirs, dir)
	if s.set.contextErr != nil && s.label == "context" {
		return nil, s.set.contextErr
	}
	return s.set.handle(s.label), nil
}

func (s *recordSurface) DeliverIsolated() (Delivered, error) {
	if s.set.contextErr != nil && s.label == "context" {
		return nil, s.set.contextErr
	}
	return s.set.handle(s.label), nil
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
	b.InitLaunch(rec, noopSkills{}, NewBaseContextProvider(), nil, nil)
	return b, rec
}

// newCellBackend wires a LaunchBackend onto a fake CellDelivery whose Build
// returns set (recording the inputs it was handed). rawContext/contextHook mirror
// the per-engine CellDelivery flags. The lifecycle is a real BaseLifecycle so
// mergedState resolves the merged hooks/MCP the surface inputs carry.
func newCellBackend(set *recordSet, rawContext, contextHook bool) (*LaunchBackend, *flushSpy) {
	spy := &flushSpy{}
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(NewBaseLifecycle("test", spy.write), noopSkills{}, NewBaseContextProvider(), nil,
		&CellDelivery{
			Build: func(in SurfaceInputs, isolatedDir string) SurfaceSet {
				set.inputs = in
				set.isolatedDir = isolatedDir
				return set
			},
			RawContext:  rawContext,
			ContextHook: contextHook,
		})
	return b, spy
}

// flushSpy is a WriteSettingsFunc that records whether Flush called it, so a
// cell-path test can assert Flush was never invoked (surfaces write settings).
type flushSpy struct{ called bool }

func (s *flushSpy) write(_ string, _ *wire.HooksConfig, _ *wire.MCPConfig, _ map[string]wire.MCPServer, _ string, _ ...SettingsOption) error {
	s.called = true
	return nil
}

// ---- legacy (acp) path ------------------------------------------------------

// TestSetup_LegacyPath_KeepsContextHash proves a backend WITHOUT a CellDelivery
// (acp) takes the unchanged lifecycle path: it keeps the context hash so its
// SessionStart hook injects context, and flushes.
func TestSetup_LegacyPath_KeepsContextHash(t *testing.T) {
	b, rec := newLegacyBackend()
	require.Nil(t, b.delivery, "legacy backend must have no cell delivery")

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "project rules"}},
		Managed:   &ManagedConfig{},
	}))

	assert.NotEmpty(t, rec.contextHash,
		"a legacy backend keeps the hash so its SessionStart hook injects context")
	assert.True(t, rec.flushed, "legacy path flushes hooks + MCP to the settings file")
	assert.Empty(t, b.delivered, "legacy path collects no delivery handles")
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
	b, spy := newCellBackend(set, false, false)

	ephem, err := paths.HarpEphemeralDir("perky-same-chevy")
	require.NoError(t, err)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Env:       map[string]string{SessionHarpEnv: "perky-same-chevy"},
		Fragments: []*Fragment{{Content: "project rules"}},
		CellKind:  CellKindShared,
		Managed: &ManagedConfig{
			ManageStatusline: true,
			Skills:           []CommandExport{{Name: "demo"}},
			Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
				SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
			}},
			MCP:       &wire.MCPConfig{Servers: map[string]wire.MCPServer{"srv": {Command: "run"}}},
			BundleMCP: map[string]wire.MCPServer{"bundle-srv": {Command: "brun"}},
		},
	}))

	assert.False(t, spy.called, "cell path must never call Flush (surfaces write settings)")
	assert.Equal(t, "project rules", set.inputs.Context, "assembled context string routed to Build")
	assert.Equal(t, ephem, set.isolatedDir, "the out-of-cwd dir is the harp's PRIVATE ephemeral dir")
	assert.True(t, set.usedShared, "a SharedCell delivers via the shared-cwd (race-safe) set")
	assert.Equal(t, []CommandExport{{Name: "demo"}}, set.inputs.Skills, "skills routed through inputs")
	require.NotNil(t, set.inputs.Hooks, "merged hooks routed through inputs")
	assert.NotEmpty(t, set.inputs.Hooks.Unified.SessionStart, "merged hooks carry the session-bind hook")
	assert.True(t, set.inputs.ManageStatusline, "manageStatusline mirrors the managed config")
	require.NotNil(t, set.inputs.MCP, "merged MCP routed through inputs")
	assert.Contains(t, set.inputs.MCP.Servers, "srv", "merged MCP carries the managed server")
	assert.Equal(t, map[string]wire.MCPServer{"bundle-srv": {Command: "brun"}}, set.inputs.BundleMCP,
		"bundle MCP passed straight through")
	for _, h := range set.inputs.Hooks.Unified.SessionStart {
		assert.Empty(t, h.ContextHash,
			"no context-injection hook: MergeManaged was fed an empty hash")
	}
	require.Len(t, b.delivered, 4, "all four surfaces collected via the seam")
}

// ---- cell path: isolated cell -----------------------------------------------

// TestSetup_IsolatedCell_UsesWellKnownSet proves an isolated cell delivers the
// plain (well-known) surface set into the private working dir — not the shared-cwd
// race-safe set — and records every handle.
func TestSetup_IsolatedCell_UsesWellKnownSet(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	b, _ := newCellBackend(set, false, false)
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
	require.Len(t, b.delivered, 4, "all four surfaces collected")
}

// ---- cell path: RawContext (codex/agy/kiro) ---------------------------------

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
	b.InitLaunch(rec, noopSkills{}, NewBaseContextProvider(), nil, &CellDelivery{
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
	b, _ := newCellBackend(set, true, false)

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
	b, _ := newCellBackend(set, false, false)

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
	assert.Len(t, b.delivered, 3, "context handle skipped; mcp/settings/skills still delivered")
}

// ---- cleanup ----------------------------------------------------------------

// TestCleanup_RunsDeliveredHandlesLIFO proves Cleanup reverses every delivered
// surface in last-in-first-out order and clears the handle set.
func TestCleanup_RunsDeliveredHandlesLIFO(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	b, _ := newCellBackend(set, false, false)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "rules"}},
		CellKind:  CellKindShared,
		Managed:   &ManagedConfig{},
	}))

	require.NoError(t, b.Cleanup(context.Background()))
	// Delivery order was context, mcp, settings, skills → LIFO reverses it.
	assert.Equal(t, []string{"skills", "settings", "mcp", "context"}, order, "Cleanup runs handles LIFO")
	assert.Empty(t, b.delivered, "Cleanup clears the handle set")
}

// TestCleanup_AttemptsAllReturnsFirstError proves Cleanup keeps going after a
// handle errors and reports the first failure.
func TestCleanup_AttemptsAllReturnsFirstError(t *testing.T) {
	boom := errors.New("boom")
	var ran int
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	// Cleanup undoes LIFO (last appended, first undone). The last-appended handle
	// errors → it is the first error encountered; a later handle also errors but
	// must not overwrite it, and the clean handle must still run.
	b.delivered = []Delivered{
		deliveredFn(func() error { ran++; return errors.New("undone-last") }), // undone last
		deliveredFn(func() error { ran++; return nil }),                       // undone second
		deliveredFn(func() error { ran++; return boom }),                      // undone first → first error
	}

	err := b.Cleanup(context.Background())
	assert.Equal(t, boom, err, "Cleanup returns the FIRST error encountered in LIFO order")
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
