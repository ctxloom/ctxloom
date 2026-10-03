package coord

import (
	"os"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// writeChildOut writes one out/ message carrying origin, as a child's runner
// would.
func writeChildOut(t *testing.T, childHarp, origin, body string) spool.Ref {
	t.Helper()
	w, err := spool.NewWriter(afero.NewOsFs(), spool.NewHomeMapper(), childHarp, spool.DirOut, childHarp)
	require.NoError(t, err)
	ref, err := w.Write(&spool.Message{Kind: KindResult, FromHarp: childHarp, To: ParentAddress, OriginID: origin, Body: body})
	require.NoError(t, err)
	return ref
}

// CRASH BETWEEN ROUTE AND CONSUME: the recipient has the message and the
// child's out/ file was never moved into out/consumed/. The next sweep routes
// it again — and because the re-route carries the out/ file's identity rather
// than a freshly minted id, the recipient's delivered record refuses the copy.
//
// The window is FORCED: the test lets the live pipeline route and consume the
// message once, then moves the routed copy back into out/, which is exactly
// the disk state the crash leaves.
func TestSpoolRouting_ACrashBetweenRouteAndConsumeDeliversOnce(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")
	const origin, body = "m-route-crash", "routed before the crash"

	ref := writeChildOut(t, out.Harp, origin, body)
	c.sweepChildOut(out.Harp) // no doorbell rang: the test is the trigger
	require.Len(t, recvBody(t, c, body, conformanceWait), 1, "the first route delivers the message once")
	awaitDelivered(t, ownerIdentity().Harp, origin,
		"the owner's copy carries the out/ file's identity, not a freshly minted one")

	m := spool.NewHomeMapper()
	livePath, err := m.Resolve(ref)
	require.NoError(t, err)
	routedPath, err := m.Resolve(spool.Ref{Harp: ref.Harp, Dir: spool.DirOutConsumed, Name: ref.Name})
	require.NoError(t, err)
	require.NoError(t, os.Rename(routedPath, livePath), "the consume never happened")

	c.sweepChildOut(out.Harp)

	assert.Empty(t, spoolEntries(t, out.Harp, spool.DirOut), "the re-route is consumed")
	assert.Empty(t, recvBody(t, c, body, 300*time.Millisecond), "the re-routed copy is refused by the owner's delivered record")
}
