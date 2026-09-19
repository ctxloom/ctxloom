package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// TestSpawnReachURL_RefusesInEveryMode pins that a child with no coordinator
// reach-back is refused under --degraded as well as in strict mode.
//
// The child's mail is a file spool, but the spool is swept by the child's OWN
// runner, which learns which spool is its own and rings its doorbells over
// the run channel it dials home on. No reach-back means no runner sweeping
// the child's in/ and nothing routing what it writes to out/: the child would
// run, spend quota and produce work nobody receives. That is lost work, and
// lost work is what --degraded must never permit.
func TestSpawnReachURL_RefusesInEveryMode(t *testing.T) {
	// A coordinator built but never served: ReachURL has no listener to
	// advertise, so spawnReachURL always takes its failure path. This is the
	// same un-served construction TestAgentRun_AbortedSpawnReleasesTheHarp
	// uses to reach the identical branch.
	newUnserved := func(t *testing.T) *Coordinator {
		t.Helper()
		teeHome(t)
		c, err := New(Options{
			ProjectDir: t.TempDir(),
			StateDir:   t.TempDir(),
			Spawner:    newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil),
			OwnerHarp:  "owner-harp",
		})
		require.NoError(t, err)
		t.Cleanup(c.Close)
		return c
	}

	for _, degraded := range []bool{false, true} {
		resetStrictness(t)
		c := newUnserved(t)

		url, err := c.spawnReachURL("child-harp", launch.RuntimeRootless)

		require.Error(t, err,
			"degraded=%v must NOT launch a child whose runner could never dial home: its work would be lost", degraded)
		assert.Empty(t, url)
		// A refusal that does not carry the way out just relocates the dead end.
		assert.Contains(t, err.Error(), "bridge network",
			"the refusal must name the repair — the endpoint itself")
	}
}
