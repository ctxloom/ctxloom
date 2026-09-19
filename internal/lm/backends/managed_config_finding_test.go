package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

func resetStrictness(t *testing.T) strictness.Mark {
	t.Helper()
	strictness.Reset()
	t.Cleanup(func() {
		strictness.Reset()
	})
	return strictness.Checkpoint()
}

// A run without a configuration has no managed surfaces to assemble. The
// refusal for a configuration that cannot be READ is upstream — config.Open
// hands out no owner, so no run reaches this function — which is why a nil
// here yields a nil payload and no finding of its own.
func TestAssembleManagedConfig_NilConfig_YieldsNoManagedSet(t *testing.T) {
	mark := resetStrictness(t)

	got := AssembleManagedConfig(nil, "claude-code", t.TempDir(), nil)

	assert.Nil(t, got)
	assert.Empty(t, strictness.Since(mark))
}

// The control: a configuration the test constructs assembles a payload and
// raises nothing, so the assertion is a property of AssembleManagedConfig and
// not of whatever .ctxloom the checkout happens to hold.
func TestAssembleManagedConfig_LoadableConfigRaisesNothing(t *testing.T) {
	mark := resetStrictness(t)

	got := AssembleManagedConfig(&config.Config{}, "claude-code", t.TempDir(), nil)

	assert.NotNil(t, got)
	assert.Empty(t, strictness.Since(mark),
		"a loadable config must not record a finding: an unconditional finding would refuse every launch")
}
