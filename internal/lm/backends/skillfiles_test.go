package backends

import (
	"bytes"
	claudeengine "github.com/ctxloom/ctxloom/internal/claude/engine"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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

	ex := claudeengine.SkillExports(skills)
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

	ex := claudeengine.SkillExports(skills)
	require.Len(t, ex, 1)
	assert.False(t, ex[0].Enabled)
}

// TestSkillExportsFor_UnregisteredBackendReturnsNil mirrors
// CommandExportsFor's opt-out contract: a name with no skillExports mapper
// exports no skills rather than erroring.
func TestSkillExportsFor_UnregisteredBackendReturnsNil(t *testing.T) {
	skills := []*bundles.LoadedSkill{loadedSkill("humanize", true)}
	assert.Nil(t, SkillExportsFor("does-not-exist", skills))
}

// An explicitly-selected (non-default) profile that fails to
// resolve must not be warned as a "default profile" — mirrors the
// commands.go/managed.go regression tests for the same wording bug.
func TestResolveProfileSkillRefs_ExplicitProfileWarningOmitsDefault(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	resolveProfileSkillRefs(cfg, []string{"explicitly-selected-and-missing"})

	assert.NotContains(t, buf.String(), "default profile",
		"an explicitly-selected profile must not be misreported as a default: got %q", buf.String())
}

// resolveProfileSkillRefs must diagnose a BROKEN inline profile
// (circular parent inheritance) instead of silently retrying it as a
// directory profile. Mirrors the commands.go/managed.go regression tests for
// the same defect.
func TestResolveProfileSkillRefs_CircularProfileIsWarnedNotMasked(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"loopy"}, map[string]string{
		"loopy": "parents:\n  - loopy\n",
	})

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	resolveProfileSkillRefs(cfg, nil)

	assert.Contains(t, buf.String(), "inheritance",
		"the real cause (inheritance) must reach the warning, not the directory loader's unrelated not-found error: got %q", buf.String())
}
