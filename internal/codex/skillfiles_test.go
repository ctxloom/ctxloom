package codex

import (
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestWriteSkillFiles_EnabledSkillLandsInGlobalCodexHomeWithModes proves an
// enabled skill materializes at the GLOBAL $CODEX_HOME/skills/<name>/SKILL.md
// (codex skills are global, like its prompts), with the exec bit on
// scripts/ entries preserved, and is manifest-tracked.
func TestWriteSkillFiles_EnabledSkillLandsInGlobalCodexHomeWithModes(t *testing.T) {
	t.Setenv("CODEX_HOME", "/fake/codexhome")
	fs := afero.NewMemMapFs()
	skills := []agent.SkillExport{{
		Name:    "humanize",
		Enabled: true,
		Files: []agent.PackageFile{
			{RelPath: "SKILL.md", Content: []byte("---\nname: humanize\ndescription: d\n---\n\nBody\n"), Mode: 0644},
			{RelPath: "scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0755},
			{RelPath: "assets/data.txt", Content: []byte("data"), Mode: 0644},
		},
	}}

	require.NoError(t, WriteSkillFiles("/some/project", skills, agent.WithCommandFS(fs)))

	base := "/fake/codexhome/skills/humanize"
	content, err := afero.ReadFile(fs, base+"/SKILL.md")
	require.NoError(t, err)
	assert.Contains(t, string(content), "Body")

	info, err := fs.Stat(base + "/scripts/run.sh")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())

	info, err = fs.Stat(base + "/assets/data.txt")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), info.Mode().Perm())

	// Not written to the (unread) project dir.
	projExists, _ := afero.Exists(fs, "/some/project/.codex/skills/humanize/SKILL.md")
	assert.False(t, projExists, "skill is NOT written to the project dir; codex skills are global")

	manifest, err := afero.ReadFile(fs, "/fake/codexhome/skills/.ctxloom-skills-manifest")
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "humanize/SKILL.md")
	assert.Contains(t, string(manifest), "humanize/scripts/run.sh")
}

// TestWriteSkillFiles_DisabledSkillNotWritten proves a disabled skill is
// never written to disk and never manifest-tracked.
func TestWriteSkillFiles_DisabledSkillNotWritten(t *testing.T) {
	t.Setenv("CODEX_HOME", "/fake/codexhome")
	fs := afero.NewMemMapFs()
	skills := []agent.SkillExport{{
		Name:    "off",
		Enabled: false,
		Files:   []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("should not appear")}},
	}}

	require.NoError(t, WriteSkillFiles("/p", skills, agent.WithCommandFS(fs)))

	exists, _ := afero.Exists(fs, "/fake/codexhome/skills/off/SKILL.md")
	assert.False(t, exists)
	exists, _ = afero.Exists(fs, "/fake/codexhome/skills/.ctxloom-skills-manifest")
	assert.False(t, exists, "nothing written means no manifest at all")
}

// TestWriteSkillFiles_CleanupPreservesForeignSkill proves re-materializing
// with fewer skills reverts only the manifest-tracked set — a foreign,
// user-authored skill directory in $CODEX_HOME/skills survives.
func TestWriteSkillFiles_CleanupPreservesForeignSkill(t *testing.T) {
	t.Setenv("CODEX_HOME", "/fake/codexhome")
	fs := afero.NewMemMapFs()
	foreign := "/fake/codexhome/skills/my-own-skill/SKILL.md"
	require.NoError(t, afero.WriteFile(fs, foreign, []byte("hand authored"), 0644))

	skills := []agent.SkillExport{{
		Name:    "humanize",
		Enabled: true,
		Files:   []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("managed"), Mode: 0644}},
	}}
	require.NoError(t, WriteSkillFiles("/p", skills, agent.WithCommandFS(fs)))
	exists, _ := afero.Exists(fs, "/fake/codexhome/skills/humanize/SKILL.md")
	require.True(t, exists)

	// Cleanup: re-materialize with no skills.
	require.NoError(t, WriteSkillFiles("/p", nil, agent.WithCommandFS(fs)))

	exists, _ = afero.Exists(fs, "/fake/codexhome/skills/humanize/SKILL.md")
	assert.False(t, exists)
	exists, _ = afero.Exists(fs, foreign)
	assert.True(t, exists, "a foreign skill file must survive cleanup")
	content, err := afero.ReadFile(fs, foreign)
	require.NoError(t, err)
	assert.Equal(t, "hand authored", string(content))
}
