package backends

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// This file hermetically proves the mock engine's context and skills routes —
// both halves of the delivery seam (docs/design/engine-delivery-seam.design.md).
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

	s := mockContext("MOCK-PAYLOAD-9f3a", fs)
	handle, err := s.Deliver(present.ProjectOnHost(dir))
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

	s := mockContext("", fs)
	handle, err := s.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)
	_ = handle

	exists, err := afero.Exists(fs, mockContextPath(dir))
	require.NoError(t, err)
	assert.False(t, exists, "empty context must write NOTHING — no stray MOCK_CONTEXT.md")
}

// TestMockContextSurface_Cleanup_LeavesTheFileInPlace pins the INVERTED
// contract, ruled by the human 2026-09-10: a delivered project surface outlives
// the run.
//
// This test previously proved the reversal was real — that Cleanup stripped the
// bytes rather than merely returning nil. It is kept, inverted, rather than
// deleted, because the underlying worry is unchanged: a Cleanup nobody asserts
// can drift either way, and exit-time removal returning by some other route
// must redden something.
//
// The reason for the inversion, so it is not read as a weakened test: exit
// cleanup never ran on SIGKILL, a crash, or a container stop, so leftover
// surfaces had to be tolerated regardless. Removing on a clean exit only made
// what a session left behind depend on how it ended.
func TestMockContextSurface_Cleanup_LeavesTheFileInPlace(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	s := mockContext("DELIVERED-CONTENT", fs)
	handle, err := s.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)

	before, err := afero.ReadFile(fs, mockContextPath(dir))
	require.NoError(t, err, "precondition: Deliver wrote the surface")

	require.NoError(t, handle.Cleanup())

	after, err := afero.ReadFile(fs, mockContextPath(dir))
	require.NoError(t, err, "cleanup must NOT remove a project surface: startup reconciles it, and clean/uninstall are the commands that remove it")
	assert.Equal(t, string(before), string(after),
		"cleanup must leave the surface byte-identical — a partial strip is the same defect as a removal")
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

	s := mockContext("ctxloom-managed-body", fs)
	_, err := s.Deliver(present.ProjectOnHost(dir))
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

	s := mockContext("", fs)
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

	s := mockContext("", fs)
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

	s := mockContext("CURRENT-COMPOSITION", fs)
	_, err := s.Deliver(present.ProjectOnHost(dir))
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

	s := mockContext("OLD-COMPOSITION", fs)
	_, err := s.Deliver(present.ProjectOnHost(dir))
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

	s := mockContext("STABLE-COMPOSITION", fs)
	_, err := s.Deliver(present.ProjectOnHost(dir))
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

// contextApproach is what the mock's context approach is to a test: it
// delivers AND reads its own state back (agent.StateReader) — "the object that
// applied answers for it".
type contextApproach interface {
	agent.Approach
	agent.StateReader
}

// mockContext constructs the mock's native-file context approach for content.
func mockContext(content string, fs afero.Fs) contextApproach {
	return newMockContext(agent.SurfaceInputs{Context: content}, fs).(contextApproach)
}

// TestMockContext_ResolvedApproachReadsItsOwnState proves the design's "the
// object that applied answers for it" property through the path a real caller
// takes: the context approach Build constructs is the one that implements
// agent.StateReader — not a second, independently-constructed reporting path
// that could disagree with the one that delivered.
func TestMockContext_ResolvedApproachReadsItsOwnState(t *testing.T) {
	resolved, err := agent.Select(mockDeclaration(config.BackendMock)).With(agent.SurfaceContext, agent.ApproachUnsafeFile).Build(agent.SurfaceInputs{Context: "X"}, afero.NewMemMapFs())
	require.NoError(t, err)
	approaches := resolved.Approaches()
	require.Len(t, approaches, 1)
	_, ok := approaches[0].Approach.(agent.StateReader)
	require.True(t, ok, "the resolved context approach must also implement agent.StateReader")
}

// TestMockDeclaration_DeclaresEveryKind pins mock's declared scope: EVERY
// SurfaceKind is declared, because mock is a complete engine with no real
// model behind it rather than a partial one.
//
// The completeness is load-bearing, not tidiness: a partial double makes its
// gaps load-bearing somewhere else, where nothing states that they are.
//
// Each kind must also CONSTRUCT a concrete approach: declaring a name and
// then failing to build would be a surface that exists only in the roster.
func TestMockDeclaration_DeclaresEveryKind(t *testing.T) {
	decl := mockDeclaration(config.BackendMock)

	for _, kind := range []agent.SurfaceKind{
		agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings,
		agent.SurfaceCommands, agent.SurfaceSkills,
	} {
		require.NotEmpty(t, decl.Names(kind), "mock must declare a %s surface", kind)

		def, ok := decl.Default(kind)
		require.True(t, ok, "%s must have a default approach", kind)
		a, ok := decl.Construct(kind, def, agent.SurfaceInputs{Context: "X"}, afero.NewMemMapFs())
		require.True(t, ok, "%s must construct at its default", kind)
		require.NotNil(t, a, "%s constructed a nil approach", kind)
	}
}

// TestMockSurfaces_WithEverything_MaterializesEverySurface is the end-to-end
// payload proof through the SAME builder path a real caller (materialize)
// uses: WithEverything + DeliverUnder must land EVERY surface's bytes, each at
// the path its route promises.
//
// Asserting the delivered COUNT as well as the bytes is deliberate: a set that
// silently dropped one kind would still satisfy every individual file
// assertion below, and the count is the only thing that notices.
func TestMockSurfaces_WithEverything_MaterializesEverySurface(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	delivered, kinds, errs := projectForms(agent.Select(mockDeclaration(config.BackendMock)).WithEverything()).DeliverUnder(agent.SurfaceInputs{
		Context:   "END-TO-END-MARKER",
		Skills:    []agent.SkillExport{reviewerSkillExport()},
		BundleMCP: map[string]wire.MCPServer{"postgres": {Command: "mcp-postgres"}},
		Hooks:     &wire.HooksConfig{},
		Commands:  []agent.CommandExport{{Name: "review", Content: "REVIEW-COMMAND-BODY", Enabled: true}},
	}, fs, present.ProjectOnHost(dir))
	require.Empty(t, errs)
	require.Len(t, delivered, 5, "every declared surface must actually deliver")
	require.ElementsMatch(t, []agent.SurfaceKind{
		agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings,
		agent.SurfaceCommands, agent.SurfaceSkills,
	}, kinds)

	got, err := afero.ReadFile(fs, filepath.Join(dir, mockContextFilename))
	require.NoError(t, err)
	assert.Contains(t, string(got), "END-TO-END-MARKER")

	skill, err := afero.ReadFile(fs, filepath.Join(mockSkillsPath(dir), "reviewer", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(skill), "MOCK-SKILL-BODY-2c7e")

	mcp, err := afero.ReadFile(fs, mockMCPPath(dir))
	require.NoError(t, err)
	assert.Contains(t, string(mcp), "mcp-postgres")

	settings, err := afero.ReadFile(fs, mockSettingsPath(dir))
	require.NoError(t, err)
	assert.Contains(t, string(settings), "hooks")

	cmd, err := afero.ReadFile(fs, filepath.Join(mockCommandsPath(dir), "review.md"))
	require.NoError(t, err)
	assert.Contains(t, string(cmd), "REVIEW-COMMAND-BODY")
}

// ---------------------------------------------------------------------------
// THE SKILLS SURFACE
//
// Same discipline as the context half above: every assertion is on delivered
// BYTES and delivered MODE, never on a nil error. A skills surface that
// reports success and materializes an empty tree — or a tree whose script is
// not executable and therefore cannot run — is the exact silent no-op this
// project keeps producing.
// ---------------------------------------------------------------------------

// reviewerSkillExport is the fixture package the skills tests below deliver:
// SKILL.md plus an executable script, i.e. the shape J001400's delivery matrix
// asserts (skills/reviewer/SKILL.md at 0644, skills/reviewer/scripts/run.sh at
// 0755).
//
// The modes here are the export's DECLARATION. On the real path they arrive
// from the package sidecar's `executable:` list, through the signed manifest,
// into bundles.LoadedSkillFile.Mode and then agent.PackageFile.Mode — no stage
// of which stats a file to decide them (a mode bit is not portable and the
// package digest deliberately excludes it). This fixture states them the same
// way, in the export, so the assertions below are on the declaration reaching
// disk.
func reviewerSkillExport() agent.SkillExport {
	return agent.SkillExport{
		Name:        "reviewer",
		Description: "the fixture skill",
		Enabled:     true,
		Files: []agent.PackageFile{
			{RelPath: "SKILL.md", Content: []byte("MOCK-SKILL-BODY-2c7e"), Mode: 0o644},
			{RelPath: "scripts/run.sh", Content: []byte("#!/bin/sh\necho MOCK-SCRIPT-9a41\n"), Mode: 0o755},
		},
	}
}

// TestMockSkillsSurface_Deliver_WritesEveryFileWithItsBytes is the base payload
// assertion: Deliver must land each file of the package, with its own content,
// under the skill's own directory. A surface that created the tree but wrote
// empty files would pass a "path exists" check and fail this one.
func TestMockSkillsSurface_Deliver_WritesEveryFileWithItsBytes(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	s := newMockSkillsSurface(agent.SurfaceInputs{Skills: []agent.SkillExport{reviewerSkillExport()}}, fs)
	handle, err := s.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)
	require.NotNil(t, handle)

	skillMD, err := afero.ReadFile(fs, filepath.Join(mockSkillsPath(dir), "reviewer", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "MOCK-SKILL-BODY-2c7e", string(skillMD),
		"the delivered SKILL.md must carry the package's actual bytes")

	script, err := afero.ReadFile(fs, filepath.Join(mockSkillsPath(dir), "reviewer", "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Contains(t, string(script), "MOCK-SCRIPT-9a41",
		"a sibling file in a subdirectory must be delivered too, not just SKILL.md")
}

// TestMockSkillsSurface_Deliver_MaterializesTheDeclaredMode is the mode half of
// the payload assertion, and the reason it is a separate test: a delivered
// script that is not executable is a package that reports success and cannot
// run. The 0755 asserted here comes from the EXPORT's declaration
// (reviewerSkillExport), which on the real path originates in the package
// sidecar's `executable:` list — never from the mode of any file on the
// filesystem, which is why an afero.MemMapFs with no source tree at all can
// produce a correctly-executable delivery.
func TestMockSkillsSurface_Deliver_MaterializesTheDeclaredMode(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	s := newMockSkillsSurface(agent.SurfaceInputs{Skills: []agent.SkillExport{reviewerSkillExport()}}, fs)
	_, err := s.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)

	script, err := fs.Stat(filepath.Join(mockSkillsPath(dir), "reviewer", "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), script.Mode().Perm(),
		"scripts/run.sh is DECLARED executable; the delivered file must be executable or the skill cannot run")

	doc, err := fs.Stat(filepath.Join(mockSkillsPath(dir), "reviewer", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), doc.Mode().Perm(),
		"SKILL.md is DECLARED non-executable; delivering it executable would be the declaration ignored in the other direction")
}

// TestMockSkillsSurface_Deliver_DeclaredModeBeatsAnExistingFilesMode is the
// direct proof that the DECLARATION is authoritative on delivery rather than
// whatever mode happens to be on disk. A previous materialize left run.sh
// non-executable; re-delivering the same declared-executable export must
// correct it. afero.WriteFile applies a mode only at file CREATION, so without
// the shared writer's explicit re-assert this file would silently stay 0644 —
// exec bit lost, exit 0, no complaint.
func TestMockSkillsSurface_Deliver_DeclaredModeBeatsAnExistingFilesMode(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	scriptPath := filepath.Join(mockSkillsPath(dir), "reviewer", "scripts", "run.sh")
	require.NoError(t, fs.MkdirAll(filepath.Dir(scriptPath), 0o755))
	require.NoError(t, afero.WriteFile(fs, scriptPath, []byte("stale\n"), 0o600))

	s := newMockSkillsSurface(agent.SurfaceInputs{Skills: []agent.SkillExport{reviewerSkillExport()}}, fs)
	_, err := s.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)

	got, err := fs.Stat(scriptPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), got.Mode().Perm(),
		"the DECLARED mode must win over the mode the file already had on the filesystem")
}

// TestMockSkillsSurface_Deliver_DisabledSkillWritesNothing is the silent-no-op
// guard's other half: a skill the export marks disabled must leave no tree
// behind. Asserting only "err == nil" would pass even if the package were
// materialized anyway.
func TestMockSkillsSurface_Deliver_DisabledSkillWritesNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	disabled := reviewerSkillExport()
	disabled.Enabled = false
	s := newMockSkillsSurface(agent.SurfaceInputs{Skills: []agent.SkillExport{disabled}}, fs)
	_, err := s.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)

	exists, err := afero.DirExists(fs, mockSkillsPath(dir))
	require.NoError(t, err)
	assert.False(t, exists, "a disabled skill must write NOTHING — not even an empty skills directory")
}

// TestMockSkillsSurface_Cleanup_LeavesWhatItWroteInPlace pins the INVERTED
// contract (human, 2026-09-10): a materialized skill package outlives the run
// that delivered it, like every other project surface.
//
// HONEST NOTE ON ITS SIBLING BELOW: with cleanup a no-op,
// TestMockSkillsSurface_Cleanup_LeavesUserAuthoredFilesAlone is now trivially
// satisfied — nothing is removed, so of course a user's files survive. It is
// left in place rather than deleted because it still guards the manifest's
// existence, but it should NOT be read as evidence that selective removal
// works. Nothing exercises selective removal any more, because nothing
// performs it.
func TestMockSkillsSurface_Cleanup_LeavesWhatItWroteInPlace(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	s := newMockSkillsSurface(agent.SurfaceInputs{Skills: []agent.SkillExport{reviewerSkillExport()}}, fs)
	handle, err := s.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)
	before, err := afero.Exists(fs, filepath.Join(mockSkillsPath(dir), "reviewer", "SKILL.md"))
	require.NoError(t, err)
	require.True(t, before, "precondition: Deliver wrote the package")

	require.NoError(t, handle.Cleanup())

	for _, rel := range []string{"reviewer/SKILL.md", "reviewer/scripts/run.sh"} {
		exists, err := afero.Exists(fs, filepath.Join(mockSkillsPath(dir), filepath.FromSlash(rel)))
		require.NoError(t, err)
		assert.True(t, exists,
			"cleanup must LEAVE %s: a delivered skill package is a project surface, and startup reconciles it rather than exit removing it", rel)
	}
}

// TestMockSkillsSurface_Cleanup_LeavesUserAuthoredFilesAlone is the property
// the manifest exists for: the skills directory is shared territory, so a
// reversal must remove exactly ctxloom's own writes and nothing a user put
// there. A cleanup that wiped the directory wholesale would pass every
// assertion above and destroy the user's file.
func TestMockSkillsSurface_Cleanup_LeavesUserAuthoredFilesAlone(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/target"
	require.NoError(t, fs.MkdirAll(mockSkillsPath(dir), 0o755))
	userFile := filepath.Join(mockSkillsPath(dir), "mine", "SKILL.md")
	require.NoError(t, fs.MkdirAll(filepath.Dir(userFile), 0o755))
	require.NoError(t, afero.WriteFile(fs, userFile, []byte("USER-AUTHORED-4f10"), 0o644))

	s := newMockSkillsSurface(agent.SurfaceInputs{Skills: []agent.SkillExport{reviewerSkillExport()}}, fs)
	handle, err := s.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)
	require.NoError(t, handle.Cleanup())

	got, err := afero.ReadFile(fs, userFile)
	require.NoError(t, err)
	assert.Equal(t, "USER-AUTHORED-4f10", string(got),
		"a user's own skill package must survive ctxloom's reversal byte-for-byte")
}

// projectForms selects the PROJECT form of every mock surface: an at-rest
// delivery into a target dir advises no Scratch, so the session default
// (MockSessionFile) has nowhere to land there and refuses. The project form
// is the explicit choice such a delivery makes.
func projectForms(sel *agent.SurfaceSelection) *agent.SurfaceSelection {
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		sel = sel.With(kind, agent.ApproachUnsafeFile)
	}
	return sel
}

// TestMockSessionForm_RefusesAnAtRestDelivery: with no Scratch advised the
// session form refuses rather than writing a bare relative path wherever
// the process happens to be.
func TestMockSessionForm_RefusesAnAtRestDelivery(t *testing.T) {
	fs := afero.NewMemMapFs()
	_, _, errs := agent.Select(mockDeclaration(config.BackendMock)).WithEverything().DeliverUnder(agent.SurfaceInputs{
		Context: "X", Hooks: &wire.HooksConfig{}, BundleMCP: map[string]wire.MCPServer{},
	}, fs, present.ProjectOnHost("/target"))
	require.NotEmpty(t, errs)
	for _, err := range errs {
		assert.ErrorIs(t, err, agent.ErrUnrootedDelivery)
	}
	entries, _ := afero.ReadDir(fs, "/")
	assert.Empty(t, entries, "nothing was written anywhere")
}
