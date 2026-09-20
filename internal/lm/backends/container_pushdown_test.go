package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// containerFixture is a synthetic engine kind with a full container story:
// an installer, a validate gate, and an env-passthrough auth plan.
func containerFixture(name string, dist engine.Distribution) engine.Engine {
	return enginefixture.Kind(name, mock.WithDistribution(dist), mock.WithContainerSpec(engine.ContainerSpec{
		Install:         []byte("RUN true\n"),
		ValidateCommand: "true",
		Auth: engine.Provide(engine.ContainerAuth{
			EnvTriggers:    []string{"FIXTURE_KEY"},
			EnvPassthrough: []string{"FIXTURE_KEY"},
			Hint:           "no FIXTURE_KEY to authenticate the in-container fixture",
		}),
		OverlayDirs:        []string{".fixture"},
		TranscriptStoreRel: ".fixture/sessions",
	}))
}

// Register pushes every engine's container declaration to isolation, so the
// container rosters are the registry filtered by what each engine declared:
// CAPABILITY (an installer, an auth plan) from Container, POLICY from
// Distribution. Each is read from where it is declared; neither is a table.
func TestRegister_PushesTheContainerDeclarationToIsolation(t *testing.T) {
	cases := []struct {
		name                string
		dist                engine.Distribution
		composable, offered bool
	}{
		{"fixture-container-default", engine.DistributionDefault, true, true},
		{"fixture-container-optin", engine.DistributionOptIn, false, true},
		{"fixture-container-testonly", engine.DistributionTestOnly, false, false},
	}
	for _, c := range cases {
		t.Run(c.dist.String(), func(t *testing.T) {
			require.NoError(t, Register(enginefixture.RegistryOf(containerFixture(c.name, c.dist)), enginefixture.Hosting(c.name)))
			t.Cleanup(func() { UnregisterForTesting(c.name) })

			assert.True(t, isolation.HasContainerAuth(c.name), "a declared auth plan is a capability whatever the policy")
			assert.Equal(t, c.composable, contains(isolation.ComposableEngines(), c.name),
				"the default image set is installer AND DistributionDefault")
			assert.Equal(t, c.offered, contains(isolation.ContainerAuthEngines(), c.name),
				"the offered container-auth set excludes only test doubles")
			assert.Equal(t, []string{".fixture", ".ctxloom/cache"}, isolation.ContainerOverlayDirsFor(c.name),
				"the engine's own overlay dirs, plus ctxloom's cache")
			assert.Equal(t, ".fixture/sessions", isolation.ContainerTranscriptStoreRelFor(c.name))
		})
	}
}

// A declared absence — no container story — fails closed at the seam and is
// in no roster, and its reason is readable; an unregistered name is the same
// at the seam but is not a declaration.
func TestRegister_DeclaredAbsentContainerFailsClosed(t *testing.T) {
	const name = "fixture-no-container"
	require.NoError(t, registerFixtures(enginefixture.Hosting(name)))
	t.Cleanup(func() { UnregisterForTesting(name) })

	assert.False(t, isolation.HasContainerAuth(name))
	assert.NotContains(t, isolation.ComposableEngines(), name)
	assert.NotContains(t, isolation.ContainerAuthEngines(), name)
	kind, ok := Kind(name)
	require.True(t, ok)
	_, err := kind.Container()
	assert.Error(t, err, "the kind refuses Container, and that refusal is the reason")
	_, ok = Kind("never-registered")
	assert.False(t, ok)
}

// Unregistering unwinds the container seam too.
func TestUnregisterForTesting_RemovesTheContainerDeclaration(t *testing.T) {
	const name = "fixture-container-unwound"
	require.NoError(t, Register(enginefixture.RegistryOf(containerFixture(name, engine.DistributionDefault)), enginefixture.Hosting(name)))
	require.True(t, isolation.HasContainerAuth(name))
	UnregisterForTesting(name)
	assert.False(t, isolation.HasContainerAuth(name))
	assert.NotContains(t, isolation.ComposableEngines(), name)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
