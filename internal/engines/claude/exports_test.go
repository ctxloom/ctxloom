package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

func claudeEngine(t *testing.T) engine.Engine {
	t.Helper()
	e, err := Build()
	require.NoError(t, err)
	return e
}

// Exports decodes each command's claude-code block: the block's enablement
// and metadata reach the export; a curated command exports even where its
// block opts out; another engine's block says nothing about this one; the
// help text is the block's description, else the authored one.
func TestExports_DecodesTheClaudeCodeBlock(t *testing.T) {
	block := []byte(`{"enabled":false,"description":"d","argument_hint":"h","allowed_tools":["Read"],"model":"m"}`)
	ex, err := claudeEngine(t).Exports(engine.Items{Commands: []engine.CommandItem{
		{Ref: "a#prompts/p", Name: "a/p", Body: []byte("body"), Exports: block},
		{Ref: "a#prompts/curated", Name: "a/curated", Body: []byte("body"), Exports: block, Curated: true},
		{Ref: "a#prompts/other", Name: "a/other", Body: []byte("body"), Description: "authored"},
	}})
	require.NoError(t, err)
	require.Len(t, ex.Commands, 3)
	assert.Equal(t, engine.CommandExport{Name: "a/p", Body: []byte("body"), Enabled: false, Description: "d", ArgumentHint: "h", AllowedTools: []string{"Read"}, Model: "m"}, ex.Commands[0])
	assert.True(t, ex.Commands[1].Enabled, "a curated command exports even where its block opts out")
	assert.True(t, ex.Commands[2].Enabled, "no block for this engine ⇒ enabled (opt-out model)")
	assert.Equal(t, "authored", ex.Commands[2].Description, "the authored description is the help text when the block gives none")
}

// A block the schema refuses is an error naming the engine and the item:
// nothing is exported on a guess.
func TestExports_RefusesAMalformedBlock(t *testing.T) {
	_, err := DecodeExportBlock([]byte(`{"enabled":"yes"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), EngineName)
	_, err = DecodeExportBlock([]byte(`{"unknown_key":1}`))
	require.Error(t, err, "the schema closes the block: an unknown key is an authoring error")

	_, err = claudeEngine(t).Exports(engine.Items{Commands: []engine.CommandItem{{Ref: "a#prompts/p", Name: "a/p", Exports: []byte(`{"enabled":"yes"}`)}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a#prompts/p")
	assert.Contains(t, err.Error(), EngineName)
}

// A skill is offered unless its block opts out; a curated one regardless.
func TestExports_ReadsTheSkillBlock(t *testing.T) {
	ex, err := claudeEngine(t).Exports(engine.Items{Skills: []engine.SkillItem{
		{Ref: "a#skills/on", Name: "on", Description: "does on"},
		{Ref: "a#skills/off", Name: "off", Exports: []byte(`{"enabled":false}`)},
		{Ref: "a#skills/curated", Name: "curated", Exports: []byte(`{"enabled":false}`), Curated: true},
	}})
	require.NoError(t, err)
	require.Len(t, ex.Skills, 3)
	assert.True(t, ex.Skills[0].Enabled)
	assert.Equal(t, "does on", ex.Skills[0].Description, "the frontmatter travels verbatim")
	assert.False(t, ex.Skills[1].Enabled)
	assert.True(t, ex.Skills[2].Enabled)
}

// The description an engine reads travels inside the authored SKILL.md's
// own frontmatter, which crosses verbatim in Files: a writer that takes
// Name and Files alone still delivers it. Pinned against Files so it holds
// whatever a separate export field does.
func TestExports_DescriptionReachesTheEngineInSKILLmd(t *testing.T) {
	const description = "Removes AI writing tells."
	skillMD := []byte("---\nname: humanize\ndescription: " + description + "\n---\n\nbody\n")
	ex, err := claudeEngine(t).Exports(engine.Items{Skills: []engine.SkillItem{{
		Ref: "skill-bundle#skills/humanize", Name: "humanize", Description: description,
		Files:   []engine.SkillFile{{Path: "SKILL.md", Mode: 0o644, Bytes: skillMD}},
		Exports: []byte(`{"enabled":true}`),
	}}})
	require.NoError(t, err)
	require.Len(t, ex.Skills, 1)
	require.True(t, ex.Skills[0].Enabled)

	var carried string
	for _, f := range ex.Skills[0].Files {
		if f.Path == "SKILL.md" {
			carried = string(f.Bytes)
		}
	}
	require.NotEmpty(t, carried, "the export must carry the authored SKILL.md")
	assert.Contains(t, carried, description, "the description an engine actually reads travels inside SKILL.md's own frontmatter")
}
