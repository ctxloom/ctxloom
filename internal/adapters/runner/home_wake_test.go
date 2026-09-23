package runner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// The runner owns everything about a wake that does not depend on the
// engine: fire only when mail is pending, arm the nonce BEFORE firing, keep
// one wake in flight, and disarm a wake that never went out.

// recordingWake reports each nonce it is fired with, and whether that nonce
// was already armed on disk at the moment of firing.
type recordingWake struct {
	h     *Home
	err   error
	fired chan string
	armed atomic.Bool
}

func (w *recordingWake) Fire(_ context.Context, nonce string) error {
	out, _ := spool.OutstandingWake(w.h.cfg.Mapper, w.h.Harp())
	for _, n := range out {
		if n == nonce {
			w.armed.Store(true)
		}
	}
	w.fired <- nonce
	return w.err
}

func newWakeHome(t *testing.T) (*Home, *recordingWake) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	h := newNoticeHome(t)
	h.identity = ownerIdentity()
	h.cfg.Mapper = spool.NewHomeMapper()
	w := &recordingWake{h: h, fired: make(chan string, 4)}
	h.SetWake(w)
	return h, w
}

func seedOwnerIn(t *testing.T, h *Home) {
	t.Helper()
	wr, err := spool.NewWriter(h.cfg.Mapper, h.Harp(), spool.DirIn, "coord")
	require.NoError(t, err)
	_, err = wr.Write(&spool.Message{Kind: "message", FromHarp: "child", To: h.Harp(), Body: "hi\n"})
	require.NoError(t, err)
}

func outstandingOf(t *testing.T, h *Home) []string {
	t.Helper()
	out, err := spool.OutstandingWake(h.cfg.Mapper, h.Harp())
	require.NoError(t, err)
	return out
}

func TestHomeWake_ANoticeWithMailPendingFiresAnArmedWake(t *testing.T) {
	h, w := newWakeHome(t)
	seedOwnerIn(t, h)

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-1", Text: "hi"})

	select {
	case nonce := <-w.fired:
		assert.True(t, w.armed.Load(), "the nonce is on disk before the wake fires")
		assert.Equal(t, []string{nonce}, outstandingOf(t, h), "…and stays armed until the hook redeems it")
	case <-time.After(5 * time.Second):
		t.Fatal("a notice with mail pending must fire the wake")
	}
}

func TestHomeWake_OneWakeInFlightIsEnough(t *testing.T) {
	h, w := newWakeHome(t)
	seedOwnerIn(t, h)
	h.fireWake(w)
	<-w.fired
	seedOwnerIn(t, h)
	h.fireWake(w)
	assert.Empty(t, w.fired, "the outstanding wake's hook drains everything; a second wake would be a wasted turn")
	assert.Len(t, outstandingOf(t, h), 1)
}

func TestHomeWake_AWakeThatFailsIsDisarmed(t *testing.T) {
	h, w := newWakeHome(t)
	w.err = errors.New("held: a draft is open")
	seedOwnerIn(t, h)
	h.fireWake(w)
	<-w.fired
	assert.Empty(t, outstandingOf(t, h), "a wake that never went out must not block the next one")
}

func TestHomeWake_NothingPendingFiresNothing(t *testing.T) {
	h, w := newWakeHome(t)
	h.fireWake(w)
	assert.Empty(t, w.fired)
	assert.Empty(t, outstandingOf(t, h))
}

func TestHomeWake_AWakeSupersedesTheTerminalNudge(t *testing.T) {
	h, _ := newWakeHome(t)
	var nudged atomic.Bool
	h.SetTerminalNudge(func() { nudged.Store(true) })
	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-1", Text: "hi"})
	assert.False(t, nudged.Load(), "a session with a wake is woken, never also typed at by the old nudge")
}

func TestHomeWake_ASecondRegistrationIsRefused(t *testing.T) {
	h, first := newWakeHome(t)
	second := &recordingWake{h: h, fired: make(chan string, 1)}
	h.SetWake(second)
	seedOwnerIn(t, h)
	h.fireWake(h.wake)
	select {
	case <-first.fired:
	case <-time.After(5 * time.Second):
		t.Fatal("the first registration must stay bound")
	}
	assert.Empty(t, second.fired)
}
