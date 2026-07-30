package tagschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An enum declaration carrying no usable member is a config defect, and it
// must fail the same way a malformed range declaration does. Returning an
// empty-but-PRESENT member list instead makes every value on that target a
// violation at both consumers (lint.Lint and operations.validateTag), with
// nothing anywhere able to say the DECLARATION is what is broken.
func TestEnum_DeclarationWithNoUsableMemberIsAnError(t *testing.T) {
	for _, decl := range []string{
		`tagma.enum:"triage:type"=","`,
		`tagma.enum:"triage:type"=" , , "`,
		`tagma.enum:"triage:type"=""`,
	} {
		t.Run(decl, func(t *testing.T) {
			s, err := Parse([]string{decl})
			require.NoError(t, err, "the declaration itself parses; the VALUE is what is malformed")

			members, ok, eerr := s.Enum("triage:type")
			assert.True(t, ok, "the declaration is present, so ok must stay true — as Range does")
			require.Error(t, eerr, "a declaration with no usable member must be reported, not returned empty")
			assert.Contains(t, eerr.Error(), "triage:type", "the error must name the offending target")
			assert.Empty(t, members)
		})
	}
}

// The healthy cases are unchanged: a well-formed list still comes back
// trimmed, with empty entries (a trailing comma) dropped rather than
// surfacing as a spurious "" member.
func TestEnum_WellFormedDeclarationsAreUnchanged(t *testing.T) {
	s, err := Parse([]string{`tagma.enum:"triage:type"="correctness, security ,tooling,"`})
	require.NoError(t, err)

	members, ok, eerr := s.Enum("triage:type")
	require.NoError(t, eerr)
	assert.True(t, ok)
	assert.Equal(t, []string{"correctness", "security", "tooling"}, members)

	members, ok, eerr = s.Enum("triage:undeclared")
	require.NoError(t, eerr)
	assert.False(t, ok)
	assert.Empty(t, members)
}
