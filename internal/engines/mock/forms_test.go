package mock

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file hermetically proves the mock engine's context and skills routes —
// both halves of the delivery seam (docs/design/engine-delivery-seam.design.md).
// ctxloom's characteristic bug is the SILENT NO-OP: exit 0, a success
// message, zero bytes written. Every assertion below is on the actual
// delivered BYTES (or their deliberate absence), never on an error being nil.

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
	decl := New().(Mock).Declaration()

	for _, kind := range []agent.SurfaceKind{
		agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings,
		agent.SurfaceCommands, agent.SurfaceSkills,
	} {
		require.NotEmpty(t, decl.Names(kind), "mock must declare a %s surface", kind)

		def, ok := decl.Default(kind)
		require.True(t, ok, "%s must have a default approach", kind)
		a, ok := decl[kind].Construct(def, agent.SurfaceInputs{Context: "X"}, safefs.NewMem(afero.NewMemMapFs()))
		require.True(t, ok, "%s must construct at its default", kind)
		require.NotNil(t, a, "%s constructed a nil approach", kind)
	}
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

	s := newMockSkillsSurface(agent.SurfaceInputs{Skills: []agent.SkillExport{reviewerSkillExport()}}, safefs.NewMem(fs)).(*agent.ManagedSkillPackagesDelivery)
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

	s := newMockSkillsSurface(agent.SurfaceInputs{Skills: []agent.SkillExport{reviewerSkillExport()}}, safefs.NewMem(fs)).(*agent.ManagedSkillPackagesDelivery)
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

	s := newMockSkillsSurface(agent.SurfaceInputs{Skills: []agent.SkillExport{reviewerSkillExport()}}, safefs.NewMem(fs)).(*agent.ManagedSkillPackagesDelivery)
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
	s := newMockSkillsSurface(agent.SurfaceInputs{Skills: []agent.SkillExport{disabled}}, safefs.NewMem(fs)).(*agent.ManagedSkillPackagesDelivery)
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

	s := newMockSkillsSurface(agent.SurfaceInputs{Skills: []agent.SkillExport{reviewerSkillExport()}}, safefs.NewMem(fs)).(*agent.ManagedSkillPackagesDelivery)
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

	s := newMockSkillsSurface(agent.SurfaceInputs{Skills: []agent.SkillExport{reviewerSkillExport()}}, safefs.NewMem(fs)).(*agent.ManagedSkillPackagesDelivery)
	handle, err := s.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)
	require.NoError(t, handle.Cleanup())

	got, err := afero.ReadFile(fs, userFile)
	require.NoError(t, err)
	assert.Equal(t, "USER-AUTHORED-4f10", string(got),
		"a user's own skill package must survive ctxloom's reversal byte-for-byte")
}
