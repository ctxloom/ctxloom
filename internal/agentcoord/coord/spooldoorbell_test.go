package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
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
			h := dialHome(t, c, doorbellHarp, CapPeerMessaging)

			got := make(chan spool.Ref, 1)
			var gotRole string
			c.SetSpoolDoorbellHandler(func(role string, ref spool.Ref) {
				gotRole = role
				got <- ref
			})

			want := spool.Ref{Harp: doorbellHarp, Dir: dir, Name: doorbellName}
			require.NoError(t, h.RingSpool(want))

			assert.Equal(t, want, waitRef(t, got),
				"the doorbell must arrive as the IDENTICAL ref: every field is a coordinate the receiver joins into a path, so a lost or altered one resolves somewhere else")
			assert.Equal(t, doorbellHarp, gotRole, "the handler is told which channel rang")
			assert.Zero(t, c.SpoolDoorbellStats().Rejected, "a well-formed doorbell is not a rejection")
		})
	}
}

// TestSpoolDoorbell_CoordinatorToRunnerRoundTrip is the mirror: the
// CoordinatorNotice arm, which will replace the PeerMessage payload push.
func TestSpoolDoorbell_CoordinatorToRunnerRoundTrip(t *testing.T) {
	for _, dir := range spool.Dirs() {
		t.Run(dir.String(), func(t *testing.T) {
			c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
			h := dialHome(t, c, doorbellHarp, CapPeerMessaging)

			got := make(chan spool.Ref, 1)
			h.SetSpoolDoorbellHandler(func(_ string, ref spool.Ref) { got <- ref })

			want := spool.Ref{Harp: doorbellHarp, Dir: dir, Name: doorbellName}
			require.NoError(t, c.RingSpool(doorbellHarp, want))

			assert.Equal(t, want, waitRef(t, got),
				"the doorbell must arrive as the IDENTICAL ref in this direction too")
			assert.Zero(t, h.SpoolDoorbellStats().Rejected)
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
			h := dialHome(t, c, doorbellHarp, CapPeerMessaging)

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
				c.handleSpoolChanged(ch, nil)
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

// TestSpoolDoorbell_ForgedHarpResolvesAgainstTheChannel is the isolation fence.
// A child can put any harp it likes in a frame; if the coordinator believed it,
// the child could aim the coordinator's reader — and its consume-renames — at a
// SIBLING's spool. Identity comes from the channel, exactly as inbound mail's
// sender identity comes from the connection credential and never from `from`.
func TestSpoolDoorbell_ForgedHarpResolvesAgainstTheChannel(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	h := dialHome(t, c, doorbellHarp, CapPeerMessaging)

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

	ref := waitRef(t, got)
	assert.Equal(t, doorbellHarp, ref.Harp,
		"the claimed harp must be replaced by the channel's own role, or a child can point the coordinator at a sibling's spool")
	assert.Equal(t, spool.DirOut, ref.Dir, "only the harp is re-aimed; the rest of the coordinate stands")
	assert.Contains(t, buf.String(), "innocent-sibling-session",
		"a runner naming someone else's harp is broken or probing, and both deserve a trace")
}

// TestSpoolDoorbell_DropsWhenItCannotBeSent pins the fire-and-forget ruling on
// every path that cannot deliver right now. The assertion that matters is
// double: RingSpool RETURNS (no error, no block — a blocking doorbell would
// make the file-write path hostage to a busy child) and the drop is COUNTED.
func TestSpoolDoorbell_DropsWhenItCannotBeSent(t *testing.T) {
	ref := spool.Ref{Harp: doorbellHarp, Dir: spool.DirIn, Name: doorbellName}

	t.Run("coordinator: no live run channel", func(t *testing.T) {
		c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

		require.NoError(t, c.RingSpool(doorbellHarp, ref),
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
		ch := &runChan{
			role:      doorbellHarp,
			send:      make(chan *agentcoordpb.CoordinatorFrame, 1),
			completed: make(chan struct{}),
		}
		ch.send <- &agentcoordpb.CoordinatorFrame{}
		c.mu.Lock()
		c.chans[doorbellHarp] = ch
		c.mu.Unlock()

		done := make(chan error, 1)
		go func() { done <- c.RingSpool(doorbellHarp, ref) }()
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
		h := dialHome(t, c, doorbellHarp, CapPeerMessaging)
		h.Close(0, "")

		done := make(chan error, 1)
		go func() { done <- h.RingSpool(ref) }()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("Home.RingSpool BLOCKED with no stream")
		}
		assert.Equal(t, uint64(1), h.SpoolDoorbellStats().Dropped)
	})
}

// TestSpoolDoorbell_NoConsumerIsTheDefault pins this slice's inertness: the
// wire is capable and nothing acts on it. A doorbell arriving with no handler
// registered is counted and dropped — not queued for a future consumer, which
// would be state, and state is what the file already is.
func TestSpoolDoorbell_NoConsumerIsTheDefault(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	h := dialHome(t, c, doorbellHarp, CapPeerMessaging)

	require.NoError(t, h.RingSpool(spool.Ref{Harp: doorbellHarp, Dir: spool.DirOut, Name: doorbellName}))

	require.Eventually(t, func() bool {
		return c.SpoolDoorbellStats().Dropped == 1
	}, 10*time.Second, 10*time.Millisecond,
		"with no consumer registered the doorbell is dropped, and counted so the inertness is observable")
	assert.Zero(t, c.SpoolDoorbellStats().Rejected, "a valid ref nobody consumes is not a rejection")
}

// TestSpoolDoorbell_InvalidRefNeverReachesTheWire keeps the writer honest: a
// sender that rings about a name it could not itself resolve is a bug worth
// failing where the stack still names who did it, rather than travelling and
// being refused at a peer.
func TestSpoolDoorbell_InvalidRefNeverReachesTheWire(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	h := dialHome(t, c, doorbellHarp, CapPeerMessaging)

	bad := []spool.Ref{
		{Harp: doorbellHarp, Dir: spool.DirIn, Name: "../escape.md"},
		{Harp: "with/separator", Dir: spool.DirIn, Name: doorbellName},
		{Harp: doorbellHarp, Dir: spool.Dir("in/nowhere"), Name: doorbellName},
		{Harp: doorbellHarp, Dir: "", Name: doorbellName},
	}
	for _, ref := range bad {
		assert.Error(t, c.RingSpool(doorbellHarp, ref), "coordinator must refuse to ring about %s", ref)
		assert.Error(t, h.RingSpool(ref), "runner must refuse to ring about %s", ref)
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
