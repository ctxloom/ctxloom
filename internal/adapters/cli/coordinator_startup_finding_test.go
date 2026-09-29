package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestRecordCoordinatorStartupFinding_Degradable: a coordinator that cannot
// stand up costs this session its delegation and nothing else — every
// child's mail is a file spool, so no work already in flight is stranded.
// The fault is reported in both modes and fatal only in strict mode.
func TestRecordCoordinatorStartupFinding_Degradable(t *testing.T) {
	cerr := errors.New("listener bind failed")

	t.Run("degraded mode records the fault and proceeds", func(t *testing.T) {
		strictness.Reset()
		t.Cleanup(func() { strictness.Reset() })

		recordCoordinatorStartupFinding(cerr)

		found := strictness.All()
		require.Len(t, found, 1, "the fault is still reported — degraded suppresses fatality, not recording")
		assert.Equal(t, strictness.ClassApply, found[0].Class)
		assert.False(t, found[0].NonDegradable,
			"children reach the owner by file spool, so losing the coordinator costs nothing that must stop a launch")
		assert.Empty(t, strictness.Mode{Degraded: true}.Actionable(found),
			"and under --degraded it must NOT be actionable: this launch proceeds")
		assert.Contains(t, found[0].FixIt, "--degraded",
			"a degradable finding names --degraded as its way out")
	})

	t.Run("strict mode aborts", func(t *testing.T) {
		strictness.Reset()
		t.Cleanup(strictness.Reset)
		recordCoordinatorStartupFinding(cerr)
		assert.NotEmpty(t, strictness.Mode{}.Actionable(strictness.All()),
			"strict mode acts on the finding")
	})
}

// TestRecordCoordinatorStartupFinding_SecondOwnerIsItsOwnClass: a project
// another live session already owns is refused in ClassOwner, naming the
// owned state, and stays degradable — --degraded launches the second session
// without delegation, never as a second owner.
func TestRecordCoordinatorStartupFinding_SecondOwnerIsItsOwnClass(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)

	recordCoordinatorStartupFinding(fmt.Errorf("wrapped: %w", coord.ErrStateOwned))

	found := strictness.All()
	require.Len(t, found, 1)
	assert.Equal(t, strictness.ClassOwner, found[0].Class, "an owned project is not an apply failure")
	assert.Contains(t, found[0].Message, coord.ErrStateOwned.Error(), "the refusal names the owned state")
	assert.False(t, found[0].NonDegradable)
	assert.Empty(t, strictness.Mode{Degraded: true}.Actionable(found), "--degraded proceeds without delegation")
	assert.NotEmpty(t, strictness.Mode{}.Actionable(found), "strict mode refuses")
	assert.Contains(t, found[0].FixIt, "ctxloom doctor", "the remedy points at where the owner can be seen")
}
