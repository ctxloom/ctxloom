package coord

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// D1 hermetic coverage: ConsumerService against a LIVE coordinator endpoint
// (real gRPC listeners, real StartRun-migrated child) — the same style as
// runchannel_test.go's plane-2 conformance suite.

// dialConsumer opens a bare gRPC connection carrying token as the bearer
// credential — deliberately NOT going through Home (which only speaks the
// runner/run channels); ConsumerService is a third, independent client.
func dialConsumer(t *testing.T, coordURL, token string) (agentcoordpb.ConsumerServiceClient, *grpc.ClientConn) {
	t.Helper()
	target, err := grpcTarget(coordURL)
	require.NoError(t, err)
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerCreds(token)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return agentcoordpb.NewConsumerServiceClient(conn), conn
}

// TestConsumerService_WatchRuns_SnapshotThenLiveDeltaText is the Recon #1
// fix, proven: a StartRun-migrated child's activity — invisible to the
// legacy agentbus TapHub (children.go's driveChild/hub.Tee is never called
// on the viaStartRun path) — arrives live on ConsumerService.WatchRuns,
// FULL payload including delta TEXT (not the journal's counts-only
// projection). The first frame is always the roster snapshot.
func TestConsumerService_WatchRuns_SnapshotThenLiveDeltaText(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	consumerToken := c.consumerCreds.token()
	require.NotEmpty(t, consumerToken, "Serve() must mint the consumer credential")
	client, _ := dialConsumer(t, c.LoopbackURL(), consumerToken)

	stream, err := client.WatchRuns(context.Background(), &agentcoordpb.WatchRunsRequest{})
	require.NoError(t, err)

	first, err := stream.Recv()
	require.NoError(t, err)
	_, isSnapshot := first.GetKind().(*agentcoordpb.WatchEvent_Snapshot)
	require.True(t, isSnapshot, "the first WatchRuns frame must be a RosterSnapshot, got %T", first.GetKind())

	// Spawn the migrated child AFTER the watcher attached — proving genuinely
	// LIVE delivery, not a replay.
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	frames := make(chan *agentcoordpb.WatchEvent, 32)
	go func() {
		for {
			f, ferr := stream.Recv()
			if ferr != nil {
				close(frames)
				return
			}
			frames <- f
		}
	}()

	// conformanceWait is left where it is deliberately. The awaited condition
	// costs 0.03-0.16s: 260 iterations under 30 CPU hogs and at GOMAXPROCS=1
	// never exceeded 0.16s, so a 5s expiry is not a budget that wants raising —
	// it is the whole process having stalled, and no number defends against
	// that. What the expiry DID lack was any way to tell "nothing was
	// published" from "the run published something else": `seen` is that, so
	// the next occurrence is diagnosable from the failure line alone rather
	// than from another triage cycle.
	deadline := time.After(conformanceWait)
	var sawDeltaText string
	var seen []string
	for sawDeltaText == "" {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("WatchRuns stream ended before a delta event arrived (saw %d live events: %v)", len(seen), seen)
			}
			ev := f.GetEvent()
			if ev == nil {
				continue
			}
			seen = append(seen, fmt.Sprintf("%T", ev.GetPayload()))
			assert.Equal(t, out.RunID, ev.GetRunId(), "every live event self-identifies its run")
			if d := ev.GetMessageDelta(); d != nil && strings.Contains(d.GetText(), "echo:") {
				sawDeltaText = d.GetText()
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a live delta-text event; %d live events arrived on the watch stream: %v",
				len(seen), seen)
		}
	}
	assert.Contains(t, sawDeltaText, "do the thing", "the scripted engine echoes the turn text verbatim")
}

// customFillEvent is one cheap, non-terminal AgentEvent for filling a
// watchHub subscriber's ring without needing a real run.
func customFillEvent(seq uint64) *agentcoordpb.AgentEvent {
	return &agentcoordpb.AgentEvent{
		Seq: seq, RunId: "r",
		Payload: &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{Name: "fill"}},
	}
}

// TestWatchHub_Broadcast_FullRingLossIsReportedBeforeTheNextEvent is the
// hub-level contract test for the ruled overflow behavior: a full subscriber
// ring still loses non-terminal events (the producer is never blocked), but
// NEVER silently — ONE synthetic EventsLost marker naming the exact seq
// range arrives ahead of the next event that fits. Restoring the plain drop
// (deliver forgetting noteLost) turns this red: after the ring's own
// contents the reader would see seq 261 with no marker before it.
func TestWatchHub_Broadcast_FullRingLossIsReportedBeforeTheNextEvent(t *testing.T) {
	h := newWatchHub()
	ch, cancel, _ := h.subscribe(nil)
	defer cancel()

	// Fill the ring to capacity (seq 1..256) without draining — the slow
	// subscriber broadcast's doc comment (invariant 1) describes.
	for i := 1; i <= watchRingSize; i++ {
		h.broadcast(customFillEvent(uint64(i)))
	}
	require.Len(t, ch, watchRingSize, "the ring must be completely full before the assertions below mean anything")

	// Four more on the full ring: lost, and broadcast must not block losing them.
	overflowDone := make(chan struct{})
	go func() {
		for seq := watchRingSize + 1; seq <= watchRingSize+4; seq++ {
			h.broadcast(customFillEvent(uint64(seq)))
		}
		close(overflowDone)
	}()
	select {
	case <-overflowDone:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast of a non-terminal event must never block, even on a full ring")
	}
	assert.Len(t, ch, watchRingSize, "a full ring never grows: the overflow was not queued")

	// The reader frees one slot — not enough for marker + event, so the next
	// broadcast is lost too rather than arriving without its marker.
	require.Equal(t, uint64(1), (<-ch).GetSeq())
	h.broadcast(customFillEvent(uint64(watchRingSize + 5)))
	assert.Len(t, ch, watchRingSize-1, "with one free slot the marker cannot precede the event, so the event joins the loss")

	// Two free slots: the marker and the next event go in, in that order.
	require.Equal(t, uint64(2), (<-ch).GetSeq())
	h.broadcast(customFillEvent(uint64(watchRingSize + 6)))
	require.Len(t, ch, watchRingSize, "marker + event fill the two freed slots")

	for seq := 3; seq <= watchRingSize; seq++ {
		require.Equal(t, uint64(seq), (<-ch).GetSeq(), "the ring's own contents arrive intact, in order")
	}
	marker := <-ch
	lost, ok := marker.GetPayload().(*agentcoordpb.AgentEvent_EventsLost)
	require.True(t, ok, "the first event after the ring's contents must be the EventsLost marker, got %T", marker.GetPayload())
	require.Len(t, lost.EventsLost.GetLost(), 1, "one run lost one contiguous range")
	r := lost.EventsLost.GetLost()[0]
	assert.Equal(t, "r", r.GetRunId())
	assert.Equal(t, uint64(watchRingSize+1), r.GetFirstSeq(), "the gap starts at the first event the full ring refused")
	assert.Equal(t, uint64(watchRingSize+5), r.GetLastSeq(), "the gap ends at the last event lost before room appeared")
	assert.Equal(t, uint64(0), marker.GetSeq(), "the marker is synthetic: it carries no seq of its own")
	assert.Equal(t, uint64(watchRingSize+6), (<-ch).GetSeq(), "the event that ended the loss episode follows its marker")
}

// TestWatchHub_Broadcast_TerminalOnFullRingIsPrecededByItsLossMarker pins
// invariant 2 against the new contract: the run's one terminal (RunCompleted)
// still fights for delivery on a full ring by evicting queued events — and
// each eviction is a loss the subscriber is told about, in the same marker as
// the events the full ring refused, delivered BEFORE the terminal.
func TestWatchHub_Broadcast_TerminalOnFullRingIsPrecededByItsLossMarker(t *testing.T) {
	h := newWatchHub()
	ch, cancel, _ := h.subscribe(nil)
	defer cancel()

	for i := 1; i <= watchRingSize; i++ {
		h.broadcast(customFillEvent(uint64(i)))
	}
	h.broadcast(customFillEvent(uint64(watchRingSize + 1))) // refused: the ring is full

	term := &agentcoordpb.AgentEvent{
		Seq: uint64(watchRingSize + 2), RunId: "r",
		Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{}},
	}
	termDone := make(chan struct{})
	go func() {
		h.broadcast(term)
		close(termDone)
	}()
	select {
	case <-termDone:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast of the terminal event must never block")
	}
	require.Len(t, ch, watchRingSize, "ring size unchanged: two slots were evicted for marker + terminal, not grown")

	// seq 1 and 2 were evicted (oldest first); 3..256 survive; then the marker; then the terminal.
	for seq := 3; seq <= watchRingSize; seq++ {
		require.Equal(t, uint64(seq), (<-ch).GetSeq())
	}
	marker := <-ch
	lost, ok := marker.GetPayload().(*agentcoordpb.AgentEvent_EventsLost)
	require.True(t, ok, "the marker must precede the terminal, got %T", marker.GetPayload())
	var ranges [][2]uint64
	for _, r := range lost.EventsLost.GetLost() {
		assert.Equal(t, "r", r.GetRunId())
		ranges = append(ranges, [2]uint64{r.GetFirstSeq(), r.GetLastSeq()})
	}
	assert.Equal(t, [][2]uint64{{uint64(watchRingSize + 1), uint64(watchRingSize + 1)}, {1, 2}}, ranges,
		"the marker names the refused event AND the two evicted ones, each run-contiguous run of seqs as one range")
	assert.Same(t, term, <-ch, "the terminal lands right after its marker")
}

// TestWatchSub_NoteLost_CoalescesContiguousSeqsPerRun pins the marker's
// shape on a hub-wide ring where two runs' events interleave: contiguous
// seqs of one run fold into one range even with another run's events
// between them, so the marker stays bounded by runs, not by events lost.
func TestWatchSub_NoteLost_CoalescesContiguousSeqsPerRun(t *testing.T) {
	sub := &watchSub{ch: make(chan *agentcoordpb.AgentEvent, 1)}
	sub.noteLost(nonTerminalEvent(7, "a"))
	sub.noteLost(nonTerminalEvent(1, "b"))
	sub.noteLost(nonTerminalEvent(8, "a"))
	sub.noteLost(nonTerminalEvent(2, "b"))
	sub.noteLost(nonTerminalEvent(3, "a")) // a's evicted tail: not contiguous with 8, a new range

	require.Len(t, sub.lost, 3)
	assert.Equal(t, "a", sub.lost[0].GetRunId())
	assert.Equal(t, [2]uint64{7, 8}, [2]uint64{sub.lost[0].GetFirstSeq(), sub.lost[0].GetLastSeq()})
	assert.Equal(t, "b", sub.lost[1].GetRunId())
	assert.Equal(t, [2]uint64{1, 2}, [2]uint64{sub.lost[1].GetFirstSeq(), sub.lost[1].GetLastSeq()})
	assert.Equal(t, "a", sub.lost[2].GetRunId())
	assert.Equal(t, [2]uint64{3, 3}, [2]uint64{sub.lost[2].GetFirstSeq(), sub.lost[2].GetLastSeq()})
}

// TestSendTerminal_EvictsOldestWhenFull is a focused unit test on the
// bounded-retry shape itself: the FIFO channel's oldest queued events (not
// some other ones) are what get evicted, and because every eviction is a
// loss the subscriber must hear about, the terminal lands right after the
// marker naming them — two evictions, well inside terminalEvictAttempts.
func TestSendTerminal_EvictsOldestWhenFull(t *testing.T) {
	sub := &watchSub{ch: make(chan *agentcoordpb.AgentEvent, 2)}
	sub.ch <- nonTerminalEvent(1, "r")
	sub.ch <- nonTerminalEvent(2, "r")
	term := terminalEvent(3, "r")

	sendTerminal(sub, term)

	require.Len(t, sub.ch, 2, "sendTerminal must not grow the ring — evict, then place")
	got := <-sub.ch
	lost, ok := got.GetPayload().(*agentcoordpb.AgentEvent_EventsLost)
	require.True(t, ok, "the evicted events must be reported by a marker ahead of the terminal, got %T", got.GetPayload())
	require.Len(t, lost.EventsLost.GetLost(), 1)
	assert.Equal(t, uint64(1), lost.EventsLost.GetLost()[0].GetFirstSeq())
	assert.Equal(t, uint64(2), lost.EventsLost.GetLost()[0].GetLastSeq())
	assert.Same(t, term, <-sub.ch, "the terminal event must be the one placed")
	assert.Nil(t, sub.lost, "flushing the marker clears the pending loss")
}

// TestSendTerminal_TerminalWinsWhenOnlyOneSlotCanBeFreed pins the priority
// when a lagged subscriber's ring cannot yield two slots (the rest is other
// runs' terminals, which are never evicted): the terminal is placed and the
// marker stays pending — dropping the terminal would hang the watcher,
// deferring the marker does not.
func TestSendTerminal_TerminalWinsWhenOnlyOneSlotCanBeFreed(t *testing.T) {
	sub := &watchSub{ch: make(chan *agentcoordpb.AgentEvent, 2)}
	sub.ch <- terminalEvent(1, "other")
	sub.ch <- nonTerminalEvent(5, "r")
	term := terminalEvent(6, "r")

	sendTerminal(sub, term)

	require.Len(t, sub.ch, 2)
	assert.Equal(t, "other", (<-sub.ch).GetRunId(), "the other run's terminal survives")
	assert.Same(t, term, <-sub.ch, "the terminal takes the one slot the eviction freed")
	require.Len(t, sub.lost, 1, "the evicted event stays a pending loss for the next flush")
	assert.Equal(t, uint64(5), sub.lost[0].GetFirstSeq())
}

// TestSendTerminal_NeverBlocksWhenChannelIsWedged proves the bounded give-up
// path: a channel nobody will ever read from or write to concurrently (the
// worst case — no eviction ever succeeds) still returns promptly instead of
// blocking forever, matching consumer.go's never-block invariant even for
// the terminal-delivery exception.
func TestSendTerminal_NeverBlocksWhenChannelIsWedged(t *testing.T) {
	sub := &watchSub{ch: make(chan *agentcoordpb.AgentEvent)} // unbuffered; nothing ever sends or receives concurrently
	term := &agentcoordpb.AgentEvent{Seq: 1, Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{}}}
	done := make(chan struct{})
	go func() {
		sendTerminal(sub, term)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sendTerminal must give up and return after its bounded attempts, never block forever")
	}
}

// TestConsumerService_ListRuns pins the unary poll alternative: the same
// roster projection plane-2 ListRuns exposes, reachable without a stream.
func TestConsumerService_ListRuns(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	client, _ := dialConsumer(t, c.LoopbackURL(), c.consumerCreds.token())
	require.Eventually(t, func() bool {
		res, err := client.ListRuns(context.Background(), &agentcoordpb.ListRunsRequest{})
		if err != nil || len(res.GetRuns()) != 1 {
			return false
		}
		return res.GetRuns()[0].GetRunId() == out.RunID
	}, conformanceWait, 10*time.Millisecond)
}

// TestConsumerService_SpoolStats_ReportsLiveCounters pins the one path an
// out-of-process diagnostic has onto the coordinator's spool counters: the
// values the in-process accessors (SpoolDeliveryStats, SpoolDoorbellStats,
// PushUnavailableCount) return are exactly what the unary RPC hands a
// consumer credential — every counter, each on its own wire field, so a
// tally of failures cannot be mistaken for a tally of deliveries.
func TestConsumerService_SpoolStats_ReportsLiveCounters(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, startRunSpawner(nil), nil)
	// Five distinct values so a field crossed with any other is caught.
	c.spoolDeliveryCount.delivered.Add(11)
	c.spoolDeliveryCount.consumed.Add(12)
	c.spoolDeliveryCount.failed.Add(13)
	c.spoolDoorbell.dropped.Add(14)
	c.spoolDoorbell.rejected.Add(15)

	client, _ := dialConsumer(t, c.LoopbackURL(), c.consumerCreds.token())
	res, err := client.SpoolStats(context.Background(), &agentcoordpb.SpoolStatsRequest{})
	require.NoError(t, err)
	assert.Equal(t, c.SpoolDeliveryStats().Delivered, res.GetDelivered())
	assert.Equal(t, c.SpoolDeliveryStats().Consumed, res.GetConsumed())
	assert.Equal(t, c.SpoolDeliveryStats().Failed, res.GetFailed())
	assert.Equal(t, c.SpoolDoorbellStats().Dropped, res.GetDoorbellDropped())
	assert.Equal(t, c.SpoolDoorbellStats().Rejected, res.GetDoorbellRejected())
	assert.Equal(t, uint64(11), res.GetDelivered())
	assert.Equal(t, uint64(15), res.GetDoorbellRejected())
}

// TestConsumerService_SpoolStats_RequiresCredential: the counters ride the
// same authenticated surface as the roster — no bearer, no numbers.
func TestConsumerService_SpoolStats_RequiresCredential(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, startRunSpawner(nil), nil)
	client, _ := dialConsumer(t, c.LoopbackURL(), "not-a-credential")
	_, err := client.SpoolStats(context.Background(), &agentcoordpb.SpoolStatsRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

// TestConsumer_CredentialRejectedOnCoordinatorService is the read-only scope
// enforcement: a consumer credential authenticates ConsumerService only — it
// must be a rejected IDENTITY (PermissionDenied), not merely an unauthorized
// verb, on RunnerChannel/RunChannel.
func TestConsumer_CredentialRejectedOnCoordinatorService(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	token := c.consumerCreds.token()
	require.NotEmpty(t, token)

	target, err := grpcTarget(c.LoopbackURL())
	require.NoError(t, err)
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerCreds(token)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	coordClient := agentcoordpb.NewCoordinatorServiceClient(conn)

	runStream, err := coordClient.RunChannel(context.Background())
	require.NoError(t, err) // stream establishment succeeds; the interceptor rejects per-stream
	_ = runStream.Send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Hello{Hello: &agentcoordpb.Hello{}}})
	_, err = runStream.Recv()
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "a consumer credential must not authenticate CoordinatorService: %v", err)

	stream, err := coordClient.RunnerChannel(context.Background())
	require.NoError(t, err) // stream establishment succeeds; the interceptor rejects per-stream
	// The stream interceptor rejects BEFORE the RunnerChannel handler ever
	// runs, so the server can close the stream before this Send's Hello
	// frame lands — Send legitimately surfaces io.EOF/Unavailable in that
	// race under load, not a real client bug (hoary-amigo — the same classic
	// gRPC client-streaming antipattern artifacts_test.go's uploadRaw had).
	// The authoritative status always rides Recv, never a Send error, so a
	// Send failure here is tolerated.
	_ = stream.Send(&agentcoordpb.RunnerFrame{Kind: &agentcoordpb.RunnerFrame_Hello{Hello: &agentcoordpb.RunnerHello{}}})
	_, err = stream.Recv()
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

// TestConsumer_CredentialPersistedInEndpointFile pins the D1 discovery
// mechanism: an out-of-process viewer has no spawn-time env seam, so
// endpoint.json (0600, host-local) is its only way to learn the URL AND the
// read-only credential.
func TestConsumer_CredentialPersistedInEndpointFile(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

	raw, err := os.ReadFile(filepath.Join(c.stateDir, "endpoint.json"))
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(c.stateDir, "endpoint.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "endpoint.json must stay 0600 — it now carries a credential")

	var ep endpointState
	require.NoError(t, json.Unmarshal(raw, &ep))
	assert.NotEmpty(t, ep.ConsumerCred)
	assert.Equal(t, c.consumerCreds.token(), ep.ConsumerCred)

	// The persisted token actually authenticates ConsumerService.
	client, _ := dialConsumer(t, c.LoopbackURL(), ep.ConsumerCred)
	_, err = client.ListRuns(context.Background(), &agentcoordpb.ListRunsRequest{})
	require.NoError(t, err)
}
