package backends

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// swapConfigLoader points the seam at a stub for one test.
func swapConfigLoader(t *testing.T, fn func(...config.LoadOption) (*config.Config, error)) {
	t.Helper()
	prev := loadConfigFn
	loadConfigFn = fn
	t.Cleanup(func() { loadConfigFn = prev })
}

func resetStrictness(t *testing.T) strictness.Mark {
	t.Helper()
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})
	return strictness.Checkpoint()
}

// TestAssembleManagedConfig_UnloadableConfigIsAFinding pins what makes a
// config-load failure VISIBLE.
//
// On a nil return the run proceeds with no hooks, no MCP and no commands while
// looking entirely healthy — exit 0, the turn answers, nothing delivered. This
// project's characteristic failure shape, and before this it was reachable by
// any config-load hiccup with only a stderr warning behind it.
//
// The assertion is that a FINDING is recorded, not that the process exits:
// strictness owns fatal-vs-warn centrally and this site must not branch on it.
func TestAssembleManagedConfig_UnloadableConfigIsAFinding(t *testing.T) {
	mark := resetStrictness(t)
	swapConfigLoader(t, func(...config.LoadOption) (*config.Config, error) {
		return nil, errors.New("config.yaml: unreadable")
	})

	got := AssembleManagedConfig("claude-code", t.TempDir(), nil, nil)

	found := strictness.Since(mark)
	require.NotEmpty(t, found,
		"an unloadable config recorded NO finding: the run would proceed with no managed surfaces and nothing would say so")
	assert.Equal(t, strictness.ClassConfig, found[0].Class)
	assert.NotEmpty(t, found[0].FixIt,
		"the refusal is the whole user interface for this failure; it must state the remedy")
	assert.False(t, found[0].NonDegradable,
		"launching without managed surfaces is not itself the harm, so --degraded must be able to pass this")
	assert.Nil(t, got, "an unloadable config must not yield a managed set")
}

// TestAssembleManagedConfig_LoadableConfigRaisesNothing is the CONTROL. Without
// it the test above passes just as happily against a function that reports a
// finding unconditionally — which would refuse every launch on every machine.
func TestAssembleManagedConfig_LoadableConfigRaisesNothing(t *testing.T) {
	mark := resetStrictness(t)
	swapConfigLoader(t, func(opts ...config.LoadOption) (*config.Config, error) {
		return config.Load(opts...)
	})

	_ = AssembleManagedConfig("claude-code", t.TempDir(), nil, nil)

	assert.Empty(t, strictness.Since(mark),
		"a loadable config must raise nothing — otherwise the finding above proves only that this function always reports")
}
