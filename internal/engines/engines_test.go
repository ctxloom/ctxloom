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
		assert.NotNil(t, h.SettingsReader(agent.SettingsOptions{}))
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

// TestUngated_EachEngineNamesItsPostureThatAsksNobody: every composed engine
// answers engine.PermissionModel.Ungated in its own words. Claude's is
// bypassPermissions, the mocks' bypass, and opencode's auto: its --auto
// approves whatever the rules do not deny, and opencode has no reviewer, so
// nobody is asked. A newly composed engine fails here until its row records
// what it answers.
//
// MUTATION: claude's Ungated answers "bypass" → the claude-code row goes red.
func TestUngated_EachEngineNamesItsPostureThatAsksNobody(t *testing.T) {
	type ungated struct {
		posture string
		ok      bool
	}
	want := map[engine.Name]ungated{
		"claude-code":   {"bypassPermissions", true},
		"mock":          {"bypass", true},
		"mock-launch":   {"bypass", true},
		"mock-lossy":    {"bypass", true},
		"mock-noskills": {"bypass", true},
		"opencode":      {"auto", true},
	}
	require.NoError(t, Compose())
	names := Registry().Names(nil)
	require.NotEmpty(t, names, "an empty registry would check nothing")
	for _, name := range names {
		t.Run(string(name), func(t *testing.T) {
			row, listed := want[name]
			require.Truef(t, listed, "engine %s has no row: decide what its Ungated answers", name)
			e, _ := Registry().Lookup(name)
			m, ok := e.Permissions().Get()
			require.True(t, ok, "every composed engine declares a permission model")
			posture, ok := m.Ungated()
			assert.Equal(t, row, ungated{posture, ok})
		})
	}
}
