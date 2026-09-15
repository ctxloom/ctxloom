package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestSpawnReachURL_DegradedRefusesOnlyWhenWorkWouldBeLost pins the audit's one
// CONDITIONAL refusal (item #4, ruled 2026-09-15): losing coordinator
// reach-back is damaging in some projects and harmless in others, so the site
// reads delegation.spool_delivery to decide its own fatality.
//
// BOTH ARMS ARE ASSERTED, and that is the point of the test rather than an
// afterthought. A conditional refusal tested only on the refusing arm leaves
// the PERMITTING arm unverified — and the permitting arm is the one that must
// not silently regress into a refusal for projects that spool, which would
// break --degraded exactly where the degrade provably costs nothing.
//
// Both cases run with --degraded ON. In strict mode this site already refused
// in both configurations, so strict mode cannot tell the two arms apart.
func TestSpawnReachURL_DegradedRefusesOnlyWhenWorkWouldBeLost(t *testing.T) {
	// A coordinator built but never served: ReachURL has no listener to
	// advertise, so spawnReachURL always takes its failure path. This is the
	// same un-served construction TestAgentRun_AbortedSpawnReleasesTheHarp
	// uses to reach the identical branch.
	newUnserved := func(t *testing.T, spoolDelivery bool) *Coordinator {
		t.Helper()
		opts := Options{
			ProjectDir:    t.TempDir(),
			StateDir:      t.TempDir(),
			Spawner:       newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil),
			SpoolDelivery: spoolDelivery,
		}
		if spoolDelivery {
			// New refuses spool delivery without it: "the owner's inbox is a
			// spool and this process is its reader", so a cutover coordinator
			// that did not know whose inbox to read would deliver nothing.
			opts.OwnerHarp = "owner-harp"
		}
		c, err := New(opts)
		require.NoError(t, err)
		t.Cleanup(c.Close)
		return c
	}

	t.Run("REFUSING ARM: spool delivery off, the child's work would be lost", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		c := newUnserved(t, false)

		url, err := c.spawnReachURL("child-harp", agent.RuntimeContainerRootless)

		require.Error(t, err,
			"--degraded must NOT strand a child whose mail has no route: it would run, spend quota and produce work nobody receives")
		assert.Empty(t, url)
		// A refusal that does not carry the fix just relocates the dead end.
		assert.Contains(t, err.Error(), "delegation.spool_delivery",
			"the refusal must name the config that makes this degrade safe")
		assert.Contains(t, err.Error(), "bridge network",
			"and the other way out — repairing the endpoint itself")
	})

	t.Run("PERMITTING ARM: spool delivery on, mail rides files and nothing is lost", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		c := newUnserved(t, true)

		url, err := c.spawnReachURL("child-harp", agent.RuntimeContainerRootless)

		require.NoError(t, err,
			"with mail delivered from the file spool the child still reaches its parent, so --degraded must keep working here")
		assert.Empty(t, url, "there is still no dial-home URL; the spool is what carries the mail")
	})

	t.Run("strict mode refuses under BOTH postures", func(t *testing.T) {
		// The conditional governs the DEGRADED path only. Strict mode has
		// always refused an unreachable endpoint and still does, whatever the
		// transport — otherwise turning spool delivery on would quietly weaken
		// strict mode, which no part of this ruling asked for.
		for _, delivery := range []bool{false, true} {
			resetStrictness(t)
			c := newUnserved(t, delivery)
			_, err := c.spawnReachURL("child-harp", agent.RuntimeContainerRootless)
			require.Error(t, err, "strict mode refuses regardless of spool_delivery=%v", delivery)
		}
	})
}
