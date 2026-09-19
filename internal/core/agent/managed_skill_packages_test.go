package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/ledger"
)

// A skill package may carry one body per form, and the process stage selects
// one before the package reaches this writer: the export it hands over has the
// selected body's bytes AT SKILL.md and no file for the other body. The writer
// is content-agnostic, so this pins the two things that selection relies on
// it for: a re-materialization that substitutes SKILL.md's bytes replaces the
// file, and a file the export no longer names (the other body, which an
// earlier export may have shipped as a plain sibling) is removed from disk
// and from the ledger.
func TestWriteManagedSkillPackages_SubstitutedBodyReplacesTheDescriptorAndDropsTheUnselectedOne(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/work/.claude/skills"
	raw := []byte("---\nname: humanize\ndescription: long\n---\nthe full body\n")
	distilled := []byte("---\nname: humanize\ndescription: short\n---\nthe short body\n")
	script := []byte("#!/bin/sh\necho hi\n")
	export := func(files ...PackageFile) []SkillExport {
		return []SkillExport{{Name: "humanize", Enabled: true, Files: files}}
	}
	readSkill := func(rel string) []byte {
		t.Helper()
		data, err := afero.ReadFile(fs, filepath.Join(dir, "humanize", rel))
		require.NoError(t, err)
		return data
	}
	tracked := func() []string {
		t.Helper()
		got, err := ledger.Ledger{FS: fs, Dir: dir}.Read(ledger.SurfaceSkills)
		require.NoError(t, err)
		return got
	}

	// A package written with BOTH bodies as plain files — the shape a writer
	// fed the unselected package produces.
	require.NoError(t, WriteManagedSkillPackages(fs, dir, export(
		PackageFile{RelPath: "SKILL.md", Content: raw, Mode: 0644},
		PackageFile{RelPath: "SKILL.distilled.md", Content: distilled, Mode: 0644},
		PackageFile{RelPath: "scripts/run.sh", Content: script, Mode: 0755},
	)))
	require.Equal(t, raw, readSkill("SKILL.md"))
	require.Equal(t, distilled, readSkill("SKILL.distilled.md"))

	// The distilled body selected: its bytes substituted at SKILL.md, and no
	// file for either body under its own name.
	require.NoError(t, WriteManagedSkillPackages(fs, dir, export(
		PackageFile{RelPath: "SKILL.md", Content: distilled, Mode: 0644},
		PackageFile{RelPath: "scripts/run.sh", Content: script, Mode: 0755},
	)))
	assert.Equal(t, distilled, readSkill("SKILL.md"), "the selected body's bytes replace the descriptor")
	gone, err := afero.Exists(fs, filepath.Join(dir, "humanize", "SKILL.distilled.md"))
	require.NoError(t, err)
	assert.False(t, gone, "the unselected body's earlier file is removed")
	assert.Equal(t, script, readSkill("scripts/run.sh"))
	info, err := fs.Stat(filepath.Join(dir, "humanize", "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())
	assert.ElementsMatch(t, []string{"humanize/SKILL.md", "humanize/scripts/run.sh"}, tracked(),
		"the ledger names exactly the materialized set")

	// And back to the raw body: the same path, different bytes.
	require.NoError(t, WriteManagedSkillPackages(fs, dir, export(
		PackageFile{RelPath: "SKILL.md", Content: raw, Mode: 0644},
		PackageFile{RelPath: "scripts/run.sh", Content: script, Mode: 0755},
	)))
	assert.Equal(t, raw, readSkill("SKILL.md"))
	assert.ElementsMatch(t, []string{"humanize/SKILL.md", "humanize/scripts/run.sh"}, tracked())
}
