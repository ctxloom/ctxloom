package kit

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

func skill(name, description string) agent.SkillExport {
	return agent.SkillExport{Name: name, Description: description, Enabled: true}
}

// Each constraint is refused naming itself; the limits are exact (a value AT
// a limit passes, one over is refused).
func TestCheckAgentSkill_Constraints(t *testing.T) {
	cases := []struct {
		label, want string
		s           agent.SkillExport
	}{
		{"missing name", "`name`", skill("", "d")},
		{"name over the limit", strconv.Itoa(SkillNameMaxLen), skill(strings.Repeat("a", SkillNameMaxLen+1), "d")},
		{"name not lowercase-hyphen", "lowercase", skill("Bad_Name", "d")},
		{"missing description", "`description`", skill("nodesc", "")},
		{"description over the limit", strconv.Itoa(SkillDescriptionMaxLen), skill("longdesc", strings.Repeat("d", SkillDescriptionMaxLen+1))},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			err := CheckAgentSkill(tc.s)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	assert.NoError(t, CheckAgentSkill(skill(strings.Repeat("a", SkillNameMaxLen), strings.Repeat("d", SkillDescriptionMaxLen))),
		"a name and description exactly at their limits pass")
}

// A reserved word is refused anywhere in the name, and only the words the
// engine names: with none, the same name passes.
func TestCheckAgentSkill_ReservedWordsAreTheEnginesAndASubstringMatch(t *testing.T) {
	for _, name := range []string{"my-acme-tool", "helper-for-acme", "xacmex"} {
		err := CheckAgentSkill(skill(name, "d"), "acme")
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "reserved")
		assert.NoError(t, CheckAgentSkill(skill(name, "d")), "no reserved words: %s passes", name)
	}
	assert.NoError(t, CheckAgentSkill(skill("acne-parser", "d"), "acme"), "a near-miss passes")
}

// The filter drops a failing enabled skill with a warning naming the skill,
// the engine and the constraint, keeps its valid sibling, and passes a
// disabled one unexamined and silently.
func TestAgentSkillRules_RefusesLoudlyNamingTheEngine(t *testing.T) {
	var buf bytes.Buffer
	t.Cleanup(clidiag.SetSink(&buf))
	off := skill("Bad_Name", "")
	off.Enabled = false

	got := AgentSkillRules("eng-x", "acme")([]agent.SkillExport{skill("acme-helper", "d"), skill("fine", "d"), off})

	require.Len(t, got, 2)
	assert.Equal(t, "fine", got[0].Name)
	assert.Equal(t, off, got[1], "a disabled skill passes unexamined")
	assert.Contains(t, buf.String(), `refusing skill "acme-helper" for eng-x`)
	assert.Contains(t, buf.String(), "reserved")
	assert.NotContains(t, buf.String(), "Bad_Name", "no warning for a disabled skill")
}
