package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
)

func TestSpoolCredit_ASeededListingIsNeverCredited(t *testing.T) {
	var s spoolCredit
	at := time.Now()
	s.seed("kid", map[string]time.Time{"m-history": at})
	got := s.credit("kid", map[string]time.Time{"m-history": at, "m-new": at})
	assert.Equal(t, 1, got, "what the record held at start is history; only the new entry is progress")
}

func TestSpoolCredit_AnEntryIsCreditedOnce(t *testing.T) {
	var s spoolCredit
	at := time.Now()
	ids := map[string]time.Time{"m-1": at}
	assert.Equal(t, 1, s.credit("kid", ids), "an unseeded role's record is all new")
	assert.Equal(t, 0, s.credit("kid", ids), "a second sweep of the same record credits nothing")
	ids["m-2"] = at
	assert.Equal(t, 1, s.credit("kid", ids), "only the new entry")
	assert.Equal(t, 1, s.credit("other", map[string]time.Time{"m-1": at}),
		"roles are remembered separately")
}

// The memory is the last listing, replaced, so it is bounded by the record:
// an entry pruned from the record is forgotten.
func TestSpoolCredit_TheMemoryIsTheLastListing(t *testing.T) {
	var s spoolCredit
	at := time.Now()
	s.credit("kid", map[string]time.Time{"m-1": at, "m-2": at})
	s.credit("kid", map[string]time.Time{"m-2": at})
	assert.Len(t, s.seen["kid"], 1, "the pruned entry is not remembered")
}

// TestSpoolCredit_ACoordinatorRestartCreditsNothingNew is the restart pin,
// FORCED rather than waited for: a fresh coordinator on the same state is
// made to sweep the child's delivered record directly, and a delivery the
// previous coordinator already saw must neither count as progress nor forgive
// a relaunch budget. A delivery recorded after the restart is credited once.
func TestSpoolCredit_ACoordinatorRestartCreditsNothingNew(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	stateDir := t.TempDir()

	sp := cutoverSpawner(0)
	first, err := New(Options{
		ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: sp, OwnerHarp: ownerIdentity().Harp,
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(first))
	out, _ := awaitCutoverChild(t, first, sp, "first task")
	msgID, _, err := first.peerSend(ownerIdentity(), out.Harp, KindMessage, "before the restart", nil, "")
	require.NoError(t, err)
	awaitDelivered(t, out.Harp, msgID, "before the restart")
	require.Eventually(t, func() bool { return first.SpoolDeliveryStats().Consumed >= 1 }, conformanceWait, 10*time.Millisecond,
		"the first coordinator credits the delivery it saw")
	first.Close()

	second, err := New(Options{
		ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: newFakeSpawner(nil, nil), OwnerHarp: ownerIdentity().Harp,
	})
	require.NoError(t, err)
	t.Cleanup(second.Close)
	second.mu.Lock()
	second.launchGateLocked(out.Harp).relaunches = 2
	second.mu.Unlock()

	second.sweepChildDelivered(out.Harp)
	assert.Zero(t, second.SpoolDeliveryStats().Consumed, "a restart credits nothing recorded before it")
	second.mu.Lock()
	relaunches := second.launchGateLocked(out.Harp).relaunches
	second.mu.Unlock()
	assert.Equal(t, 2, relaunches, "history must not forgive a relaunch budget")

	// A delivery recorded AFTER the restart is real progress, credited once.
	w, err := spool.NewWriter(spool.NewHomeMapper(), out.Harp, spool.DirIn, spoolWriterIDCoordinator)
	require.NoError(t, err)
	ref, err := w.Write(&spool.Message{Kind: KindMessage, FromHarp: ownerIdentity().Harp, To: out.Harp,
		OriginID: "m-after-restart", Body: "after"})
	require.NoError(t, err)
	require.NoError(t, spool.Deliver(spool.NewHomeMapper(), ref, "m-after-restart", time.Now()))
	second.sweepChildDelivered(out.Harp)
	second.sweepChildDelivered(out.Harp)
	assert.EqualValues(t, 1, second.SpoolDeliveryStats().Consumed, "credited exactly once")
	second.mu.Lock()
	relaunches = second.launchGateLocked(out.Harp).relaunches
	second.mu.Unlock()
	assert.Zero(t, relaunches, "real progress forgives the relaunch budget")
}
