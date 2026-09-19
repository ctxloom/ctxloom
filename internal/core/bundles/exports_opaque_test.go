package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Per-engine exports are OPAQUE blocks keyed by engine name: the bundle
// package carries each block as bytes and never reads inside one; the engine
// named by the key decodes its own block against its ExportSchema. Two
// on-disk spellings read for one slice — the retired typed `llm:` key and
// the `exports:` key — and both decode to the same blocks.
const (
	legacyShape = `version: 1.0.0
commands:
  review:
    content: Review it.
    llm:
      claude-code:
        enabled: false
        description: Review (claude)
        allowed_tools: [Read]
skills:
  reviewer:
    llm:
      claude-code:
        enabled: false
`
	opaqueShape = `version: 1.0.0
commands:
  review:
    content: Review it.
    exports:
      claude-code:
        enabled: false
        description: Review (claude)
        allowed_tools: [Read]
      other-engine:
        anything: [1, 2]
skills:
  reviewer:
    exports:
      claude-code:
        enabled: false
      other-engine:
        enabled: true
`
)

func TestParseBundle_ExportsReadBothShapesToTheSameBlocks(t *testing.T) {
	legacy, err := ParseBundle([]byte(legacyShape))
	require.NoError(t, err)
	opaque, err := ParseBundle([]byte(opaqueShape))
	require.NoError(t, err)

	want := `{"allowed_tools":["Read"],"description":"Review (claude)","enabled":false}`
	assert.JSONEq(t, want, string(legacy.Commands["review"].Exports["claude-code"]))
	assert.JSONEq(t, want, string(opaque.Commands["review"].Exports["claude-code"]))
	assert.JSONEq(t, `{"enabled":false}`, string(legacy.Skills["reviewer"].Exports["claude-code"]))
	assert.JSONEq(t, `{"enabled":false}`, string(opaque.Skills["reviewer"].Exports["claude-code"]))

	// A block for an engine this binary does not know is carried, not
	// dropped: the registry names the engines, the bundle package does not.
	assert.JSONEq(t, `{"anything":[1,2]}`, string(opaque.Commands["review"].Exports["other-engine"]))
	assert.JSONEq(t, `{"enabled":true}`, string(opaque.Skills["reviewer"].Exports["other-engine"]))
}

// The writer emits the `exports:` key only, engines in name order, and what
// it writes reads back to the same blocks.
func TestBundle_MarshalWritesOnlyTheExportsKey(t *testing.T) {
	b, err := ParseBundle([]byte(legacyShape))
	require.NoError(t, err)
	out, err := yaml.Marshal(b)
	require.NoError(t, err)

	assert.Contains(t, string(out), "exports:")
	assert.NotContains(t, string(out), "llm:")

	again, err := ParseBundle(out)
	require.NoError(t, err)
	assert.Equal(t, b.Commands["review"].Exports, again.Commands["review"].Exports)
	assert.Equal(t, b.Skills["reviewer"].Exports, again.Skills["reviewer"].Exports)
}

// The trust preimage is UNCHANGED by the migration: the command contract's
// canonical exports payload is the same bytes for a raw block as it was for
// the typed struct — every field always emitted, in declaration order, the
// effective enablement (absent means enabled) and an empty tool grant as an
// empty list. An item with no block for the engine canonicalises to the
// defaults.
func TestCommandSurface_ExportsPayloadIsTheFrozenCanonicalForm(t *testing.T) {
	b, err := ParseBundle([]byte(opaqueShape))
	require.NoError(t, err)
	review := b.Commands["review"]
	got := review.Surface(false).ExportsPayload()
	assert.Equal(t, `{"claude-code":{"enabled":false,"description":"Review (claude)","argument_hint":"","allowed_tools":["Read"],"model":""}}`, string(got))

	bare := BundleCommand{ItemBody: ItemBody{Content: "x"}}
	assert.Equal(t, `{"claude-code":{"enabled":true,"description":"","argument_hint":"","allowed_tools":[],"model":""}}`, string(bare.Surface(false).ExportsPayload()))

	skillBytes, err := skillPayloadFor(b.Skills["reviewer"].LLM, SkillManifest{})
	require.NoError(t, err)
	assert.Contains(t, string(skillBytes), `"exports":{"claude-code":{"enabled":false}}`)
}

// A raw block that is not a JSON object is refused at parse: a scalar under
// an engine name is an authoring error, not a block an engine could decode.
func TestParseBundle_ExportsBlockMustBeAMapping(t *testing.T) {
	_, err := ParseBundle([]byte("version: 1.0.0\ncommands:\n  c:\n    content: x\n    exports:\n      claude-code: yes\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude-code")
}
