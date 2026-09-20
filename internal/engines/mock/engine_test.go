package mock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The missing-skills arm has a subject: NoSkills exports no skill while
// mock's Exports do. If a change makes NoSkills export one, THIS TEST IS THE
// ONE THAT SHOULD STOP YOU: the absence is the double's entire purpose.
func TestExports_NoSkillsDoubleExportsNoSkill(t *testing.T) {
	items := engine.Items{
		Commands: []engine.CommandItem{{Ref: "b#prompts/c", Name: "b/c", Body: []byte("body")}},
		Skills:   []engine.SkillItem{{Ref: "b#skills/s", Name: "s"}},
	}
	ex, err := New().Exports(items)
	require.NoError(t, err)
	require.Len(t, ex.Commands, 1)
	assert.True(t, ex.Commands[0].Enabled, "mock exports every command")
	require.Len(t, ex.Skills, 1)
	assert.True(t, ex.Skills[0].Enabled, "mock exports every skill")

	var noSkills engine.Engine
	for _, e := range Doubles() {
		if e.Root().Name == NameNoSkills {
			noSkills = e
		}
	}
	require.NotNil(t, noSkills, "the registry composes the no-skills double")
	ex, err = noSkills.Exports(items)
	require.NoError(t, err)
	assert.Len(t, ex.Commands, 1)
	assert.Empty(t, ex.Skills, "NoSkills exports no skill — it is the only subject the missing-surface arm has")
}
