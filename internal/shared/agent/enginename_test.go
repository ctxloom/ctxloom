package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The alias table is POPULATED by the engines a binary composes, never born
// full: shared code defines the vocabulary's shape, an engine declares its
// own spellings, and a binary that composes no engine resolves no alias.
// That is what keeps ltk, taskloom and ctxloom from diverging — each
// composes the same engine facts through its own root.
func TestCanonicalEngineName_ResolvesOnlyRegisteredAliases(t *testing.T) {
	t.Cleanup(resetEngineAliasesForTesting)
	resetEngineAliasesForTesting()

	assert.Equal(t, "fixture-alias", CanonicalEngineName("Fixture-Alias"), "nothing registered: lowercased, unresolved")
	assert.Nil(t, EngineNameAliases("fixture-engine"))

	require.NoError(t, RegisterEngineAliases("fixture-engine", []string{"fixture-alias", "fx"}))
	assert.Equal(t, "fixture-engine", CanonicalEngineName("Fixture-Alias"))
	assert.Equal(t, "fixture-engine", CanonicalEngineName("FX"))
	assert.Equal(t, "fixture-engine", CanonicalEngineName("fixture-engine"), "the canonical name resolves to itself")
	assert.Equal(t, []string{"fixture-alias", "fx"}, EngineNameAliases("fixture-engine"), "sorted, canonical excluded")
	assert.Equal(t, "fixture-engin", CanonicalEngineName("fixture-engin"), "no prefix or fuzzy matching")
}

// Re-registering the same mapping is idempotent (two composition roots may
// name one engine in one process); binding an alias to a DIFFERENT engine is
// refused, since one spelling resolving two ways is the divergence the
// table exists to prevent.
func TestRegisterEngineAliases_IdempotentAndRefusesConflicts(t *testing.T) {
	t.Cleanup(resetEngineAliasesForTesting)
	resetEngineAliasesForTesting()

	require.NoError(t, RegisterEngineAliases("engine-a", []string{"a"}))
	assert.NoError(t, RegisterEngineAliases("engine-a", []string{"a"}), "same mapping again is not a conflict")
	err := RegisterEngineAliases("engine-b", []string{"a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"a"`)
	assert.Equal(t, "engine-a", CanonicalEngineName("a"), "a refused registration changes nothing")
}

// The shape rules travel with the registration: a spelling the table would
// rewrite can never be installed where no lookup reaches it.
func TestRegisterEngineAliases_RefusesMalformedSpellings(t *testing.T) {
	t.Cleanup(resetEngineAliasesForTesting)
	resetEngineAliasesForTesting()

	assert.Error(t, RegisterEngineAliases("", []string{"x"}), "empty canonical")
	assert.Error(t, RegisterEngineAliases("Engine", []string{"x"}), "uppercase canonical")
	assert.Error(t, RegisterEngineAliases("engine", []string{"Engine-X"}), "uppercase alias")
	assert.Error(t, RegisterEngineAliases("engine", []string{"engine"}), "alias equals the name")
	assert.Error(t, RegisterEngineAliases("engine", []string{"x", "x"}), "alias declared twice")
	assert.Equal(t, "x", CanonicalEngineName("x"), "nothing from a refused registration lands")
}
