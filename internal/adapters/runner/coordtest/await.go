package coordtest

import (
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/stretchr/testify/require"
)

// AwaitHome waits for the runner hosting runID to have been spawned. A spawn
// is asynchronous at the coordinator (agent_run returns before the runner
// exists), so a test that needs the child's own Home waits here.
func (r *Runners) AwaitHome(t *testing.T, runID string) *runner.Home {
	t.Helper()
	var h *runner.Home
	require.Eventually(t, func() bool {
		h = r.Home(runID)
		return h != nil
	}, AwaitTimeout, 10*time.Millisecond, "the runner for %s was never spawned", runID)
	return h
}

// AwaitEngine waits for the n-th spawned engine to have received its first
// turn — the moment its recorded Request is inspectable.
func (r *Runners) AwaitEngine(t *testing.T, n int) *Engine {
	t.Helper()
	var e *Engine
	require.Eventually(t, func() bool {
		e = r.Engine(n)
		if e == nil {
			return false
		}
		_, ok := e.Request()
		return ok
	}, AwaitTimeout, 10*time.Millisecond, "spawn #%d never reached the engine's Chat call", n)
	return e
}
