package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// TestProvisioning_EveryComposedEngineDeclares is the settling assertion:
// every composed engine — the shipped one and the test doubles together,
// because tests run the same provisioning path — states, on its own Home,
// either the credential material it seeds WITH the deliveries it accepts
// (engine.CredentialSeed.Accept, which HomeSpec.Validate refuses empty), or
// that nothing seeds it. There is no third state: "material to place but no
// delivery it accepts" cannot be authored.
func TestProvisioning_EveryComposedEngineDeclares(t *testing.T) {
	names := Engines().Names(nil)
	require.NotEmpty(t, names)
	for _, name := range names {
		t.Run(string(name), func(t *testing.T) {
			kind, ok := Kind(string(name))
			require.True(t, ok)
			home := kind.Home()
			require.NoError(t, home.Validate())
			if seed, ok := home.Credentials.Get(); ok {
				assert.NotEmpty(t, seed.Accept, "%s seeds material but accepts no delivery", name)
				return
			}
			_, seeded := CredentialSeedFor(string(name)).Get()
			assert.False(t, seeded)
		})
	}
}

// Every test double keeps no home at all: it authenticates against nothing,
// so its Home is the null object and nothing is provisioned for it.
func TestProvisioning_TestDoublesKeepNoHome(t *testing.T) {
	for _, h := range MockHostings() {
		t.Run(string(h.Engine), func(t *testing.T) {
			kind, ok := Kind(string(h.Engine))
			require.True(t, ok)
			assert.False(t, kind.Home().Relocates(), "%s keeps no engine-global state", h.Engine)
			d := CredentialSeedFor(string(h.Engine))
			_, seeded := d.Get()
			assert.False(t, seeded)
			assert.Contains(t, d.AbsentReason(), string(h.Engine), "the reason names the engine it is about")
		})
	}
}

var _ = engine.Name("")
