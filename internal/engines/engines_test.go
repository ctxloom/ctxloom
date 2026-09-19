package engines

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/engine/conformance"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
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

// TestBuild_EveryShippedEngineConforms is the registry-level half of the
// conformance gate: every kind the composition root builds passes the
// declarative suite, exactly one ships by default, and the two registries
// (the kinds and the hosting records) name the same engines.
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

	require.NoError(t, Register())
	var kinds []string
	for _, n := range reg.Names(nil) {
		kinds = append(kinds, string(n))
	}
	assert.Equal(t, backends.List(), kinds, "the hosting registry and the kind registry name the same engines")
}
