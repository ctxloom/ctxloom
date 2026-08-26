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

// TestWatchHub_Broadcast_TerminalEvictsOnFullBufferAndNonTerminalStillDrops
// is PART 2's hub-level contract test: a full subscriber ring still drops a
// non-terminal event exactly as before (invariant 1, unchanged), but the
// run's one terminal event (RunCompleted) evicts the oldest queued event to
// make room rather than being silently lost — and never blocks the caller
// either way. "Direct seq inspection" (the brief's alternative to routing
// through PART 1) proves the eviction here: a baseline event is drained
// first, the ring is filled contiguously behind it, and after the terminal
// broadcast the next drained seq skips exactly the one evicted event.
func TestWatchHub_Broadcast_TerminalEvictsOnFullBufferAndNonTerminalStillDrops(t *testing.T) {
	h := newWatchHub()
	ch, cancel, _ := h.subscribe(nil)
	defer cancel()

	// Baseline: one event, drained immediately (mirrors PART 1's own
	// baseline discipline — this fixes "the next expected seq" at 2).
	h.broadcast(customFillEvent(1))
	baseline := <-ch
	require.Equal(t, uint64(1), baseline.GetSeq())

	// Fill the ring to capacity (seq 2..257) without draining — the slow
	// subscriber broadcast's own doc comment (invariant 1) describes.
	for i := 0; i < watchRingSize; i++ {
		h.broadcast(customFillEvent(uint64(2 + i)))
	}
	require.Len(t, ch, watchRingSize, "the ring must be completely full before the assertions below mean anything")

	// A NON-terminal event on a full ring must still drop (invariant 1 is
	// unchanged by this fix) — and broadcast must not block doing it.
	overflowDone := make(chan struct{})
	go func() {
		h.broadcast(customFillEvent(uint64(2 + watchRingSize)))
		close(overflowDone)
	}()
	select {
	case <-overflowDone:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast of a non-terminal event must never block, even on a full ring")
	}
	assert.Len(t, ch, watchRingSize, "a non-terminal event must still be dropped when the ring is full")

	// The terminal event fights for delivery — it must arrive despite the
	// full ring, and broadcast must still never block.
	term := &agentcoordpb.AgentEvent{
		Seq: uint64(3 + watchRingSize), RunId: "r",
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

	// Drain the ring and confirm: the terminal event is in it, capacity was
	// never exceeded (exactly one slot was evicted to make room), and the
	// first drained seq after the baseline reveals the evicted hole.
	var gotTerminal bool
	var firstAfterBaseline uint64
	drained := 0
drain:
	for {
		select {
		case ev := <-ch:
			drained++
			if firstAfterBaseline == 0 {
				firstAfterBaseline = ev.GetSeq()
			}
			if _, ok := ev.GetPayload().(*agentcoordpb.AgentEvent_RunCompleted); ok {
				gotTerminal = true
			}
		default:
			break drain
		}
	}
	assert.True(t, gotTerminal, "the terminal event must be received despite the full ring")
	assert.Equal(t, watchRingSize, drained, "ring size unchanged: one slot was evicted to make room, not grown")
	assert.Equal(t, uint64(3), firstAfterBaseline, "the oldest queued event (seq 2) must have been evicted — a real, honest gap")
}

// TestSendTerminal_EvictsOldestWhenFull is a focused unit test on the
// bounded-retry shape itself: the FIFO channel's oldest queued event (not
// some other one) is what gets evicted, and the terminal event lands right
// after it — one eviction, one successful retry, well inside
// terminalEvictAttempts.
// TestWatchHub_Narrow_ScopesToOneRun is PART 1's hub-level contract
// test. startContainerOwnedRun must subscribe BEFORE the run's ID exists
// (StartOwnedRun mints it internally, mid-call — see run_owned.go's own
// comment on that ordering), so subscribe(nil) — hub-wide — is unavoidable at
// that instant. But staying hub-wide for the run's ENTIRE lifetime means
// every other concurrent run sharing this coordinator competes for the same
// watchRingSize-slot ring, silently stealing this run's own delivery budget.
// narrow lets the caller re-scope the SAME subscription, in place, the
// moment it learns its run's ID — proven here by broadcasting an unrelated
// run's events both before and after narrowing: they arrive while
// unscoped, and are excluded from the ring entirely (not merely
// client-filtered) once narrowed.
func TestWatchHub_Narrow_ScopesToOneRun(t *testing.T) {
	h := newWatchHub()
	ch, cancel, narrow := h.subscribe(nil)
	defer cancel()

	other := &agentcoordpb.AgentEvent{
		Seq: 1, RunId: "other-run",
		Payload: &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{Name: "fill"}},
	}
	h.broadcast(other)
	select {
	case ev := <-ch:
		assert.Equal(t, "other-run", ev.GetRunId(), "unscoped (nil filter): every run's events reach this subscriber")
	default:
		t.Fatal("an unscoped subscription must receive every run's events")
	}

	narrow("target-run")

	// Now unrelated events must never even enter the ring — proven by
	// filling the ring past capacity with them and confirming it never
	// reports full to a non-blocking broadcast, unlike the shared-ring test
	// above.
	for i := 0; i < watchRingSize+1; i++ {
		h.broadcast(&agentcoordpb.AgentEvent{
			Seq: uint64(2 + i), RunId: "other-run",
			Payload: &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{Name: "fill"}},
		})
	}
	assert.Empty(t, ch, "after narrowing, a foreign run's events must never occupy this subscriber's ring at all")

	mine := &agentcoordpb.AgentEvent{
		Seq: 999, RunId: "target-run",
		Payload: &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{Name: "fill"}},
	}
	h.broadcast(mine)
	select {
	case ev := <-ch:
		assert.Equal(t, "target-run", ev.GetRunId(), "the narrowed run's own events must still arrive")
	default:
		t.Fatal("the narrowed-to run's events must still be delivered")
	}
}

func TestSendTerminal_EvictsOldestWhenFull(t *testing.T) {
	ch := make(chan *agentcoordpb.AgentEvent, 2)
	ch <- &agentcoordpb.AgentEvent{Seq: 1}
	ch <- &agentcoordpb.AgentEvent{Seq: 2}
	term := &agentcoordpb.AgentEvent{Seq: 3, Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{}}}

	sendTerminal(ch, term)

	require.Len(t, ch, 2, "sendTerminal must not grow the ring — evict one, then place one")
	got := <-ch
	assert.Equal(t, uint64(2), got.GetSeq(), "the OLDEST queued event (seq 1) must be the one evicted, not seq 2")
	got = <-ch
	assert.Same(t, term, got, "the terminal event must be the one placed")
}

// TestSendTerminal_NeverBlocksWhenChannelIsWedged proves the bounded give-up
// path: a channel nobody will ever read from or write to concurrently (the
// worst case — no eviction ever succeeds) still returns promptly instead of
// blocking forever, matching consumer.go's never-block invariant even for
// the terminal-delivery exception.
func TestSendTerminal_NeverBlocksWhenChannelIsWedged(t *testing.T) {
	ch := make(chan *agentcoordpb.AgentEvent) // unbuffered; nothing ever sends or receives concurrently
	term := &agentcoordpb.AgentEvent{Seq: 1, Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{}}}
	done := make(chan struct{})
	go func() {
		sendTerminal(ch, term)
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
