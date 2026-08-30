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
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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
	wrappedStdin, _, release := ti.Wrap(realStdin, io.Discard)
	t.Cleanup(release)

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-1", Text: "for the terminal"})

	got := readWithin(t, wrappedStdin, 2*time.Second)
	assert.Contains(t, got, "call agent_recv", "the injected text must be a directive the model acts on, not a narrated sentence")
	assert.Contains(t, got, `kind="mail-pending"`)
	// The frame must arrive ALONE. A TUI treats one read as a paste and
	// renders a carriage return inside it as literal text, so a frame shipped
	// with its own CR renders perfectly and is never submitted — it sits in
	// the input box until a human presses Enter, which for an unattended
	// session owner is never.
	assert.NotContains(t, got, "\r", "the submit must not ride in the same read as the frame; a TUI reads that as a paste and the CR becomes literal text")
	assert.NotContains(t, got, "\n", "a line feed is not Enter in raw mode and must not terminate the frame")

	// The frame must be ACTUATED, not merely typed: a SUBSEQUENT read carries
	// the raw-mode Enter (0x0D) as its own input event. Asserting the frame's
	// presence alone passes in exactly the broken state this shipped in.
	submit := readWithin(t, wrappedStdin, 2*time.Second)
	assert.Equal(t, "\r", submit,
		"a later read must deliver the carriage return that SUBMITS the frame; got %q", submit)
}

// readWithin returns the next chunk read from r, failing the test if nothing
// arrives in time. Each injected input event is one Read return, so tests read
// once per event rather than reasoning about buffering.
func readWithin(t *testing.T, r io.Reader, d time.Duration) string {
	t.Helper()
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, 512)
		n, err := r.Read(buf)
		ch <- result{string(buf[:n]), err}
	}()
	select {
	case got := <-ch:
		require.NoError(t, got.err)
		return got.s
	case <-time.After(d):
		t.Fatalf("no bytes reached the wrapped stdin within %s", d)
		return ""
	}
}

// TestNudgeReader_SubmitIsADistinctLaterReadEvent pins the actual mechanism of
// the fix at the level that owns it. The producer cannot solve this by spacing
// two writes: both would still be drained into one Read and coalesce into a
// paste. Only the READER can make them two input events, so the split is
// tested here, on the reader.
func TestNudgeReader_SubmitIsADistinctLaterReadEvent(t *testing.T) {
	realStdin, realStdinW := io.Pipe()
	t.Cleanup(func() { _ = realStdinW.Close() })
	r := newNudgeReader(realStdin)
	r.submitGap = 60 * time.Millisecond

	r.Inject("<frame/>", "\r")

	assert.Equal(t, "<frame/>", readWithin(t, r, time.Second), "the first read carries the frame and nothing else")

	start := time.Now()
	assert.Equal(t, "\r", readWithin(t, r, time.Second), "the submit is delivered by a later read")
	assert.GreaterOrEqual(t, time.Since(start), 40*time.Millisecond,
		"the submit must be SEPARATED IN TIME from the frame by the paste-coalescing gap; delivering it immediately puts it in the same input burst")
}

// TestNudgeReader_EvictionNeverStrandsABareSubmit pins the hazard that makes
// the obvious fix worse than the bug. The queue holds one item and EVICTS to
// make room, so if the submit were injected by a second call it would evict
// the very frame it belongs to: the notice is lost and a blank line is
// submitted in its place. That is silent corruption and would still look
// green, so the pairing is what is under test.
func TestNudgeReader_EvictionNeverStrandsABareSubmit(t *testing.T) {
	realStdin, realStdinW := io.Pipe()
	t.Cleanup(func() { _ = realStdinW.Close() })
	r := newNudgeReader(realStdin)
	r.submitGap = 10 * time.Millisecond

	// Two cycles land before either is read: the second must replace the
	// first as a WHOLE PAIR.
	r.Inject("<frame-1/>", "\r")
	r.Inject("<frame-2/>", "\r")

	first := readWithin(t, r, time.Second)
	assert.NotEqual(t, "\r", first, "a bare carriage return must never be the first thing read: that submits an empty line and loses the notice entirely")
	assert.Contains(t, first, "<frame-2/>", "the newer frame replaces the older one, carrying the more current count")
	assert.Equal(t, "\r", readWithin(t, r, time.Second), "the surviving pair's own submit still follows it")
}

// TestTerminalInject_AckWarnsWhenTheWakeNeverLands gives the delivery path the
// feedback channel it never had. A wake that stages the frame without
// submitting it drains nothing, so the mail count does not move — and before
// this, nothing anywhere could observe that. Every gate stayed green while the
// feature was dark, which is exactly how the bug shipped.
func TestTerminalInject_AckWarnsWhenTheWakeNeverLands(t *testing.T) {
	t.Run("mail never consumed is reported", func(t *testing.T) {
		warnings := captureWarnings(t)
		ti := &TerminalInjector{ackWait: 30 * time.Millisecond, ackTick: 5 * time.Millisecond, count: func() int { return 2 }}

		ti.awaitAck(2)

		assert.Contains(t, warnings.String(), "still unread",
			"an injection whose mail is never consumed must be reported, not succeed silently")
	})

	t.Run("mail consumed is silent", func(t *testing.T) {
		warnings := captureWarnings(t)
		var count atomic.Int32
		count.Store(2)
		ti := &TerminalInjector{ackWait: 2 * time.Second, ackTick: 2 * time.Millisecond, count: func() int { return int(count.Load()) }}
		go func() {
			time.Sleep(20 * time.Millisecond)
			count.Store(0) // the engine took a turn and called agent_recv
		}()

		ti.awaitAck(2)

		assert.Empty(t, warnings.String(), "a wake that landed must not warn: the mail count fell, which is the proof")
	})
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
	// quiet is deliberately an order of magnitude above the writer's own
	// cadence below: an occasional scheduler hiccup in the writer goroutine
	// must not accidentally look "quiet" and pass this test for the wrong
	// reason. maxWait is deliberately far BELOW quiet, so the only way an
	// injection can land inside this test's window is the bound firing.
	ti := &TerminalInjector{quiet: 300 * time.Millisecond, tick: time.Millisecond, maxWait: 40 * time.Millisecond, count: func() int { return 3 }}
	var mu sync.Mutex
	var got string
	ti.inject = func(frame, submit string) { mu.Lock(); got = frame + submit; mu.Unlock() }

	// Seed lastWrite BEFORE arming the writer/nudge: the zero value would
	// otherwise read as "56 years idle" on run's very first check, satisfying
	// "quiet" by accident and proving nothing about the bound this test
	// exists to pin.
	ti.lastWrite.Store(time.Now().UnixNano())

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				ti.lastWrite.Store(time.Now().UnixNano())
				time.Sleep(time.Millisecond)
			}
		}
	}()

	start := time.Now()
	ti.nudge()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return got != ""
	}, time.Second, 2*time.Millisecond, "the bound must force an injection even when the terminal never looks quiet")
	assert.Less(t, time.Since(start), 200*time.Millisecond, "the wait must not run past its own bound")
}

// TestTerminalInject_BurstOfMailCoalescesToOneInjection pins decision #4:
// several arrivals in a tight burst must produce exactly one injection, not
// one per message — mirroring mailbox.go's settleBurst for the analogous
// "many arrivals, one wake" problem on the recv side.
func TestTerminalInject_BurstOfMailCoalescesToOneInjection(t *testing.T) {
	h := newNoticeHome(t)
	var calls atomic.Int32
	ti := &TerminalInjector{quiet: 20 * time.Millisecond, tick: 2 * time.Millisecond, maxWait: 500 * time.Millisecond, count: h.BufferedMailCount}
	ti.inject = func(string, string) { calls.Add(1) }
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

// TestTerminalInject_ReleaseStopsInjectingIntoTheEndedTurn pins the release
// half of Wrap's contract. The injector has HOME lifetime and the stream it
// writes into has TURN lifetime; with no release the two never reconcile.
//
// The failure is silent by construction and that is why it needs a test:
// nudgeReader.Inject never blocks, so injecting into an abandoned reader
// SUCCEEDS. The buffered count never falls, and 45s later awaitAck warns that
// the frame "may be staged in the input box unsubmitted" — naming a cause
// that is not the real one, because there is no live turn at all. After the
// release, run() must take its inject == nil branch and leave the mail
// buffered for a real Recv instead.
func TestTerminalInject_ReleaseStopsInjectingIntoTheEndedTurn(t *testing.T) {
	h := newNoticeHome(t)
	ti := &TerminalInjector{quiet: 5 * time.Millisecond, tick: time.Millisecond, maxWait: 200 * time.Millisecond, count: h.BufferedMailCount}
	h.SetTerminalNudge(ti.nudge)

	turn, turnW := io.Pipe()
	t.Cleanup(func() { _ = turnW.Close() })
	wrapped, _, release := ti.Wrap(turn, io.Discard)

	// The turn ends.
	release()

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-after-release", Text: "arrives with no live turn"})

	// Read past the injector's whole wait bound. Nothing may appear on the
	// dead turn's stdin.
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 256)
		n, err := wrapped.Read(buf)
		if err == nil {
			got <- string(buf[:n])
		}
	}()
	select {
	case frame := <-got:
		t.Fatalf("a released turn's stdin must receive nothing, got %q", frame)
	case <-time.After(500 * time.Millisecond):
	}
	assert.Equal(t, 1, h.BufferedMailCount(),
		"the message must stay buffered for a real Recv rather than being spent on a stream nobody reads")
}

// TestTerminalInject_RewrapRetargetsInjectionToTheCurrentTurn pins the
// one-injector-per-Home contract that llm_serve.go depends on. A SECOND
// interactive turn must be the one that receives the nudge.
//
// The failure this guards is silent and slow rather than loud: build an
// injector per turn instead of re-Wrapping one, and SetTerminalNudge refuses
// the later registration, so mail is injected into the previous turn's reader
// — which nothing drains. The session owner is never told, and the mail
// surfaces only if some unrelated activity moves the pipeline.
func TestTerminalInject_RewrapRetargetsInjectionToTheCurrentTurn(t *testing.T) {
	h := newNoticeHome(t)
	ti := &TerminalInjector{quiet: 5 * time.Millisecond, tick: time.Millisecond, maxWait: 500 * time.Millisecond, count: h.BufferedMailCount}
	h.SetTerminalNudge(ti.nudge)

	// Turn 1 wraps and then ends; nothing reads its stdin afterwards.
	turn1, turn1W := io.Pipe()
	t.Cleanup(func() { _ = turn1W.Close() })
	_, _, release1 := ti.Wrap(turn1, io.Discard)

	// Turn 2 wraps the same injector: it now owns the injection target.
	turn2, turn2W := io.Pipe()
	t.Cleanup(func() { _ = turn2W.Close() })
	wrapped2, _, _ := ti.Wrap(turn2, io.Discard)

	// Turn 1's release lands LATE — after turn 2 already took the target.
	// This ordering is reachable in production (RunTurn defers the release,
	// and nothing sequences one turn's teardown before the next turn's wrap),
	// so the release must be compare-and-clear: an unconditional nil here
	// disarms the LIVE turn and the assertions below go dark.
	release1()

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-rewrap", Text: "for the current turn"})

	frame := readWithin(t, wrapped2, 2*time.Second)
	assert.Contains(t, frame, `kind="mail-pending"`, "the CURRENT turn's stdin must receive the frame")
	assert.Equal(t, "\r", readWithin(t, wrapped2, 2*time.Second),
		"and it must still be actuated: the submit follows on the current turn's stdin too")
}

// TestSetTerminalNudge_SecondRegistrationIsAFinding pins BOTH arms of the
// refusal. A second registration means two injectors think they own the one
// terminal, and the loser is the one silently disabled: the registered nudge
// stays bound to a stdin its turn has abandoned, so the session owner is never
// told mail arrived. That is the "succeeds without doing the thing" shape, so
// it is reported rather than merely warned about.
//
// The refusal itself is unconditional in both modes — degraded lowers the
// FINDING to a warning, it does not let a second injector win the terminal.
func TestSetTerminalNudge_SecondRegistrationIsAFinding(t *testing.T) {
	t.Run("strict records a finding", func(t *testing.T) {
		resetStrictness(t)
		h := newNoticeHome(t)
		mark := strictness.Checkpoint()

		first := func() {}
		h.SetTerminalNudge(first)
		require.NoError(t, strictness.FindingsError(mark), "the FIRST registration is the normal case and must not fault")

		h.SetTerminalNudge(func() {})
		require.Error(t, strictness.FindingsError(mark),
			"a second terminal-nudge registration silently disables the owner's mail wake and must be reported")
	})

	t.Run("degraded still refuses the second registration", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		h := newNoticeHome(t)
		mark := strictness.Checkpoint()

		var firstFired atomic.Bool
		h.SetTerminalNudge(func() { firstFired.Store(true) })
		h.SetTerminalNudge(func() { t.Error("the second nudge must never be installed, in either mode") })

		assert.NoError(t, strictness.FindingsError(mark), "degraded lowers this finding to a warning and continues")

		h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-degraded", Text: "x"})
		assert.True(t, firstFired.Load(), "the FIRST registration keeps the terminal in both modes")
	})
}
