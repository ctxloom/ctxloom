package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// deliverProjectSkills delivers skills through claude's typed skills approach
// at the project root — the seam the static writer drives — so a test of the
// skill write is a test of what a delivery actually does.
func deliverProjectSkills(t *testing.T, dir string, skills []agent.SkillExport) error {
	t.Helper()
	in := engine.SkillsInputs{}
	for _, s := range skills {
		e := engine.SkillExport{Name: s.Name, Description: s.Description, Enabled: s.Enabled}
		for _, f := range s.Files {
			e.Files = append(e.Files, engine.SkillFile{Path: f.RelPath, Bytes: f.Content, Mode: uint32(f.Mode)})
		}
		in.Skills = append(in.Skills, e)
	}
	_, err := claudeDef(t).Skills.DeliverSkills(present.ProjectOnHost(dir), present.RootProjectRoot, in, safefs.New())
	return err
}

// TestDeliverSkills_EnabledSkillLandsAtPathWithModes proves an enabled
// skill materializes at .claude/skills/<name>/SKILL.md plus its sibling
// files, with each file's mode (the exec bit on scripts/ in particular)
// preserved.
func TestDeliverSkills_EnabledSkillLandsAtPathWithModes(t *testing.T) {
	dir := t.TempDir()
	skills := []agent.SkillExport{{
		Name:        "humanize",
		Description: "d",
		Enabled:     true,
		Files: []agent.PackageFile{
			{RelPath: "SKILL.md", Content: []byte("---\nname: humanize\ndescription: d\n---\n\nBody\n"), Mode: 0644},
			{RelPath: "scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0755},
			{RelPath: "assets/data.txt", Content: []byte("data"), Mode: 0644},
		},
	}}

	require.NoError(t, deliverProjectSkills(t, dir, skills))

	base := filepath.Join(dir, ".claude", "skills", "humanize")
	content, err := os.ReadFile(filepath.Join(base, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Body")

	info, err := os.Stat(filepath.Join(base, "scripts", "run.sh"))
	require.NoError(t, err)
	fileperm.Equal(t, 0o755, info.Mode())

	info, err = os.Stat(filepath.Join(base, "assets", "data.txt"))
	require.NoError(t, err)
	fileperm.Equal(t, 0o644, info.Mode())
}

// TestDeliverSkills_DisabledSkillNotWritten proves a disabled skill is
// never written to disk.
func TestDeliverSkills_DisabledSkillNotWritten(t *testing.T) {
	dir := t.TempDir()
	skills := []agent.SkillExport{{
		Name:        "off",
		Description: "d",
		Enabled:     false,
		Files:       []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("should not appear")}},
	}}

	require.NoError(t, deliverProjectSkills(t, dir, skills))

	assert.NoFileExists(t, filepath.Join(dir, ".claude", "skills", "off", "SKILL.md"))
}
