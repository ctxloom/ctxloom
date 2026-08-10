package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

// TestSkillExports_DescriptionReachesTheEngineInSKILLmd is the coverage that
// makes agent.SkillExport.Description safe to delete.
//
// That field is write-only: buildSkillExports sets it from the skill's
// frontmatter and no engine reads it — claude/opencode's skill
// writers take Enabled, Name and Files only. Deleting a field that carries
// user-authored text is only safe if the text still reaches the engine by
// another route, and it does: the description lives in the authored SKILL.md's
// own frontmatter, which travels verbatim inside Files.
//
// This pins that route, so the deletion is provably a redundancy removal and
// not a silent loss of the one thing an engine uses to decide when to invoke a
// skill. It is written against Files, so it keeps holding after the field goes.
func TestSkillExports_DescriptionReachesTheEngineInSKILLmd(t *testing.T) {
	const description = "Removes AI writing tells."
	skill := &bundles.LoadedSkill{
		Name:        "skill-bundle/humanize",
		Bundle:      "skill-bundle",
		Item:        "humanize",
		Frontmatter: bundles.SkillFrontmatter{Name: "humanize", Description: description},
		Files: []bundles.LoadedSkillFile{
			{RelPath: "SKILL.md", Mode: 0644, Content: []byte(
				"---\nname: humanize\ndescription: " + description + "\n---\n\nbody\n")},
		},
		LLM: bundles.SkillLLMExports{ClaudeCode: bundles.SkillEngineExport{Enabled: boolPtr(true)}},
	}

	ex := claudeSkillExports([]*bundles.LoadedSkill{skill})
	require.Len(t, ex, 1)
	require.True(t, ex[0].Enabled)

	var skillMD string
	for _, f := range ex[0].Files {
		if f.RelPath == "SKILL.md" {
			skillMD = string(f.Content)
		}
	}
	require.NotEmpty(t, skillMD, "the export must carry the authored SKILL.md")
	assert.Contains(t, skillMD, description,
		"the description an engine actually reads travels inside SKILL.md's own frontmatter, "+
			"not in a separate export field")
}

func boolPtr(b bool) *bool { return &b }
