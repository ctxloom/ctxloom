package antigravity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestWriteSkillFiles_EnabledSkillLandsAtPathWithModes proves an enabled
// skill materializes at .agents/skills/<name>/SKILL.md plus its sibling
// files, with each file's mode (the exec bit on scripts/ in particular)
// preserved.
func TestWriteSkillFiles_EnabledSkillLandsAtPathWithModes(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	skills := []agent.SkillExport{{
		Name:    "humanize",
		Enabled: true,
		Files: []agent.PackageFile{
			{RelPath: "SKILL.md", Content: []byte("---\nname: humanize\ndescription: d\n---\n\nBody\n"), Mode: 0644},
			{RelPath: "scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0755},
			{RelPath: "assets/data.txt", Content: []byte("data"), Mode: 0644},
		},
	}}

	require.NoError(t, WriteSkillFiles(dir, skills, agent.WithCommandFS(fs)))

	base := filepath.Join(dir, AgentsDir, "skills", "humanize")
	content, err := afero.ReadFile(fs, filepath.Join(base, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Body")

	info, err := fs.Stat(filepath.Join(base, "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())

	info, err = fs.Stat(filepath.Join(base, "assets", "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), info.Mode().Perm())

	manifest, err := afero.ReadFile(fs, filepath.Join(dir, AgentsDir, "skills", ".ctxloom-skills-manifest"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "humanize/SKILL.md")
	assert.Contains(t, string(manifest), "humanize/scripts/run.sh")
}

// TestWriteSkillFiles_DisabledSkillNotWritten proves a disabled skill is
// never written to disk and never manifest-tracked.
func TestWriteSkillFiles_DisabledSkillNotWritten(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	skills := []agent.SkillExport{{
		Name:    "off",
		Enabled: false,
		Files:   []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("should not appear")}},
	}}

	require.NoError(t, WriteSkillFiles(dir, skills, agent.WithCommandFS(fs)))

	exists, _ := afero.Exists(fs, filepath.Join(dir, AgentsDir, "skills", "off", "SKILL.md"))
	assert.False(t, exists)
	exists, _ = afero.Exists(fs, filepath.Join(dir, AgentsDir, "skills", ".ctxloom-skills-manifest"))
	assert.False(t, exists, "nothing written means no manifest at all")
}

// TestWriteSkillFiles_CleanupPreservesForeignSkill proves re-materializing
// with fewer skills reverts only the manifest-tracked set — a foreign,
// user-authored skill directory in .agents/skills/ survives.
func TestWriteSkillFiles_CleanupPreservesForeignSkill(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	foreign := filepath.Join(dir, AgentsDir, "skills", "my-own-skill", "SKILL.md")
	require.NoError(t, afero.WriteFile(fs, foreign, []byte("hand authored"), 0644))

	skills := []agent.SkillExport{{
		Name:    "humanize",
		Enabled: true,
		Files:   []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("managed"), Mode: 0644}},
	}}
	require.NoError(t, WriteSkillFiles(dir, skills, agent.WithCommandFS(fs)))
	exists, _ := afero.Exists(fs, filepath.Join(dir, AgentsDir, "skills", "humanize", "SKILL.md"))
	require.True(t, exists)

	// Cleanup: re-materialize with no skills.
	require.NoError(t, WriteSkillFiles(dir, nil, agent.WithCommandFS(fs)))

	exists, _ = afero.Exists(fs, filepath.Join(dir, AgentsDir, "skills", "humanize", "SKILL.md"))
	assert.False(t, exists)
	exists, _ = afero.Exists(fs, foreign)
	assert.True(t, exists, "a foreign skill directory must survive cleanup")
	content, err := afero.ReadFile(fs, foreign)
	require.NoError(t, err)
	assert.Equal(t, "hand authored", string(content))
}

// TestWriteSkillFiles_CommandAndSkillCoexistSameName proves a command file
// (flat `<name>.md`) and a skill directory (`<name>/SKILL.md`) with the SAME
// name coexist under the shared .agents/skills/ parent without collision —
// they are distinct filesystem entries (a file vs. a directory).
func TestWriteSkillFiles_CommandAndSkillCoexistSameName(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"

	require.NoError(t, WriteCommandFiles(dir, []agent.CommandExport{
		{Name: "humanize", Content: "command body", Enabled: true},
	}, agent.WithCommandFS(fs)))
	require.NoError(t, WriteSkillFiles(dir, []agent.SkillExport{
		{Name: "humanize", Enabled: true, Files: []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("skill body"), Mode: 0644}}},
	}, agent.WithCommandFS(fs)))

	cmdPath := filepath.Join(dir, AgentsDir, "skills", "humanize.md")
	skillPath := filepath.Join(dir, AgentsDir, "skills", "humanize", "SKILL.md")
	cmdData, err := afero.ReadFile(fs, cmdPath)
	require.NoError(t, err, "the command file survives alongside the skill directory")
	assert.Equal(t, "command body", string(cmdData))
	skillData, err := afero.ReadFile(fs, skillPath)
	require.NoError(t, err, "the skill directory survives alongside the command file")
	assert.Contains(t, string(skillData), "skill body")
}
