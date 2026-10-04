package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// TestFakeSpawner_ARunnerHalfEndsWithTheTestThatSpawnedIt: the runner half
// fakeSpawner.Start builds lives under its own context, detached from the
// coordinator on purpose (a restarted coordinator re-adopts it), so the only
// thing that can end it is the test. One left running keeps redialling an
// absent coordinator and, when the owner-loss window expires, warns through
// clidiag's PROCESS-WIDE sink — into whatever unsynchronised buffer a LATER
// test has installed there, concurrently with that test's own writes and
// reads (the -race finding TestTrackedGroup_BoundedJoinOmitsAnEmptyRiskClause
// was charged with).
//
// The interleaving is forced, not waited for: the spawning test ends, its
// cleanups run, and the very next statement in the enclosing test is the
// later test's moment. A runner half still live there can write into it.
func TestFakeSpawner_ARunnerHalfEndsWithTheTestThatSpawnedIt(t *testing.T) {
	var sp *fakeSpawner
	t.Run("spawning test", func(t *testing.T) {
		sp = newFakeSpawner(t, nil, nil)
		// A coordinator that is not there: the runner half dials, fails and
		// waits on its owner — exactly the state a closed test coordinator
		// leaves it in.
		_, err := sp.Start(context.Background(),
			launch.Launch{Engine: "mock", Identity: sessions.Identity{RunID: "run-1"}},
			sessions.Endpoint{URL: "http://127.0.0.1:1/mcp", Credential: "t"})
		require.NoError(t, err)
	})

	home := sp.engineHome(0)
	require.NotNil(t, home, "the spawning test started a runner half")
	select {
	case <-home.Done():
	default:
		sp.killEngine(0) // do not leak it into the rest of this binary
		t.Fatal("the fake's runner half outlived the test that spawned it: it can still warn into a later test's clidiag sink")
	}
}
