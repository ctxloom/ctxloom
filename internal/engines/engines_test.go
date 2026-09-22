package engines

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/engine/conformance"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// TestRegister_EveryShippedEngineComposes is what turns a forgotten slot on a
// NEW engine into a red test in seconds: every descriptor in the composition
// root must validate and install, or the process would refuse to start.
func TestRegister_EveryShippedEngineComposes(t *testing.T) {
	require.NoError(t, Compose())
	names := Registry().Names(nil)
	assert.NotEmpty(t, names)
	shippable := Registry().Names(func(d engine.Definition) bool { return d.Distribution != engine.DistributionTestOnly })
	assert.GreaterOrEqual(t, len(shippable), 1, "at least one non-test engine must be composed")
}

// A second call is the same registration, not a duplicate error: TestMains
// and the CLI may both compose in one process.
func TestRegister_IsIdempotent(t *testing.T) {
	require.NoError(t, Compose())
	assert.NoError(t, Compose())
	assert.NotPanics(t, MustCompose)
}

// TestBuild_EveryShippedEngineConforms is the registry-level half of the
// conformance gate: every kind the composition root builds passes the
// declarative suite, exactly one ships by default, and the composed
// registry is Build's.
func TestBuild_EveryShippedEngineConforms(t *testing.T) {
	reg, err := Build()
	require.NoError(t, err)
	for _, name := range reg.Names(nil) {
		e, ok := reg.Lookup(name)
		require.True(t, ok)
		t.Run(string(name), func(t *testing.T) { conformance.Run(t, e) })
	}
	def, err := reg.Default()
	require.NoError(t, err)
	assert.Equal(t, engine.DistributionDefault, def.Root().Distribution)

	require.NoError(t, Compose())
	assert.Equal(t, reg.Names(nil), Registry().Names(nil), "the composed registry names the engines Build composes")
}

// Every shipped kind carries the instance half's remaining contract
// (agent.Hosted): the adapters that drive a Backend assert it on the
// registry's value, so a kind composed without it would be declared and
// unrunnable.
func TestBuild_EveryShippedEngineIsHosted(t *testing.T) {
	reg, err := Build()
	require.NoError(t, err)
	for _, name := range reg.Names(nil) {
		e, _ := reg.Lookup(name)
		h, ok := e.(agent.Hosted)
		require.True(t, ok, "%s is not agent.Hosted", name)
		assert.Equal(t, string(name), h.Backend(nil).Name())
		assert.Equal(t, string(name), h.NewConfig().BackendType(), "a decoded config names its OWN engine")
		assert.NotNil(t, h.SettingsWriter(agent.SettingsOptions{}))
		assert.NotNil(t, h.Declaration())
	}
}

// Use is the test seam: what it installs is what Registry reads, until
// restored.
func TestUse_SwapsTheComposedRegistryUntilRestored(t *testing.T) {
	before := Registry().Names(nil)
	synthetic, err := engine.NewRegistry(mock.NewNamed("synthetic"))
	require.NoError(t, err)
	restore := Use(synthetic)
	assert.Equal(t, []engine.Name{"synthetic"}, Registry().Names(nil))
	restore()
	assert.Equal(t, before, Registry().Names(nil))
}
