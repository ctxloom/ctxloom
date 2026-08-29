package coord

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// TestTerminalInject_NoSinkInjectsMailPendingFrame is the primary case: a
// session-owner Home (no turn sink) receives mail with nothing to hand it to,
// and the EFFECT under test is bytes actually landing on a Read of the
// wrapped stdin — not that some function was merely called.
func TestTerminalInject_NoSinkInjectsMailPendingFrame(t *testing.T) {
	h := newNoticeHome(t)
	ti := &TerminalInjector{quiet: 5 * time.Millisecond, tick: time.Millisecond, maxWait: 500 * time.Millisecond, count: h.BufferedMailCount}
	h.SetTerminalNudge(ti.nudge)

	// A real reader that never produces anything on its own, standing in for
	// a terminal the human is not typing into.
	realStdin, realStdinW := io.Pipe()
	t.Cleanup(func() { _ = realStdinW.Close() })
	wrappedStdin, _ := ti.Wrap(realStdin, io.Discard)

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-1", Text: "for the terminal"})

	buf := make([]byte, 256)
	readDone := make(chan struct{})
	var n int
	var rerr error
	go func() {
		n, rerr = wrappedStdin.Read(buf)
		close(readDone)
	}()

	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("no bytes reached the wrapped stdin: the terminal was never nudged")
	}
	require.NoError(t, rerr)
	got := string(buf[:n])
	assert.Contains(t, got, "call agent_recv", "the injected text must be a directive the model acts on, not a narrated sentence")
	assert.Contains(t, got, `kind="mail-pending"`)
}

// TestTerminalInject_SinkPresentNeverNudges is the CONTROL: a target that DOES
// have a turn sink must take the structural enqueueTurn path, and the
// terminal nudge must never fire for it — the double-delivery guard.
func TestTerminalInject_SinkPresentNeverNudges(t *testing.T) {
	h := newNoticeHome(t)
	h.turnQ = make(chan *agentcoordpb.PeerMessage, 1)
	var nudged atomic.Bool
	h.SetTerminalNudge(func() { nudged.Store(true) })

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-2", Text: "structural"})

	select {
	case pm := <-h.turnQ:
		assert.Equal(t, "m-2", pm.GetMessageId())
	case <-time.After(time.Second):
		t.Fatal("expected the registered turn sink to take the delivery")
	}
	assert.False(t, nudged.Load(), "a target with a turn sink must never be nudged via the terminal path")
}

// TestTerminalInject_NeverQuietStillInjectsWithinBound pins decision #3: an
// engine whose output never goes quiet must still be nudged, bounded, rather
// than wait forever.
func TestTerminalInject_NeverQuietStillInjectsWithinBound(t *testing.T) {
	ti := &TerminalInjector{quiet: 50 * time.Millisecond, tick: 5 * time.Millisecond, maxWait: 100 * time.Millisecond, count: func() int { return 3 }}
	var mu sync.Mutex
	var got string
	ti.inject = func(s string) { mu.Lock(); got = s; mu.Unlock() }

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				ti.lastWrite.Store(time.Now().UnixNano())
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	start := time.Now()
	ti.nudge()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return got != ""
	}, time.Second, 5*time.Millisecond, "the bound must force an injection even when the terminal never looks quiet")
	assert.Less(t, time.Since(start), 500*time.Millisecond, "the wait must not run past its own bound")
}

// TestTerminalInject_BurstOfMailCoalescesToOneInjection pins decision #4:
// several arrivals in a tight burst must produce exactly one injection, not
// one per message — mirroring mailbox.go's settleBurst for the analogous
// "many arrivals, one wake" problem on the recv side.
func TestTerminalInject_BurstOfMailCoalescesToOneInjection(t *testing.T) {
	h := newNoticeHome(t)
	var calls atomic.Int32
	ti := &TerminalInjector{quiet: 20 * time.Millisecond, tick: 2 * time.Millisecond, maxWait: 500 * time.Millisecond, count: h.BufferedMailCount}
	ti.inject = func(string) { calls.Add(1) }
	h.SetTerminalNudge(ti.nudge)

	for i := 0; i < 5; i++ {
		h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: fmt.Sprintf("m-burst-%d", i)})
	}

	require.Eventually(t, func() bool { return calls.Load() >= 1 }, time.Second, 5*time.Millisecond,
		"expected at least one injection for the burst")
	// Give a second cycle every chance to fire wrongly before asserting it didn't.
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, int32(1), calls.Load(), "five arrivals in one burst must coalesce into exactly one injection")
	assert.Equal(t, 5, h.BufferedMailCount(), "coalescing the WAKE must not drop any of the buffered messages")
}
