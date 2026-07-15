package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

// loadedSkill builds a bundles.LoadedSkill fixture with one SKILL.md file and
// a claude-code enablement flag, the skills analog of remotePrompt above.
func loadedSkill(name string, claudeEnabled bool) *bundles.LoadedSkill {
	on, off := true, false
	enabled := &off
	if claudeEnabled {
		enabled = &on
	}
	return &bundles.LoadedSkill{
		Name:        "skill-bundle/" + name,
		Bundle:      "skill-bundle",
		Item:        name,
		Frontmatter: bundles.SkillFrontmatter{Name: name, Description: "does a thing"},
		Files: []bundles.LoadedSkillFile{
			{RelPath: "SKILL.md", Content: []byte("SKILL.md body"), Mode: 0644},
			{RelPath: "scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0755},
		},
		LLM: bundles.SkillLLMExports{ClaudeCode: bundles.SkillEngineExport{Enabled: enabled}},
	}
}

// TestClaudeSkillExports_ResolvesEnablementAndFiles proves claudeSkillExports
// maps a resolved skill's frontmatter name/description and every file (with
// its mode) straight through into agent.SkillExport, and resolves the
// claude-code enablement from SkillLLMExports.
func TestClaudeSkillExports_ResolvesEnablementAndFiles(t *testing.T) {
	skills := []*bundles.LoadedSkill{loadedSkill("humanize", true)}

	ex := claudeSkillExports(skills)
	require.Len(t, ex, 1)
	assert.Equal(t, "humanize", ex[0].Name)
	assert.Equal(t, "does a thing", ex[0].Description)
	assert.True(t, ex[0].Enabled)

	byPath := map[string]struct {
		content string
		mode    uint32
	}{}
	for _, f := range ex[0].Files {
		byPath[f.RelPath] = struct {
			content string
			mode    uint32
		}{string(f.Content), uint32(f.Mode)}
	}
	require.Contains(t, byPath, "scripts/run.sh")
	assert.Equal(t, uint32(0755), byPath["scripts/run.sh"].mode, "the exec bit survives the export mapping")
}

// TestClaudeSkillExports_DisabledSkillReportsDisabled proves a skill the
// bundle disabled for claude-code resolves with Enabled == false — the
// downstream writer (WriteSkillFiles) is what skips writing it, but the
// export mapper is what carries the decision.
func TestClaudeSkillExports_DisabledSkillReportsDisabled(t *testing.T) {
	skills := []*bundles.LoadedSkill{loadedSkill("humanize", false)}

	ex := claudeSkillExports(skills)
	require.Len(t, ex, 1)
	assert.False(t, ex[0].Enabled)
}

// TestSkillExportsFor_UnregisteredBackendReturnsNil mirrors
// commandExportsFor's opt-out contract: a backend with no skillExports mapper
// (every backend but claude today) exports no skills rather than erroring.
func TestSkillExportsFor_UnregisteredBackendReturnsNil(t *testing.T) {
	skills := []*bundles.LoadedSkill{loadedSkill("humanize", true)}
	assert.Nil(t, SkillExportsFor("kiro", skills), "kiro has no skillExports mapper yet (next parallel wave)")
	assert.Nil(t, SkillExportsFor("does-not-exist", skills))
}
