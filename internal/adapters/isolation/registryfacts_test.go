package isolation_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// RegistryFacts reads the kinds' OWN declarations, live: a kind's Home and
// Container reach the cells adapter by name, and a name nobody composed is
// a fact nobody has.
func TestRegistryFacts_ReadsTheKindsDeclarationsByName(t *testing.T) {
	kind := enginefixture.Kind("fixture-facts", mock.WithContainer())
	facts := isolation.RegistryFacts{Registry: enginefixture.RegistryOf(kind)}

	got, ok := facts.For("fixture-facts")
	require.True(t, ok)
	c, ok := got.Container.Get()
	require.True(t, ok)
	want, err := kind.Container()
	require.NoError(t, err)
	assert.Equal(t, want.OverlayDirs, c.OverlayDirs)
	assert.Equal(t, kind.Home(), got.Home)
	assert.Equal(t, []string{"fixture-facts"}, facts.Names())

	_, ok = facts.For("never-composed")
	assert.False(t, ok)
}
