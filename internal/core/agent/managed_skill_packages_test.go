package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// A skill package may carry one body per form, and the process stage selects
// one before the package reaches this writer: the export it hands over has the
// selected body's bytes AT SKILL.md and no file for the other body. The writer
// is content-agnostic, so this pins the two things that selection relies on
// it for: a re-materialization that substitutes SKILL.md's bytes replaces the
// file, and a file the export no longer names (the other body, which an
// earlier export may have shipped as a plain sibling) is not delivered — so
// the static writer's release of the earlier delivery removes it.
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
	write := func(skills []SkillExport) []string {
		t.Helper()
		delivered, err := WriteManagedSkillPackages(safefs.NewMem(fs), dir, skills)
		require.NoError(t, err)
		return delivered
	}
	materialized := []string{filepath.Join(dir, "humanize", "SKILL.md"), filepath.Join(dir, "humanize", "scripts", "run.sh")}

	// A package written with BOTH bodies as plain files — the shape a writer
	// fed the unselected package produces.
	write(export(
		PackageFile{RelPath: "SKILL.md", Content: raw, Mode: 0644},
		PackageFile{RelPath: "SKILL.distilled.md", Content: distilled, Mode: 0644},
		PackageFile{RelPath: "scripts/run.sh", Content: script, Mode: 0755},
	))
	require.Equal(t, raw, readSkill("SKILL.md"))
	require.Equal(t, distilled, readSkill("SKILL.distilled.md"))

	// The distilled body selected: its bytes substituted at SKILL.md, and no
	// file for either body under its own name.
	delivered := write(export(
		PackageFile{RelPath: "SKILL.md", Content: distilled, Mode: 0644},
		PackageFile{RelPath: "scripts/run.sh", Content: script, Mode: 0755},
	))
	assert.Equal(t, distilled, readSkill("SKILL.md"), "the selected body's bytes replace the descriptor")
	assert.Equal(t, script, readSkill("scripts/run.sh"))
	info, err := fs.Stat(filepath.Join(dir, "humanize", "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())
	assert.ElementsMatch(t, materialized, delivered,
		"the delivered set is exactly the materialized one; the unselected body is not in it")

	// And back to the raw body: the same path, different bytes.
	delivered = write(export(
		PackageFile{RelPath: "SKILL.md", Content: raw, Mode: 0644},
		PackageFile{RelPath: "scripts/run.sh", Content: script, Mode: 0755},
	))
	assert.Equal(t, raw, readSkill("SKILL.md"))
	assert.ElementsMatch(t, materialized, delivered)
}
