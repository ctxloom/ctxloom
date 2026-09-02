package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// TestPrimaryEngines_AreAllRegisteredBackends is the floor for the curated
// menu: every name it offers must be a backend this build can actually run.
//
// An unregistered name here is not a cosmetic error — init OFFERS it, the user
// PICKS it, and the failure lands later at launch on an engine ctxloom does
// not have. This gate reads backends.List() live and names no engine, so
// adding a correctly-registered engine never requires editing it; only a
// roster entry that has drifted out of registration trips it.
func TestPrimaryEngines_AreAllRegisteredBackends(t *testing.T) {
	require.NotEmpty(t, primaryEngines, "an empty curated menu offers the user nothing")
	registered := backends.List()
	require.NotEmpty(t, registered, "no backend is registered; this comparison would be vacuous")

	for _, name := range primaryEngines {
		assert.Contains(t, registered, name,
			"primaryEngines offers %q, which is not a registered backend — init would accept a choice that cannot launch", name)
	}
}
