package coord

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// Plane-2 conformance (B1.6 deliverable 2): the RunChannel against a LIVE
// coordinator endpoint, driven through the runner-side Home — reissue
// idempotency, parked-recv preemption, crash-between-notice-and-consume
// redelivery, timeout fallbacks, lineage-checked stop, and the report path.

// ownerHome registers a session-owner credential and opens a Home on it
// (Hello with an empty run_id — the owner attach).
func ownerHome(t *testing.T, c *Coordinator) *Home {
	t.Helper()
	token, err := c.RegisterSessionOwner(ownerIdentity().Harp)
	require.NoError(t, err)
	h, err := NewHome(context.Background(), HomeConfig{
		URL:     c.LoopbackURL(),
		Token:   token,
		RunID:   "", // depth-0: the channel attaches to the owning session
		Harness: "mock",
		Version: "test",
		Harp:    ownerIdentity().Harp,
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })
	return h
}

// childHome returns the Home of the spawned child's OWN runner — the one the
// fake spawner stood up for that run. A run has exactly one runner: dialing a
// second Home with the same credential would supersede the first, and the
// coordinator would refuse the StartRun it was about to issue.
func childHome(t *testing.T, c *Coordinator, runID string) *Home {
	t.Helper()
	sp := c.spawner.(*fakeSpawner)
	var h *Home
	require.Eventually(t, func() bool {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		for _, home := range sp.engineHomes {
			if home.cfg.RunID == runID {
				h = home
				return true
			}
		}
		return false
	}, conformanceWait, 10*time.Millisecond, "the child's runner never came up")
	return h
}

func spawnResearcher(t *testing.T, c *Coordinator) *RunOutcome {
	t.Helper()
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "researcher", "find the thing", "", "")
	require.NoError(t, err)
	return out
}

func researcherSpawner() *fakeSpawner {
	return newFakeSpawner(map[string]fakeAgent{
		"researcher": {perm: "bypass", profiles: []string{"p1"}},
	}, nil)
}

// TestRunChannel_ChildSendReachesParent: a child's plane-2 PeerSendRequest
// (to_role "parent") lands in the parent's durable mailbox with the kind
// carried on the typed Kind field (coordination.proto field 7).
func TestRunChannel_ChildSendReachesParent(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	h := childHome(t, c, out.RunID)

	resp, err := h.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress,
			Text:   "found it",
			Kind:   agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	require.NotEmpty(t, resp.GetPeerSend().GetMessageId())

	msgs := recvBody(t, c, "found it", time.Second)
	require.Len(t, msgs, 1)
	assert.Equal(t, "result", msgs[0].Kind)
	assert.Equal(t, out.Harp, msgs[0].From)
}

// TestRunChannel_RequestIdempotency: a reissued request (same request_id)
// gets the CACHED response — one queued message, same message_id back.
func TestRunChannel_RequestIdempotency(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	// A request kind that still rides the wire (agent_send does not: it is a
	// local spool write). A custom handler makes the double execution
	// directly countable.
	var calls atomic.Int32
	c.SetCustomHandlers(map[string]CustomHandler{
		"count": func(context.Context, Identity, json.RawMessage) (json.RawMessage, error) {
			calls.Add(1)
			return json.RawMessage(`{"n":1}`), nil
		},
	})
	out := spawnResearcher(t, c)
	h := childHome(t, c, out.RunID)

	mk := func() *agentcoordpb.AgentRequest {
		return &agentcoordpb.AgentRequest{
			RequestId: "req-fixed-1",
			Kind:      &agentcoordpb.AgentRequest_Custom{Custom: &agentcoordpb.CustomRequest{Name: "count"}},
		}
	}
	first, err := h.Request(context.Background(), mk())
	require.NoError(t, err)
	second, err := h.Request(context.Background(), mk())
	require.NoError(t, err)
	require.EqualValues(t, 0, first.GetStatus().GetCode(), first.GetStatus().GetMessage())
	assert.Equal(t, first.GetCustom().AsMap(), second.GetCustom().AsMap(),
		"the reissued request_id returns the cached response")
	assert.EqualValues(t, 1, calls.Load(), "the handler ran exactly once for the reissued request_id")
}

// TestRunChannel_RecvPreemptionAndTimeout: the newest recv preempts a parked
// one (typed error), and a timed-out recv fails with the recv-timeout
// contract.
func TestRunChannel_RecvPreemptionAndTimeout(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	h := childHome(t, c, out.RunID)

	firstErr := make(chan error, 1)
	go func() {
		_, err := h.Recv(context.Background(), conformanceWait)
		firstErr <- err
	}()
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.park != nil
	}, conformanceWait, 10*time.Millisecond)

	// The newer receive preempts the parked one...
	_, err := h.Recv(context.Background(), 50*time.Millisecond)
	// ...and, with no mail arriving, itself times out.
	require.ErrorIs(t, err, ErrRecvTimeout)

	select {
	case ferr := <-firstErr:
		require.ErrorIs(t, ferr, ErrRecvPreempted)
	case <-time.After(conformanceWait):
		t.Fatal("preempted recv never completed")
	}
}

// TestRunChannel_StopRunLineage: plane-2 stop_run (rev-7 D1) terminates the
// caller's own child and refuses a foreign run id.
func TestRunChannel_StopRunLineage(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	// The stop below must find a RUNNING child, not one whose StartRun is
	// still in flight — a stop that lands mid-launch ends the run as a
	// cancelled launch, which is a different terminal.
	require.NoError(t, c.awaitChildUp(context.Background(), out.Harp))
	owner := ownerHome(t, c)

	// A child cannot stop itself (its lineage owns nothing).
	child := childHome(t, c, out.RunID)
	resp, err := child.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: out.RunID}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, codes.PermissionDenied, resp.GetStatus().GetCode())

	// The parent may, and its `reason` is HONOURED: it used to be
	// advertised in agent_stop's tool schema and discarded. It must reach the
	// run's durable terminal detail, and a second stop must report it back.
	resp, err = owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{
			RunId:  out.RunID,
			Reason: "superseded by a narrower brief",
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())

	rec := RunRecord{}
	c.runs.View(func() { rec = *c.runsF.run(out.RunID) })
	assert.True(t, rec.Ended)
	assert.Equal(t, CauseStopped, rec.Cause)
	assert.Contains(t, rec.Detail, "superseded by a narrower brief")

	// Stopping it again reports the recorded reason rather than the bare cause.
	resp, err = owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: out.RunID}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, codes.OK, resp.GetStatus().GetCode())
	assert.Contains(t, resp.GetStatus().GetMessage(), "superseded by a narrower brief")
}

// TestRunChannel_RosterProjection: list_runs projects the roster fold with
// the harp as agent_id and the latest report summary.
func TestRunChannel_RosterProjection(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	owner := ownerHome(t, c)
	child := childHome(t, c, out.RunID)

	require.NoError(t, child.Report(context.Background(), &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS,
		Text:  "halfway there",
	}, nil))

	resp, err := owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_ListRuns{ListRuns: &agentcoordpb.ListRunsRequest{}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	runs := resp.GetListRuns().GetRuns()
	require.Len(t, runs, 1)
	assert.Equal(t, out.Harp, runs[0].GetAgent().GetAgentId())
	assert.Equal(t, out.RunID, runs[0].GetRunId())
	assert.Contains(t, runs[0].GetLatestSummary(), "halfway there")
}

// TestRunChannel_ReportDurability: Report returns only after the facts are
// journaled (Ack-gated); artifact revisions are coordinator-assigned,
// monotonic, and content-addressed (unchanged sha ≠ new revision).
func TestRunChannel_ReportDurability(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	child := childHome(t, c, out.RunID)

	art := func(sha byte) *agentcoordpb.ArtifactProduced {
		return &agentcoordpb.ArtifactProduced{
			ArtifactId: "plan/x",
			Kind:       agentcoordpb.ArtifactKind_ARTIFACT_KIND_IMPLEMENTATION_PLAN,
			Name:       "x.plan.md",
			Sha256:     []byte{sha},
		}
	}
	require.NoError(t, child.Report(context.Background(), &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_CHECKPOINT, Text: "cp1",
	}, []*agentcoordpb.ArtifactProduced{art(1)}))
	// SCOPE, HERE, IS JUST A CARRIER. What this test is about — Ack-gated
	// durability and coordinator-assigned revisions — is the same whatever
	// scope the report has, and these two were FINAL only incidentally. They
	// cannot be: FINAL is the completion contract, so the first one now ENDS
	// THE RUN (drain.go, endOnFinalReport), and the run whose channel this
	// reports over is severed before the second could land.
	require.NoError(t, child.Report(context.Background(), &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS, Text: "done",
	}, []*agentcoordpb.ArtifactProduced{art(1)})) // unchanged content
	require.NoError(t, child.Report(context.Background(), &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS, Text: "done v2",
	}, []*agentcoordpb.ArtifactProduced{art(2)})) // changed content

	assert.Contains(t, c.LatestReport(out.Harp), "done v2")
	arts := c.Artifacts(out.Harp)
	require.Len(t, arts, 1)
	assert.EqualValues(t, 2, arts[0].Revision, "unchanged sha did not mint a revision; changed sha did")
}

// TestRunChannel_ForeignRunIDRejected: a Hello presenting a run_id the
// credential does not own is rejected.
func TestRunChannel_ForeignRunIDRejected(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)

	h, err := NewHome(context.Background(), HomeConfig{
		URL:     env[EnvCoordURL],
		Token:   env[EnvCoordCred],
		RunID:   "run-not-mine",
		Harness: "mock",
		Version: "test",
		Harp:    env["CTXLOOM_SESSION_HARP"],
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })

	// The channel never attaches; a request cannot complete.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, rerr := h.Request(ctx, &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_ListRuns{ListRuns: &agentcoordpb.ListRunsRequest{}},
	})
	require.ErrorIs(t, rerr, ErrCoordinatorUnreachable)
}

// TestServePeerSend_UnmarshalableStructuredIsRefused is the regression guard:
// servePeerSend marshalled the caller's Struct with
// `if raw, merr := protojson.Marshal(s); merr == nil { structured = raw }` and
// NEVER inspected merr. On failure `structured` stayed nil and the message was
// queued WITHOUT its structured payload, reported as a successful send.
//
// For a parent answering a relayed approval that converts a decision into an
// unanswerable message: the decode side is strict and then reports "structured
// is required", attributing the fault to the sender, who was told the send
// succeeded. Refusing the send is what serveCustom two functions below already
// does with the identical protojson.Marshal failure.
//
// A Value with no oneof member set is the shape protojson rejects — the
// zero-value *structpb.Value a hand-built Struct can easily carry.
func TestAgentSend_UnmarshalableStructuredIsRefused(t *testing.T) {
	unmarshalable := &structpb.Struct{Fields: map[string]*structpb.Value{"kind": {}}}
	require.Error(t, func() error { _, err := protojson.Marshal(unmarshalable); return err }(),
		"fixture check: this Struct must really be unmarshalable, or the test proves nothing")

	c, out, resp := childSpoolSend(t, &agentcoordpb.PeerSendRequest{
		ToRole: ParentAddress, Text: "decision", Structured: unmarshalable,
		Kind: agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
	})
	assert.NotEqualValues(t, 0, resp.GetStatus().GetCode(),
		"a send whose structured payload cannot be carried must not report OK")
	assert.Empty(t, resp.GetPeerSend().GetMessageId(),
		"a refused send must not hand back a message id")
	for _, e := range spoolEntries(t, out.Harp, spool.DirOut) {
		assert.NotEqual(t, "decision", e.Message.Body, "a refused send must not write a hollowed-out message")
	}
	assert.Empty(t, recvBody(t, c, "decision", 200*time.Millisecond))
}

// TestServeStopRun_CancelsLaunch is plane-2 agent_stop's twin of
// Coordinator.AgentStop's own launch-cancellation fix: a stop that
// only ends the run record cannot stop a LAUNCHER — an armed relaunch or an
// in-flight container prepare (a seconds-wide window) carries on behind a
// response that already said "stopped". The host-side AgentStop verb calls
// cancelLaunch "on BOTH paths"; plane-2's
// serveStopRun (the path a coordinator-capable CHILD uses to stop its own
// grandchild) must call it too — a launcher reachable from either surface
// must be cancellable from either surface.
func TestServeStopRun_CancelsLaunch(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	owner := ownerHome(t, c)

	require.False(t, c.launchStopped(out.Harp), "precondition: no stop has landed yet")

	resp, err := owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: out.RunID}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode())

	assert.True(t, c.launchStopped(out.Harp),
		"plane-2 agent_stop (serveStopRun) must cancel the launch exactly like the host-side AgentStop verb — "+
			"an armed relaunch or an in-flight container prepare must turn back, not carry on behind a "+
			"response that already said \"stopped\"")
}

// TestServeStopRun_CancelsLaunch_EvenWhenAlreadyEnded reproduces the exact
// hazard shape on the plane-2 surface: a stop landing on a run
// that has ALREADY ended, with a relaunch armed behind it (simulated here by
// clearLaunchGate — exactly what a fresh agent_send/inject delivery to an
// ended child does). The already-ended early return must still cancel the
// launch, not just report "already ended" and leave the armed relaunch to
// carry on.
func TestServeStopRun_CancelsLaunch_EvenWhenAlreadyEnded(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	owner := ownerHome(t, c)

	first, err := owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: out.RunID}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, first.GetStatus().GetCode())
	require.True(t, c.launchStopped(out.Harp), "precondition: the first stop already cancelled the launch")

	// Simulate a fresh delivery re-arming a relaunch (clearLaunchGate is
	// exactly what agent_send/inject call on a fresh ask to an ended child).
	c.clearLaunchGate(out.Harp)
	require.False(t, c.launchStopped(out.Harp), "precondition: the re-arm actually cleared the stop bit")

	second, err := owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: out.RunID}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, second.GetStatus().GetCode())
	assert.Contains(t, second.GetStatus().GetMessage(), "already ended", "sanity: this IS the already-ended branch")

	assert.True(t, c.launchStopped(out.Harp),
		"the already-ended branch must still cancel the launch — the exact 2026-07-24 incident shape: a stop "+
			"landing on an already-ended run with a relaunch armed behind it must not let that relaunch carry on")
}

// TestRunChannel_StopRunOmittedRunId_SweepsTheCallersChildren: plane-2
// agent_stop with NO run_id is the bulk form — every live child of the
// CALLER is stopped under the drain bound and the result names each one with
// its outcome. With a run_id the verb is untouched (TestRunChannel_StopRunLineage).
func TestRunChannel_StopRunOmittedRunId_SweepsTheCallersChildren(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	c.drainBound = 300 * time.Millisecond
	first := spawnResearcher(t, c)
	second := spawnResearcher(t, c)
	owner := ownerHome(t, c)

	resp, err := owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{Reason: "fan-out complete"}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())

	children := resp.GetStopRun().GetChildren()
	require.Len(t, children, 2, "the result names EVERY child, not a count")
	byHarp := map[string]*agentcoordpb.StopRunResult_Child{}
	for _, ch := range children {
		byHarp[ch.GetHarp()] = ch
	}
	for _, out := range []*RunOutcome{first, second} {
		ch := byHarp[out.Harp]
		require.NotNil(t, ch, "child %s missing from %v", out.Harp, children)
		assert.Equal(t, out.RunID, ch.GetRunId())
		assert.Equal(t, "researcher", ch.GetAgent())
		assert.Contains(t, []string{StopOutcomeStopped, StopOutcomeInterrupted}, ch.GetOutcome())
		assert.Contains(t, ch.GetDetail(), "fan-out complete", "the reason reaches each child's terminal detail")
		assert.Equal(t, StateEnded, rosterState(c, out.Harp))
		assert.Equal(t, CauseStopped, currentRunCause(c, out.Harp))
	}
	for _, e := range c.Roster() {
		assert.Equal(t, StateEnded, e.State, "roster afterwards shows none live: %+v", e)
	}
}

// TestRunChannel_StopRunOmittedRunIdAndReason_IsRefused: omitting BOTH is
// refused, naming the missing reason, and nothing is stopped — the bulk form
// cannot be reached by an accidental omission.
func TestRunChannel_StopRunOmittedRunIdAndReason_IsRefused(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	owner := ownerHome(t, c)

	resp, err := owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, codes.InvalidArgument, resp.GetStatus().GetCode())
	assert.Contains(t, resp.GetStatus().GetMessage(), "reason")
	assert.Contains(t, resp.GetStatus().GetMessage(), "run_id")
	assert.NotEqual(t, StateEnded, rosterState(c, out.Harp), "a refused sweep stops nothing")
}

// TestPeerMessageProto_ProjectsKindOntoTheTypedField is the push side of the
// typed-kind contract: a mailbox message rides the wire with PeerMessage.kind
// set from the closed vocabulary, and `structured` carries the sender's
// companion VERBATIM — no "kind" key is merged in, because nothing on the
// receive side reads one anymore.
func TestPeerMessageProto_ProjectsKindOntoTheTypedField(t *testing.T) {
	for _, kind := range MailKinds() {
		pm, err := peerMessageProto(Message{ID: "m-1", From: "child-harp-1", Kind: kind, Body: "hi"})
		require.NoError(t, err, "mail kind %q must project", kind)
		assert.Equal(t, kind, agentcoordpb.LegacyKindName(pm.GetKind()), "typed kind must round-trip for %q", kind)
		assert.Nil(t, pm.GetStructured(), "a message with no companion must not grow one to carry %q", kind)
	}

	pm, err := peerMessageProto(Message{
		ID: "m-2", From: "child-harp-1", Kind: KindResult, Body: "hi",
		Structured: json.RawMessage(`{"kind":"approval_request","answer":"yes"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, agentcoordpb.MessageKind_MESSAGE_KIND_RESULT, pm.GetKind())
	assert.Equal(t, map[string]any{"kind": "approval_request", "answer": "yes"}, pm.GetStructured().AsMap(),
		"the sender's companion travels untouched: its kind key is inert, not overwritten")

	_, err = peerMessageProto(Message{ID: "m-3", From: "child-harp-1", Kind: "a_kind_nobody_mapped", Body: "hi"})
	require.Error(t, err, "a kind outside the closed vocabulary must not be pushed as UNSPECIFIED")
}
