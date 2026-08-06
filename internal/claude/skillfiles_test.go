package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
)

// TestWriteSkillFiles_EnabledSkillLandsAtPathWithModes proves an enabled
// skill materializes at .claude/skills/<name>/SKILL.md plus its sibling
// files, with each file's mode (the exec bit on scripts/ in particular)
// preserved.
func TestWriteSkillFiles_EnabledSkillLandsAtPathWithModes(t *testing.T) {
	dir := t.TempDir()
	skills := []agent.SkillExport{{
		Name:    "humanize",
		Enabled: true,
		Files: []agent.PackageFile{
			{RelPath: "SKILL.md", Content: []byte("---\nname: humanize\ndescription: d\n---\n\nBody\n"), Mode: 0644},
			{RelPath: "scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0755},
			{RelPath: "assets/data.txt", Content: []byte("data"), Mode: 0644},
		},
	}}

	require.NoError(t, WriteSkillFiles(dir, skills))

	base := filepath.Join(dir, ".claude", "skills", "humanize")
	content, err := os.ReadFile(filepath.Join(base, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Body")

	info, err := os.Stat(filepath.Join(base, "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())

	info, err = os.Stat(filepath.Join(base, "assets", "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), info.Mode().Perm())

	manifest, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", ledger.Name))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "humanize/SKILL.md")
	assert.Contains(t, string(manifest), "humanize/scripts/run.sh")
}

// TestWriteSkillFiles_DisabledSkillNotWritten proves a disabled skill is
// never written to disk and never manifest-tracked.
func TestWriteSkillFiles_DisabledSkillNotWritten(t *testing.T) {
	dir := t.TempDir()
	skills := []agent.SkillExport{{
		Name:    "off",
		Enabled: false,
		Files:   []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("should not appear")}},
	}}

	require.NoError(t, WriteSkillFiles(dir, skills))

	assert.NoFileExists(t, filepath.Join(dir, ".claude", "skills", "off", "SKILL.md"))
	assert.NoFileExists(t, filepath.Join(dir, ".claude", "skills", ledger.Name),
		"nothing written means no manifest at all")
}

// TestWriteSkillFiles_CleanupPreservesForeignSkill proves re-materializing
// with fewer skills reverts only the manifest-tracked set — a foreign,
// user-authored skill directory in .claude/skills/ survives.
func TestWriteSkillFiles_CleanupPreservesForeignSkill(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, ".claude", "skills", "my-own-skill", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(foreign), 0755))
	require.NoError(t, os.WriteFile(foreign, []byte("hand authored"), 0644))

	skills := []agent.SkillExport{{
		Name:    "humanize",
		Enabled: true,
		Files:   []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("managed"), Mode: 0644}},
	}}
	require.NoError(t, WriteSkillFiles(dir, skills))
	require.FileExists(t, filepath.Join(dir, ".claude", "skills", "humanize", "SKILL.md"))

	// Cleanup: re-materialize with no skills.
	require.NoError(t, WriteSkillFiles(dir, nil))

	assert.NoFileExists(t, filepath.Join(dir, ".claude", "skills", "humanize", "SKILL.md"))
	assert.FileExists(t, foreign, "a foreign skill directory must survive cleanup")
	content, err := os.ReadFile(foreign)
	require.NoError(t, err)
	assert.Equal(t, "hand authored", string(content))
}
