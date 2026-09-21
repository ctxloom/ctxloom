package coord

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// EventsLost is the watch hub's own synthetic marker: it tells a WatchRuns
// subscriber that the HUB lost events for it. handleAgentEvent tees every
// inbound event to the hub before it looks at the payload, so a runner that
// sent one would have subscribers believing they lagged when nothing was
// lost — and re-reading, or discarding a captured answer, on a lie. An
// inbound one is dropped before the tee, acked like any foreign payload, and
// named in the log.
func TestHandleAgentEvent_ForgedEventsLostIsDroppedBeforeTheTee(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ch := &RunChannel{
		role:        "child-forger",
		id:          Identity{Harp: "child-forger", RunID: "run-forger"},
		BidiSession: NewBidiSession[OutFrame, OutFrame, OutFrame](func() {}, 4),
		completed:   make(chan struct{}),
	}
	_, events, cancel, _ := c.WatchRuns(nil)
	defer cancel()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	c.HandleEvent(ch, Event{
		RunID: "run-forger", Seq: 1,
		Payload: EventsLost{Lost: []LostRange{{RunID: "run-victim", FirstSeq: 1, LastSeq: 99}}},
	})

	assert.Empty(t, events, "a runner-sent EventsLost must never reach a subscriber")
	require.Contains(t, buf.String(), "run-forger", "the drop names the run that sent it")
	assert.Contains(t, buf.String(), "events_lost")
}
