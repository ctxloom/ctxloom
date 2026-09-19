package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// Register pushes every engine's credential-seed declaration down to
// isolation at the moment it installs the descriptor, so an engine cannot be
// launchable here and unseedable there. The push is unconditional — a
// declared ABSENCE is pushed too — which is what makes "no seed for this
// engine" a readable decision at the seam rather than a lookup miss.
func TestRegister_PushesTheCredentialSeedDeclarationToIsolation(t *testing.T) {
	const name = "fixture-seeded"
	d := enginefixture.Descriptor(name)
	d.Home = agent.Provide(agent.EngineHome{
		Vars: []agent.HomeVar{{EnvVar: "FIXTURE_HOME", Subdir: "fixture-home"}},
		Credentials: agent.Provide(agent.CredentialSeed{
			Subdir:     "fixture-home",
			EnvTrigger: "FIXTURE_KEY",
			LoginHint:  "fixture login",
			Files:      []agent.SeedFile{{HostRelHome: ".fixture/creds.json", DestName: "creds.json", Required: true}},
		}),
	})
	require.NoError(t, Register(d))
	t.Cleanup(func() { UnregisterForTesting(name) })

	assert.Equal(t, []isolation.AmbientFile{
		{HostRel: ".fixture/creds.json", DestRel: "fixture-home/creds.json", Mode: 0o600, Required: true},
	}, isolation.AmbientSet(name), "the engine's own declared files are what isolation seeds")
	assert.Contains(t, isolation.AmbientEngineNames(), name)
}

// Every registered backend has an EXPLICIT entry at the seam — provided or
// declared absent — so the registry is a subset of the ambient roster by
// construction, and an engine outside it is one nobody registered.
func TestRegister_EveryRegisteredBackendHasAnAmbientDeclaration(t *testing.T) {
	const absentName = "fixture-unseeded"
	require.NoError(t, Register(enginefixture.Descriptor(absentName)))
	t.Cleanup(func() { UnregisterForTesting(absentName) })

	declared := isolation.AmbientEngineNames()
	for _, name := range List() {
		assert.Contains(t, declared, name, "%s is registered but has no ambient declaration at the seam", name)
	}
	assert.Nil(t, isolation.AmbientSet(absentName), "a declared absence seeds nothing")
	assert.NotEmpty(t, CredentialSeedFor(absentName).AbsentReason(), "and says why")
}

// Unregistering unwinds the seam too, so a test's synthetic engine does not
// linger as a seedable name for later tests.
func TestUnregisterForTesting_RemovesTheCredentialSeedDeclaration(t *testing.T) {
	const name = "fixture-unwound"
	require.NoError(t, Register(enginefixture.Descriptor(name)))
	require.Contains(t, isolation.AmbientEngineNames(), name)
	UnregisterForTesting(name)
	assert.NotContains(t, isolation.AmbientEngineNames(), name)
}
