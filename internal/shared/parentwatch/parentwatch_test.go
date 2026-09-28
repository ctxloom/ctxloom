package parentwatch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/parentwatch"
)

// While the parent (the test runner) lives, the watched context must stay
// live, and the returned CancelFunc must still release it. The parent-death
// half needs a process whose parent is killed: it is proven end to end by
// isolation's TestIsolateRunner_RunnerDiesWithItsHost.
func TestWithParent_LiveWhileParentLives(t *testing.T) {
	ctx, cancel, err := parentwatch.WithParent(context.Background())
	require.NoError(t, err)
	require.NoError(t, ctx.Err(), "a living parent must not cancel the context")
	cancel()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

// The watched context is derived from the caller's: cancelling the parent
// context reaches it regardless of the process watch.
func TestWithParent_InheritsCallerCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancel, err := parentwatch.WithParent(parent)
	require.NoError(t, err)
	defer cancel()
	cancelParent()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}
