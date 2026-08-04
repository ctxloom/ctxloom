package convert

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/content"
)

// authoredSkillBundle stages one bundle directory holding a skill package whose
// scripts/run.sh is committed 0755 and whose SKILL.md is 0644 — the ordinary
// shape of an authored package, and the one the derivation has to read.
func authoredSkillBundle(t *testing.T) (afero.Fs, string, *bundles.Bundle) {
	t.Helper()
	fsys := afero.NewMemMapFs()
	dir := "/src/vault"
	require.NoError(t, fsys.MkdirAll(dir+"/skills/reviewer/scripts", 0o755))
	require.NoError(t, afero.WriteFile(fsys, dir+"/skills/reviewer/SKILL.md",
		[]byte("---\nname: reviewer\ndescription: D\n---\n\nBODY\n"), 0o644))
	require.NoError(t, afero.WriteFile(fsys, dir+"/skills/reviewer/scripts/run.sh",
		[]byte("#!/bin/sh\necho hi\n"), 0o755))
	// MemMapFs applies the mode on create, but say it a second time: the
	// derivation reads the mode, so a fixture that only *asked* for 0755 would
	// pass whatever the walk actually saw.
	require.NoError(t, fsys.Chmod(dir+"/skills/reviewer/scripts/run.sh", 0o755))
	return fsys, dir, &bundles.Bundle{
		Name:    "vault",
		Version: "1.0.0",
		Skills:  map[string]bundles.BundleSkill{"reviewer": {}},
	}
}

// TestSkillFilesFromDir_DerivesTheDeclarationFromTheCommittedMode is the
// authoring half of the exec-bit decision: nobody hand-writes the declaration,
// so the committed mode becomes it.
func TestSkillFilesFromDir_DerivesTheDeclarationFromTheCommittedMode(t *testing.T) {
	fsys, dir, b := authoredSkillBundle(t)

	files, err := SkillFilesFromDir(fsys, dir, b)("reviewer")
	require.NoError(t, err)

	modes := map[string]content.ComponentMode{}
	for _, f := range files {
		modes[f.Path] = f.Mode
	}
	assert.Equal(t, content.ModeExecutable, modes["scripts/run.sh"],
		"a file committed 0755 must be DECLARED executable, or the manifest claims 0644 and the script never runs")
	assert.Equal(t, content.ModeRegular, modes["SKILL.md"],
		"a plain file must not be declared executable")

	byPath := map[string][]byte{}
	for _, f := range files {
		byPath[f.Path] = f.Bytes
	}
	assert.Equal(t, "#!/bin/sh\necho hi\n", string(byPath["scripts/run.sh"]),
		"the bytes must be the package's own, not a stat result")
}

// TestConvert_WritesTheDerivedDeclarationIntoTheSidecar closes the loop the
// decision actually asked for: the `executable:` line has to end up in
// skills/.<name>.meta.yaml, which is the hashed, signed carrier. Asserting the
// in-memory ComponentMode alone would pass with a sidecar that never mentions
// the file.
func TestConvert_WritesTheDerivedDeclarationIntoTheSidecar(t *testing.T) {
	src, dir, b := authoredSkillBundle(t)
	st, treeFS := newStore(t)

	require.NoError(t, Convert(t.Context(), st, "vault", b, Options{
		SkillFiles: SkillFilesFromDir(src, dir, b),
	}))

	sidecar, err := afero.ReadFile(treeFS, "/tree/vault/skills/.reviewer.meta.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(sidecar), "executable:",
		"the sidecar carries no declaration at all, so the package's script is 0644 to every consumer")
	assert.Contains(t, string(sidecar), "scripts/run.sh")

	// And the reverse: the file the author left alone must NOT be declared.
	execBlock := string(sidecar[strings.Index(string(sidecar), "executable:"):])
	assert.NotContains(t, execBlock, "SKILL.md",
		"declaring a plain file executable is the same divergence in the other direction")
}

// TestSkillFilesFromDir_HonoursAnExplicitPath: a skill may live somewhere other
// than skills/<name>, and a reader that assumed the default would convert an
// EMPTY package for it — which converts, signs and materializes while
// delivering nothing.
func TestSkillFilesFromDir_HonoursAnExplicitPath(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/src/vault"
	require.NoError(t, fsys.MkdirAll(dir+"/elsewhere/pkg", 0o755))
	require.NoError(t, afero.WriteFile(fsys, dir+"/elsewhere/pkg/SKILL.md", []byte("BODY\n"), 0o644))
	b := &bundles.Bundle{Skills: map[string]bundles.BundleSkill{"reviewer": {Path: "elsewhere/pkg"}}}

	files, err := SkillFilesFromDir(fsys, dir, b)("reviewer")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "SKILL.md", files[0].Path)
}

// TestSkillFilesFromDir_MissingPackageIsAnError. A skill declared in the
// document with no directory on disk must stop the conversion: an empty package
// is indistinguishable from a working one until a model asks for it.
func TestSkillFilesFromDir_MissingPackageIsAnError(t *testing.T) {
	fsys := afero.NewMemMapFs()
	b := &bundles.Bundle{Skills: map[string]bundles.BundleSkill{"reviewer": {}}}

	_, err := SkillFilesFromDir(fsys, "/src/vault", b)("reviewer")
	require.Error(t, err)
}
