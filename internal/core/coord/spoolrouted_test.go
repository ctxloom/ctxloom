package coord

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport/spooltest"
)

// A routed out/ file is deleted with its identity recorded in out/routed/
// (spool.Consume), and the identity rides through peerSend onto the
// recipient's copy. Between them they close both crash windows of the
// route-then-record order. Each test below FORCES its window: it lets the live
// pipeline route a message, then rebuilds on disk the exact state the crash
// would have left, and sweeps.

// awaitRouted is spooltest.AwaitRouted at this suite's wait.
func awaitRouted(t *testing.T, harp, identity, why string) {
	t.Helper()
	spooltest.AwaitRouted(t, harp, identity, conformanceWait, why)
}

// routeOnce has the child at out.Harp send one message carrying origin to its
// parent (the owner), and waits until it is routed, delivered to the owner and
// recorded on both sides.
func routeOnce(t *testing.T, c *Coordinator, childHarp, origin, body string) {
	t.Helper()
	writeChildOut(t, childHarp, origin, body)
	c.sweepChildOut(childHarp) // no doorbell rang: the test is the trigger
	got := recvBody(t, c, body, conformanceWait)
	require.Len(t, got, 1, "the first route delivers the message once")
	awaitRouted(t, childHarp, origin, "after the first route")
	awaitDelivered(t, ownerIdentity().Harp, origin,
		"the owner's copy carries the out/ file's identity, not a freshly minted one")
}

// writeChildOut writes one out/ message carrying origin, as a child's runner
// would.
func writeChildOut(t *testing.T, childHarp, origin, body string) {
	t.Helper()
	w, err := spool.NewWriter(afero.NewOsFs(), spool.NewHomeMapper(), childHarp, spool.DirOut, childHarp)
	require.NoError(t, err)
	_, err = w.Write(&spool.Message{Kind: KindResult, FromHarp: childHarp, To: ParentAddress, OriginID: origin, Body: body})
	require.NoError(t, err)
}

// ownerHolds reports whether the owner's inbox — waiting or claimed — holds a
// file whose body is body.
func ownerHolds(t *testing.T, body string) bool {
	t.Helper()
	for _, d := range []spool.Dir{spool.DirIn, spool.ClaimedDirName} {
		for _, e := range spoolEntries(t, ownerIdentity().Harp, d) {
			if e.Message.Body == body {
				return true
			}
		}
	}
	return false
}

// CRASH BETWEEN ROUTE AND RECORD: the recipient has the message, the out/ file
// is still there, and nothing is recorded. The next sweep routes it again —
// and because the re-route carries the out/ file's identity, the recipient's
// delivered record refuses the copy: delivered once.
func TestSpoolRouting_ACrashBetweenRouteAndRecordDeliversOnce(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")
	const origin, body = "m-route-crash", "routed before the crash"
	routeOnce(t, c, out.Harp, origin, body)

	root, err := spool.Root(spool.NewHomeMapper(), out.Harp)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(root, "out", "routed", origin)), "the record never got written")
	writeChildOut(t, out.Harp, origin, body)

	c.sweepChildOut(out.Harp)

	awaitRouted(t, out.Harp, origin, "the re-route is recorded and the file deleted")
	assert.Empty(t, recvBody(t, c, body, 300*time.Millisecond), "the re-routed copy is refused by the owner's delivered record")
	assert.False(t, ownerHolds(t, body), "and its file is discarded, not left waiting")
}

// CRASH BETWEEN RECORD AND DELETE: the out/ file is still there and its
// identity is recorded. The next sweep finishes the delete and does NOT route
// it again — no copy reaches the recipient's inbox at all.
func TestSpoolRouting_ARecordedOutFileIsFinishedNotRouted(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")
	const origin, body = "m-record-crash", "recorded before the crash"
	routeOnce(t, c, out.Harp, origin, body)

	writeChildOut(t, out.Harp, origin, body)
	c.sweepChildOut(out.Harp)

	assert.Empty(t, spoolEntries(t, out.Harp, spool.DirOut), "the interrupted delete is finished")
	assert.False(t, ownerHolds(t, body), "a recorded out/ file is never routed again")
}
