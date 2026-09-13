package engines

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/lm/enginenames"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestRegister_EveryShippedEngineComposes is what turns a forgotten slot on a
// NEW engine into a red test in seconds: every descriptor in the composition
// root must validate and install, or the process would refuse to start.
func TestRegister_EveryShippedEngineComposes(t *testing.T) {
	require.NoError(t, Register())
	names := backends.List()
	assert.NotEmpty(t, names)
	shippable := 0
	for _, n := range names {
		if !backends.IsTestOnly(n) {
			shippable++
		}
	}
	assert.GreaterOrEqual(t, shippable, 1, "at least one non-test engine must be composed")
}

// A second call is the same registration, not a duplicate error: TestMains
// and the CLI may both compose in one process.
func TestRegister_IsIdempotent(t *testing.T) {
	require.NoError(t, Register())
	assert.NoError(t, Register())
	assert.NotPanics(t, MustRegister)
}

// TestRegister_LeanNameRootAgreesWithTheDescriptors holds the two composition
// roots together: every shipped (non-test-double) descriptor is named in
// internal/lm/enginenames with exactly its own aliases, and the lean root
// names nothing the descriptor root does not ship. An engine added to one
// root and not the other resolves its spellings under one binary and errors
// under the other — the divergence the lean root exists to prevent.
func TestRegister_LeanNameRootAgreesWithTheDescriptors(t *testing.T) {
	require.NoError(t, Register())

	lean := map[string][]string{}
	for _, e := range enginenames.Declared() {
		lean[e.Name] = e.Aliases
	}
	shipped := 0
	for _, name := range backends.List() {
		if backends.IsTestOnly(name) {
			_, named := lean[name]
			assert.False(t, named, "%s is a test double; the lean root must not name it", name)
			continue
		}
		shipped++
		aliases, named := lean[name]
		if !assert.True(t, named, "%s ships but internal/lm/enginenames does not name it — ltk and taskloom would not resolve its spellings", name) {
			continue
		}
		assert.ElementsMatch(t, agent.EngineNameAliases(name), aliases,
			"%s: the lean root's aliases must be the descriptor's own", name)
		delete(lean, name)
	}
	assert.Empty(t, lean, "the lean root names engines the descriptor root does not ship")
	assert.GreaterOrEqual(t, shipped, 1)
}
