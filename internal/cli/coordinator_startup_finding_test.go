package cli

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestRecordCoordinatorStartupFinding_BothArms is the owner-side half of the
// audit's conditional refusal (item #4): a coordinator that cannot stand up
// strands every child this session would spawn — unless delegation.spool_delivery
// gives them a route home by file, in which case the degrade costs reach-back
// and loses no work.
//
// BOTH ARMS ARE ASSERTED. Testing only the refusing arm would leave the
// permitting arm — the one that must not regress into a refusal for projects
// that spool — covered by nothing.
func TestRecordCoordinatorStartupFinding_BothArms(t *testing.T) {
	cerr := errors.New("listener bind failed")

	t.Run("REFUSING ARM: spool delivery off is non-degradable", func(t *testing.T) {
		strictness.Reset()
		strictness.SetDegraded(true)
		t.Cleanup(func() { strictness.Reset(); strictness.SetDegraded(false) })

		recordCoordinatorStartupFinding(false, cerr)

		found := strictness.All()
		require.Len(t, found, 1)
		assert.Equal(t, strictness.ClassApply, found[0].Class)
		assert.True(t, found[0].NonDegradable,
			"with no route home the children are stranded and their work unrecoverable: refuse in both modes")
		// The assertion that proves it actually refuses, rather than merely
		// being marked: Actionable is the single place the mode is consulted.
		assert.NotEmpty(t, strictness.Actionable(found),
			"the finding must survive Actionable under --degraded or the gate drops it and the session launches anyway")
		assert.Contains(t, found[0].FixIt, "delegation.spool_delivery",
			"a refusal must carry the fix that makes this degrade safe")
		assert.NotContains(t, found[0].FixIt, "--degraded",
			"a non-degradable finding must not offer --degraded as its remedy")
	})

	t.Run("PERMITTING ARM: spool delivery on stays degradable", func(t *testing.T) {
		strictness.Reset()
		strictness.SetDegraded(true)
		t.Cleanup(func() { strictness.Reset(); strictness.SetDegraded(false) })

		recordCoordinatorStartupFinding(true, cerr)

		found := strictness.All()
		require.Len(t, found, 1, "the fault is still reported — degraded suppresses fatality, not recording")
		assert.False(t, found[0].NonDegradable,
			"children reach the owner by file spool, so losing reach-back costs nothing that must stop a launch")
		assert.Empty(t, strictness.Actionable(found),
			"and under --degraded it must NOT be actionable: this launch proceeds")
	})

	t.Run("strict mode aborts on BOTH arms", func(t *testing.T) {
		// Whatever the transport, a coordinator that failed to start is a
		// fault strict mode reports. The conditional decides degraded-mode
		// fatality only; it must not quietly excuse the fault in strict mode.
		for _, delivery := range []bool{false, true} {
			strictness.Reset()
			strictness.SetDegraded(false)
			recordCoordinatorStartupFinding(delivery, cerr)
			assert.NotEmpty(t, strictness.Actionable(strictness.All()),
				"strict mode acts on the finding regardless of spool_delivery=%v", delivery)
		}
		strictness.Reset()
	})
}
