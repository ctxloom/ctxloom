package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// TestSpoolInboxRecv_MailLandingAsItParksIsReceived pins the park race: the
// courier rings only after the file is on disk, and a wake finding no parked
// poll is dropped. Mail that lands after a receive's first claim but before
// its poll is registered therefore rings nobody — the receive must still
// find it rather than sit out its whole wait beside it. onPark runs inside
// exactly that window (registered, not yet blocked), so the file written
// there with no wake at all is the lost-wake interleaving, made deterministic.
func TestSpoolInboxRecv_MailLandingAsItParksIsReceived(t *testing.T) {
	teeHome(t)
	const harp = "owner-harp-lostwake"
	in := newSpoolInbox(report.To(termSink()), spool.NewHomeMapper(), &SpoolDeliveryCounters{},
		func(role string) { writeSpoolMail(t, role, "child-harp-1", KindMessage, "landed as it parked") },
		func(string) {})

	start := time.Now()
	msgs, err := in.recv(context.Background(), harp, 5*time.Second)
	require.NoError(t, err, "the mail was on disk the whole time the receive was parked")
	require.Len(t, msgs, 1)
	assert.Equal(t, "landed as it parked", msgs[0].Body)
	assert.Less(t, time.Since(start), 5*time.Second, "received without waiting out the park")
	assert.False(t, in.parked(harp), "a receive that returned mail leaves no poll behind")
}
