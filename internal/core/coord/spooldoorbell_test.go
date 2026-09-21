package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// The spool doorbell: a wire frame that carries a spool.Ref and nothing else.
// These tests pin the four properties the design makes load-bearing — the ref
// survives the round trip byte-identical, the two closed vocabularies map onto
// each other exhaustively, an invalid ref dies at the receive chokepoint before
// anything could resolve it to a path, and a send that cannot go out right now
// drops it COUNTABLY rather than blocking or bookkeeping.

const doorbellHarp = "ugly-icy-squid"

// doorbellName is a realistic spool filename: unixnano.seq.writer.md.
const doorbellName = "00001754919000123456789.00000042.coord.md"

// waitRef takes the ref a handler received, failing the test rather than
// hanging forever if the doorbell never arrived.
func waitRef(t *testing.T, got <-chan spool.Ref) spool.Ref {
	t.Helper()
	select {
	case ref := <-got:
		return ref
	case <-time.After(10 * time.Second):
		t.Fatal("the doorbell never reached the receiving side's handler")
		return spool.Ref{}
	}
}

// TestSpoolDoorbell_RunnerToCoordinatorRoundTrip drives a REAL runner Home over
// a REAL RunChannel: the top-level AgentFrame arm exists to carry exactly this,
// and only an end-to-end dial proves the frame survives encode, transport,
// dispatch and validation with every field intact.
func TestSpoolDoorbell_RunnerToCoordinatorRoundTrip(t *testing.T) {
	for _, dir := range spool.Dirs() {
		t.Run(dir.String(), func(t *testing.T) {
			c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
			h := dialHome(t, c, doorbellHarp)

			got := make(chan spool.Ref, 1)
			var gotRole string
			c.SetSpoolDoorbellHandler(func(role string, ref spool.Ref) {
				gotRole = role
				got <- ref
			})

			want := spool.Ref{Harp: doorbellHarp, Dir: dir, Name: doorbellName}
			require.NoError(t, h.ringSpool(want))

			assert.Equal(t, want, waitRef(t, got),
				"the doorbell must arrive as the IDENTICAL ref: every field is a coordinate the receiver joins into a path, so a lost or altered one resolves somewhere else")
			assert.Equal(t, doorbellHarp, gotRole, "the handler is told which channel rang")
			assert.Zero(t, c.SpoolDoorbellStats().Rejected, "a well-formed doorbell is not a rejection")
		})
	}
}

// TestSpoolDoorbell_CoordinatorToRunnerRoundTrip is the mirror: the
// CoordinatorNotice arm. A runner is rung for what it READS — its own in/,
// and in/withdrawn as the retraction notice — and refuses a doorbell for any
// other directory (those are its own writes coming back at it), COUNTED as a
// rejection rather than followed.
func TestSpoolDoorbell_CoordinatorToRunnerRoundTrip(t *testing.T) {
	for _, dir := range spool.Dirs() {
		t.Run(dir.String(), func(t *testing.T) {
			c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
			h := dialHome(t, c, doorbellHarp)

			got := make(chan spool.Ref, 1)
			h.SetSpoolDoorbellHandler(func(_ string, ref spool.Ref) { got <- ref })

			want := spool.Ref{Harp: doorbellHarp, Dir: dir, Name: doorbellName}
			require.NoError(t, c.ringSpool(doorbellHarp, want))

			switch dir {
			case spool.DirIn, spool.DirInWithdrawn:
				assert.Equal(t, want, waitRef(t, got),
					"the doorbell must arrive as the IDENTICAL ref in this direction too")
				assert.Zero(t, h.SpoolDoorbellStats().Rejected)
			default:
				require.Eventually(t, func() bool { return h.SpoolDoorbellStats().Rejected == 1 }, 10*time.Second, 10*time.Millisecond,
					"a doorbell for a directory this runner does not read is refused, and counted")
				select {
				case ref := <-got:
					t.Fatalf("a refused doorbell must never reach the handler, got %v", ref)
				default:
				}
			}
			assert.Zero(t, h.SpoolDoorbellStats().Dropped, "a live channel drops nothing")
		})
	}
}

// TestSpoolDoorbell_EnumExhaustiveBothDirections is the drift alarm. The two
// closed vocabularies — spool.Dir and the wire enum — are declared in different
// languages in different files, and the ONLY thing keeping them in step is the
// table in spooldoorbell.go. Adding a sixth spool directory, or a sixth enum
// value, must fail HERE rather than silently not map: an unmapped directory
// means a doorbell that cannot be rung (mail that only ever arrives by sweep,
// i.e. slower with no error anywhere), and an unmapped enum value means an
// inbound doorbell refused as invalid.
func TestSpoolDoorbell_EnumExhaustiveBothDirections(t *testing.T) {
	dirs := spool.Dirs()

	// Every wire enum value except UNSPECIFIED must have a spool.Dir, and the
	// counts must match: this is what catches a value added on ONE side only.
	wireValues := make([]agentcoordpb.SpoolDir, 0, len(agentcoordpb.SpoolDir_name))
	for num := range agentcoordpb.SpoolDir_name {
		if v := agentcoordpb.SpoolDir(num); v != agentcoordpb.SpoolDir_SPOOL_DIR_UNSPECIFIED {
			wireValues = append(wireValues, v)
		}
	}
	assert.Len(t, wireValues, len(dirs),
		"the wire enum and spool.Dir must have the same number of live values; a value added to one side alone has no mapping")

	t.Run("spool.Dir -> wire -> spool.Dir", func(t *testing.T) {
		seen := map[agentcoordpb.SpoolDir]spool.Dir{}
		for _, d := range dirs {
			w, err := SpoolDirToWire(d)
			require.NoError(t, err, "spool directory %q has no wire representation", d)
			assert.NotEqual(t, agentcoordpb.SpoolDir_SPOOL_DIR_UNSPECIFIED, w,
				"%q must not encode as the unspecified value, which is invalid at every consumer", d)
			if prev, dup := seen[w]; dup {
				t.Fatalf("%q and %q both encode as %s: two spool directories collapsed onto one wire value, so a consume-rename could be read out of the wrong directory", prev, d, w)
			}
			seen[w] = d

			back, err := SpoolDirFromWire(w)
			require.NoError(t, err)
			assert.Equal(t, d, back, "the round trip must return the ORIGINAL directory")
		}
	})

	t.Run("wire -> spool.Dir -> wire", func(t *testing.T) {
		for _, w := range wireValues {
			d, err := SpoolDirFromWire(w)
			require.NoError(t, err, "wire value %s has no spool.Dir", w)
			assert.NoError(t, d.Validate(), "%s decoded to %q, which is not in spool's closed set", w, d)

			back, err := SpoolDirToWire(d)
			require.NoError(t, err)
			assert.Equal(t, w, back, "the round trip must return the ORIGINAL wire value")
		}
	})

	t.Run("unspecified and unknown are refused, never defaulted", func(t *testing.T) {
		_, err := SpoolDirFromWire(agentcoordpb.SpoolDir_SPOOL_DIR_UNSPECIFIED)
		assert.Error(t, err, "the zero value must not be a way to arrive unclassified")
		_, err = SpoolDirFromWire(agentcoordpb.SpoolDir(9999))
		assert.Error(t, err, "a value from a future build must fail loudly, not guess a directory")
		_, err = SpoolDirToWire(spool.Dir("in/somewhere-new"))
		assert.Error(t, err, "an unmappable directory must not encode as UNSPECIFIED and become the receiver's problem")
	})
}

// TestSpoolDoorbell_InvalidRefRejectedAtTheChokepoint is the security pin.
// Every field of a doorbell arrives from a less-trusted peer, and each of these
// frames is one a hostile or broken runner can put on the wire. None may reach
// the consumer, because the consumer's whole job is to resolve the ref into a
// filesystem path.
//
// Sending goes through h.send, NOT h.RingSpool: RingSpool validates at the
// writer, which is right but would mean this test never exercised the receive
// chokepoint at all.
func TestSpoolDoorbell_InvalidRefRejectedAtTheChokepoint(t *testing.T) {
	cases := []struct {
		name string
		msg  *agentcoordpb.SpoolChanged
		want string
	}{
		{
			name: "traversal in the file name",
			msg:  &agentcoordpb.SpoolChanged{Harp: doorbellHarp, Dir: agentcoordpb.SpoolDir_SPOOL_DIR_IN, Name: "../../../etc/passwd"},
			want: "path separator",
		},
		{
			name: "bare dot-dot as the file name",
			msg:  &agentcoordpb.SpoolChanged{Harp: doorbellHarp, Dir: agentcoordpb.SpoolDir_SPOOL_DIR_IN, Name: ".."},
			want: "names a directory",
		},
		{
			name: "unspecified directory",
			msg:  &agentcoordpb.SpoolChanged{Harp: doorbellHarp, Dir: agentcoordpb.SpoolDir_SPOOL_DIR_UNSPECIFIED, Name: doorbellName},
			want: "unknown wire spool directory",
		},
		{
			name: "directory value from a future build",
			msg:  &agentcoordpb.SpoolChanged{Harp: doorbellHarp, Dir: agentcoordpb.SpoolDir(77), Name: doorbellName},
			want: "unknown wire spool directory",
		},
		{
			name: "harp carrying a path separator",
			msg:  &agentcoordpb.SpoolChanged{Harp: "../sibling", Dir: agentcoordpb.SpoolDir_SPOOL_DIR_OUT, Name: doorbellName},
			want: "invalid ref harp",
		},
		{
			name: "empty harp",
			msg:  &agentcoordpb.SpoolChanged{Harp: "", Dir: agentcoordpb.SpoolDir_SPOOL_DIR_OUT, Name: doorbellName},
			want: "invalid ref harp",
		},
		{
			name: "empty name",
			msg:  &agentcoordpb.SpoolChanged{Harp: doorbellHarp, Dir: agentcoordpb.SpoolDir_SPOOL_DIR_OUT, Name: ""},
			want: "file name is required",
		},
		{
			name: "no reference at all",
			msg:  nil,
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
			h := dialHome(t, c, doorbellHarp)

			fired := make(chan spool.Ref, 1)
			c.SetSpoolDoorbellHandler(func(_ string, ref spool.Ref) { fired <- ref })

			// A LOCKED sink: the refusal is written on the server's own
			// receive goroutine while this one polls the counter.
			var buf syncBuf
			restore := clidiag.SetSink(&buf)
			defer restore()

			// A nil payload cannot travel inside a set oneof arm, so that case
			// exercises the chokepoint directly; the rest go over the wire.
			if tc.msg == nil {
				c.mu.Lock()
				ch := c.chans[doorbellHarp]
				c.mu.Unlock()
				require.NotNil(t, ch)
				handleAgentFrame(c, ch, &agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_SpoolChanged{SpoolChanged: nil}})
			} else {
				h.send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_SpoolChanged{SpoolChanged: tc.msg}})
			}

			require.Eventually(t, func() bool {
				return c.SpoolDoorbellStats().Rejected == 1
			}, 10*time.Second, 10*time.Millisecond,
				"the refusal must be COUNTED: an invisible rejection makes a broken sender look like a quiet one forever")

			select {
			case ref := <-fired:
				t.Fatalf("the consumer was handed %s — an unvalidated ref reached the code that resolves it to a path", ref)
			default:
			}

			assert.Contains(t, buf.String(), "refusing an invalid spool doorbell",
				"the refusal must also be reported, naming the sender")
			if tc.want != "" {
				assert.Contains(t, buf.String(), tc.want,
					"the report must name WHICH field was wrong, or an operator cannot tell a broken mapper from a probe")
			}
		})
	}
}

// TestSpoolDoorbell_ForgedHarpIsRefused is the isolation fence. A child can
// put any harp it likes in a frame; if the coordinator believed it, the child
// could aim the coordinator's reader — and its consume-renames — at a SIBLING's
// spool. The frame is REFUSED, not re-aimed: the spool contract is "rejected,
// never sanitised", and a rewritten field would leave a probing peer
// indistinguishable in the counters from a quiet one. Nothing is lost by
// refusing — the sweep is the delivery floor, and a doorbell only bounds
// latency (see TestSpoolDoorbell_RefusedForgedHarpStillDeliveredByTheSweep).
func TestSpoolDoorbell_ForgedHarpIsRefused(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	h := dialHome(t, c, doorbellHarp)

	got := make(chan spool.Ref, 1)
	c.SetSpoolDoorbellHandler(func(_ string, ref spool.Ref) { got <- ref })

	var buf syncBuf
	restore := clidiag.SetSink(&buf)
	defer restore()

	// Syntactically perfect, and about somebody else's spool.
	h.send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_SpoolChanged{
		SpoolChanged: &agentcoordpb.SpoolChanged{
			Harp: "innocent-sibling-session",
			Dir:  agentcoordpb.SpoolDir_SPOOL_DIR_OUT,
			Name: doorbellName,
		},
	}})

	require.Eventually(t, func() bool {
		return c.SpoolDoorbellStats().Rejected == 1
	}, 10*time.Second, 10*time.Millisecond,
		"a runner naming someone else's harp is broken or probing; the refusal must be COUNTED, or a probe reads as ordinary contention")

	select {
	case ref := <-got:
		t.Fatalf("the consumer was handed %s — a ref naming a foreign harp must be refused, not re-aimed at the channel's own spool", ref)
	default:
	}
	assert.Contains(t, buf.String(), "innocent-sibling-session",
		"the refusal must name the harp the frame claimed, or an operator cannot tell a probe from a broken mapper")
	assert.Contains(t, buf.String(), "refusing",
		"the report must read as a refusal, not a correction")
}

// TestSpoolDoorbell_RefusedForgedHarpStillDeliveredByTheSweep pins what makes
// refusal SAFE: the doorbell is a latency hint and the file is the truth. A
// child whose out/ holds a real message but whose doorbell claimed a sibling's
// harp gets that doorbell refused — and the message still reaches the owner,
// because the reconciliation tick sweeps every known role regardless.
//
// The doorbell consumer is replaced with a capture so the ONLY way the file
// can arrive is the tick: a delivery here proves the floor, not the bell. The
// child still rings honestly for its own turn report, so the capture is
// checked for THIS file's ref rather than for silence.
func TestSpoolDoorbell_RefusedForgedHarpStillDeliveredByTheSweep(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	const cadence = 150 * time.Millisecond
	sp := cutoverSpawner(cadence)
	c := newCutoverCoordinator(t, sp, cadence)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	got := make(chan spool.Ref, 16)
	c.SetSpoolDoorbellHandler(func(_ string, ref spool.Ref) { got <- ref })
	handedOver := func(name string) bool {
		for {
			select {
			case r := <-got:
				if r.Name == name {
					return true
				}
			default:
				return false
			}
		}
	}

	// The message, written straight into the child's out/ so that no honest
	// doorbell is rung for it by anyone.
	w, err := spool.NewWriter(spool.NewHomeMapper(), out.Harp, spool.DirOut, out.Harp)
	require.NoError(t, err)
	ref, err := w.Write(&spool.Message{
		Kind: KindResult, FromHarp: out.Harp, To: ParentAddress,
		Body: "announced under a false name",
	})
	require.NoError(t, err)

	// The forged announcement: the right file, a sibling's harp.
	c.mu.Lock()
	ch := c.chans[out.Harp]
	c.mu.Unlock()
	require.NotNil(t, ch, "the child's run channel must be attached for the doorbell to have a role")
	before := c.SpoolDoorbellStats().Rejected
	c.HandleSpoolChanged(ch, spool.Ref{
		Harp: "innocent-sibling-session",
		Dir:  spool.DirOut,
		Name: ref.Name,
	})
	assert.Equal(t, before+1, c.SpoolDoorbellStats().Rejected, "the forged doorbell must be counted as refused")
	assert.False(t, handedOver(ref.Name), "the consumer must not be handed a refused doorbell's ref, re-aimed or otherwise")

	msgs := recvBody(t, c, "announced under a false name", conformanceWait)
	require.NotEmpty(t, msgs, "refusing the doorbell must cost latency only: the sweep is the at-least-once floor")
	assert.Equal(t, out.Harp, msgs[0].From, "the sender is the spool the file was found in")
	awaitSpoolCount(t, out.Harp, spool.DirOut, 0, "after the sweep routed it")
	assert.False(t, handedOver(ref.Name), "the delivery must have come from the tick, never from the refused bell")
}

// TestSpoolDoorbell_DropsWhenItCannotBeSent pins the fire-and-forget ruling on
// every path that cannot deliver right now. The assertion that matters is
// double: RingSpool RETURNS (no error, no block — a blocking doorbell would
// make the file-write path hostage to a busy child) and the drop is COUNTED.
func TestSpoolDoorbell_DropsWhenItCannotBeSent(t *testing.T) {
	ref := spool.Ref{Harp: doorbellHarp, Dir: spool.DirIn, Name: doorbellName}

	t.Run("coordinator: no live run channel", func(t *testing.T) {
		c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

		require.NoError(t, c.ringSpool(doorbellHarp, ref),
			"a runner that is not attached is an ordinary state, not a caller error")
		assert.Equal(t, uint64(1), c.SpoolDoorbellStats().Dropped)
	})

	t.Run("coordinator: saturated send pump", func(t *testing.T) {
		c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

		// A registered channel whose outbound queue is FULL and whose pump is
		// not draining — a child too busy to read, which is precisely the
		// state in which a blocking send would deadlock the writer. Built
		// directly rather than by wedging a live dial's pump: the pump reads
		// ch.send off-lock by design, so swapping it under a live channel
		// races the very goroutine the test is trying to stall.
		ch := &RunChannel{
			role:        doorbellHarp,
			BidiSession: NewBidiSession[OutFrame, OutFrame, OutFrame](func() {}, 1),
			completed:   make(chan struct{}),
		}
		ch.send <- OutFrame{}
		c.mu.Lock()
		c.chans[doorbellHarp] = ch
		c.mu.Unlock()

		done := make(chan error, 1)
		go func() { done <- c.ringSpool(doorbellHarp, ref) }()
		select {
		case err := <-done:
			assert.NoError(t, err, "a dropped doorbell is not an error: the file is the truth and the sweep redelivers")
		case <-time.After(5 * time.Second):
			t.Fatal("RingSpool BLOCKED on a saturated pump — fire-and-forget means it returns, always")
		}
		assert.Equal(t, uint64(1), c.SpoolDoorbellStats().Dropped,
			"the drop must be counted, or a permanently saturated child is indistinguishable from a quiet one")
	})

	t.Run("runner: no stream", func(t *testing.T) {
		c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
		h := dialHome(t, c, doorbellHarp)
		h.Close(0, "")

		done := make(chan error, 1)
		go func() { done <- h.ringSpool(ref) }()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("Home.RingSpool BLOCKED with no stream")
		}
		assert.Equal(t, uint64(1), h.SpoolDoorbellStats().Dropped)
	})
}

// TestSpoolDoorbell_InvalidRefNeverReachesTheWire keeps the writer honest: a
// sender that rings about a name it could not itself resolve is a bug worth
// failing where the stack still names who did it, rather than travelling and
// being refused at a peer.
func TestSpoolDoorbell_InvalidRefNeverReachesTheWire(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	h := dialHome(t, c, doorbellHarp)

	bad := []spool.Ref{
		{Harp: doorbellHarp, Dir: spool.DirIn, Name: "../escape.md"},
		{Harp: "with/separator", Dir: spool.DirIn, Name: doorbellName},
		{Harp: doorbellHarp, Dir: spool.Dir("in/nowhere"), Name: doorbellName},
		{Harp: doorbellHarp, Dir: "", Name: doorbellName},
	}
	for _, ref := range bad {
		assert.Error(t, c.ringSpool(doorbellHarp, ref), "coordinator must refuse to ring about %s", ref)
		assert.Error(t, h.ringSpool(ref), "runner must refuse to ring about %s", ref)
	}
	assert.Zero(t, c.SpoolDoorbellStats().Dropped,
		"a refused ref was never a doorbell, so it is not a DROP — conflating the two would hide real drops in the count")
	assert.Zero(t, h.SpoolDoorbellStats().Dropped)
}

// TestSpoolDoorbell_CarriesNothingButTheReference pins the design's central
// restraint. The doorbell's whole value is that it is not a second carrier: the
// file's frontmatter is the control plane. A body, a kind, or a correlation id
// added here would recreate exactly the two-carrier desync the spool exists to
// kill, so the frame's field set is a contract, not an implementation detail.
func TestSpoolDoorbell_CarriesNothingButTheReference(t *testing.T) {
	fields := (&agentcoordpb.SpoolChanged{}).ProtoReflect().Descriptor().Fields()
	got := make([]string, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		got = append(got, string(fields.Get(i).Name()))
	}
	assert.ElementsMatch(t, []string{"harp", "dir", "name"}, got,
		"SpoolChanged must carry the logical coordinate and NOTHING else: any payload field makes the wire a second source of truth")
}

// dialHome stands up a REAL runner Home against c, attached as harp,
// advertising exactly caps. The end-to-end dial is the point: only a real
// Hello attaches a real run channel on both sides.
func dialHome(t *testing.T, c *Coordinator, harp string, caps ...string) *Home {
	t.Helper()
	url, err := c.ReachURL("host")
	require.NoError(t, err)
	token, err := c.RegisterSessionOwner(harp)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, err := NewHome(ctx, HomeConfig{
		Reporter: termSink(),
		URL:      url, Token: token, Harness: "test", Version: "test",
		Capabilities: caps,
		Harp:         harp,
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.chans[harp] != nil
	}, 10*time.Second, 10*time.Millisecond, "the run channel must attach before a control request can be sent")
	// BOTH sides, because the handshake attaches them at different moments:
	// the coordinator registers c.chans[harp] when it READS the Hello, while
	// the runner sets h.stream only after it has read the HelloAck back. A
	// fixture that waits on the coordinator's half alone hands back a Home
	// whose stream is still nil, and every unbuffered runner->coordinator
	// send in that window is dropped ON PURPOSE (Home.trySend's nil-stream
	// arm) — silently, because fire-and-forget is the design. That is a
	// fixture defect, not a product one: it turns "the doorbell arrived" into
	// a race against the scheduler, lost whenever the box is busy.
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.stream != nil
	}, 10*time.Second, 10*time.Millisecond, "the runner's own end of the run channel must be attached, or a send made now is dropped as 'run channel down'")
	return h
}
