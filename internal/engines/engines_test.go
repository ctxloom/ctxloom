package engines

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
