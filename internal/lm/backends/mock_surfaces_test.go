package backends

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file hermetically proves the mock engine's context route — both
// halves of the delivery seam (docs/design/engine-delivery-seam.design.md).
// ctxloom's characteristic bug is the SILENT NO-OP: exit 0, a success
// message, zero bytes written. Every assertion below is on the actual
// delivered BYTES (or their deliberate absence), never on an error being nil.

// TestMockContextSurface_Deliver_WritesActualBytes is the base payload
// assertion: Deliver must land the CONTENT itself inside MOCK_CONTEXT.md's
// managed section, not merely create a file or return a nil error. A
// silent-no-op Deliver (writes an empty/templated file regardless of
// content) would pass a "file exists" check and fail this one.
func TestMockContextSurface_Deliver_WritesActualBytes(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	s := &mockContextSurface{context: "MOCK-PAYLOAD-9f3a", fs: fs}
	handle, err := s.Deliver(dir)
	require.NoError(t, err)
	require.NotNil(t, handle)

	got, err := afero.ReadFile(fs, mockContextPath(dir))
	require.NoError(t, err)
	assert.Contains(t, string(got), "MOCK-PAYLOAD-9f3a",
		"the delivered file must carry the actual composed bytes")
	assert.Contains(t, string(got), agent.ManagedContextBegin, "content must sit inside the managed markers")
	assert.Contains(t, string(got), agent.ManagedContextEnd)
}

// TestMockContextSurface_Deliver_EmptyContext_WritesNothing is the silent-no-op
// guard's other half: an empty compose must not fabricate a file. Asserting
// only "err == nil" here would pass even if Deliver wrote a stray empty
// MOCK_CONTEXT.md — the trap this project's own bug class takes.
func TestMockContextSurface_Deliver_EmptyContext_WritesNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	s := &mockContextSurface{context: "", fs: fs}
	handle, err := s.Deliver(dir)
	require.NoError(t, err)
	_ = handle

	exists, err := afero.Exists(fs, mockContextPath(dir))
	require.NoError(t, err)
	assert.False(t, exists, "empty context must write NOTHING — no stray MOCK_CONTEXT.md")
}

// TestMockContextSurface_Cleanup_RemovesFileWhenNothingElseRemains proves the
// reversal is real: Cleanup must strip the bytes it wrote, not merely
// succeed. A no-op Cleanup that returns nil without touching the file would
// pass an error check and fail this one.
func TestMockContextSurface_Cleanup_RemovesFileWhenNothingElseRemains(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	s := &mockContextSurface{context: "TEMPORARY-CONTENT", fs: fs}
	handle, err := s.Deliver(dir)
	require.NoError(t, err)

	require.NoError(t, handle.Cleanup())

	exists, err := afero.Exists(fs, mockContextPath(dir))
	require.NoError(t, err)
	assert.False(t, exists, "cleanup must remove the file once nothing user-authored remains")
}

// TestMockContextSurface_Deliver_PreservesUserContentOutsideMarkers is the
// property the whole managed-marker convention exists for: a user's own
// prose living OUTSIDE the markers must survive a Deliver byte-for-byte. A
// Deliver that clobbered the whole file (the historical claude data-loss
// bug this shared core was built to close) would destroy it silently.
func TestMockContextSurface_Deliver_PreservesUserContentOutsideMarkers(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))
	path := mockContextPath(dir)
	userLine := "MY OWN NOTES — do not touch"
	require.NoError(t, afero.WriteFile(fs, path, []byte(userLine+"\n"), 0o644))

	s := &mockContextSurface{context: "ctxloom-managed-body", fs: fs}
	_, err := s.Deliver(dir)
	require.NoError(t, err)

	got, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Contains(t, string(got), userLine, "user content outside the markers must survive byte-for-byte")
	assert.Contains(t, string(got), "ctxloom-managed-body")
}

// TestMockContextSurface_State_ReportsMissing_WhenFileAbsent proves the read
// half's missing verdict when the route was never delivered at all.
func TestMockContextSurface_State_ReportsMissing_WhenFileAbsent(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	s := &mockContextSurface{fs: fs}
	state, err := s.State(dir)
	require.NoError(t, err)

	assert.Equal(t, mockContextFilename, state.Route())
	got := state.Currency("whatever the intended context is")
	assert.Equal(t, agent.StatusMissing, got.Status)
}

// TestMockContextSurface_State_ReportsMissing_WhenFileExistsWithoutManagedSection
// proves the read half does not confuse "a file the user wrote" with "a
// route ctxloom ever delivered" — a plain file with no markers is missing,
// not stale and not delivered.
func TestMockContextSurface_State_ReportsMissing_WhenFileExistsWithoutManagedSection(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))
	require.NoError(t, afero.WriteFile(fs, mockContextPath(dir), []byte("just a user file, no markers\n"), 0o644))

	s := &mockContextSurface{fs: fs}
	state, err := s.State(dir)
	require.NoError(t, err)

	got := state.Currency("anything")
	assert.Equal(t, agent.StatusMissing, got.Status)
}

// TestMockContextSurface_State_ReportsDelivered_WhenManagedSectionMatches is
// the round-trip proof: Deliver, then State+Currency against the SAME
// intended content, must agree it is current.
func TestMockContextSurface_State_ReportsDelivered_WhenManagedSectionMatches(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	s := &mockContextSurface{context: "CURRENT-COMPOSITION", fs: fs}
	_, err := s.Deliver(dir)
	require.NoError(t, err)

	state, err := s.State(dir)
	require.NoError(t, err)
	got := state.Currency("CURRENT-COMPOSITION")
	assert.Equal(t, agent.StatusDelivered, got.Status)
}

// TestMockContextSurface_State_ReportsStale_WhenManagedSectionDiffersFromIntended
// proves drift detection: what is on disk no longer matches what would be
// composed today.
func TestMockContextSurface_State_ReportsStale_WhenManagedSectionDiffersFromIntended(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	s := &mockContextSurface{context: "OLD-COMPOSITION", fs: fs}
	_, err := s.Deliver(dir)
	require.NoError(t, err)

	state, err := s.State(dir)
	require.NoError(t, err)
	got := state.Currency("NEW-COMPOSITION-AFTER-A-FRAGMENT-CHANGED")
	assert.Equal(t, agent.StatusStale, got.Status)
}

// TestMockContextSurface_State_IgnoresUserContentOutsideMarkersForCurrency is
// the FileDeliveryState guarantee stated explicitly in the design: a user's
// own prose outside the markers must never make a current managed section
// read as stale. This is the payload-level proof that Currency compares the
// MANAGED SECTION ONLY.
func TestMockContextSurface_State_IgnoresUserContentOutsideMarkersForCurrency(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	s := &mockContextSurface{context: "STABLE-COMPOSITION", fs: fs}
	_, err := s.Deliver(dir)
	require.NoError(t, err)

	// A user hand-edits the file, adding prose OUTSIDE the managed markers —
	// exactly what the marker convention exists to permit.
	path := mockContextPath(dir)
	existing, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	require.NoError(t, afero.WriteFile(fs, path, append([]byte("# My own heading\n\n"), existing...), 0o644))

	state, err := s.State(dir)
	require.NoError(t, err)
	got := state.Currency("STABLE-COMPOSITION")
	assert.Equal(t, agent.StatusDelivered, got.Status,
		"user content outside the markers must never make a current managed section read as drift")
}

// TestMockContextSurface_IsTheSameObjectThroughSurfaceFor proves the design's
// "the object that applied answers for it" property directly: resolving the
// context surface via MockSurfaces.SurfaceFor (the path a real caller takes)
// yields the SAME *mockContextSurface State() reads through — not a second,
// independently-constructed reporting path that could disagree with the one
// that delivered.
func TestMockContextSurface_IsTheSameObjectThroughSurfaceFor(t *testing.T) {
	fs := afero.NewMemMapFs()
	set := NewMockSurfaces(agent.SurfaceInputs{Context: "X"}, fs)

	resolved, err := set.SurfaceFor(agent.SurfaceContext, agent.ApproachUnsafeFile)
	require.NoError(t, err)

	reader, ok := resolved.(agent.StateReader)
	require.True(t, ok, "the resolved context Delivery must also implement agent.StateReader")
	assert.Same(t, set.Context, reader, "SurfaceFor must resolve to the SAME instance NewMockSurfaces built, not a copy")
}

// TestMockSurfaces_SupportedApproaches_OnlyContext pins mock's declared scope:
// context is supported, every other SurfaceKind is absent (folded/unsupported
// for mock, matching how codex declares no MCP surface) — never silently
// materializing a surface this change did not build.
func TestMockSurfaces_SupportedApproaches_OnlyContext(t *testing.T) {
	set := NewMockSurfaces(agent.SurfaceInputs{Context: "X"}, afero.NewMemMapFs())

	assert.NotEmpty(t, set.SupportedApproaches(agent.SurfaceContext))
	for _, kind := range []agent.SurfaceKind{agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		assert.Empty(t, set.SupportedApproaches(kind), "mock declares no %s surface", kind)
	}
}

// TestMockSurfaces_WithEverything_MaterializesExactlyOneFile is the
// end-to-end payload proof through the SAME builder path a real caller
// (materialize) uses: WithEverything + DeliverUnder must land exactly one
// file, carrying the composed bytes.
func TestMockSurfaces_WithEverything_MaterializesExactlyOneFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	set := NewMockSurfaces(agent.SurfaceInputs{Context: "END-TO-END-MARKER"}, fs)
	delivered, kinds, errs := agent.Select(set).WithEverything().DeliverUnder(dir)
	require.Empty(t, errs)
	require.Len(t, delivered, 1)
	require.Equal(t, []agent.SurfaceKind{agent.SurfaceContext}, kinds)

	got, err := afero.ReadFile(fs, filepath.Join(dir, mockContextFilename))
	require.NoError(t, err)
	assert.Contains(t, string(got), "END-TO-END-MARKER")
}
