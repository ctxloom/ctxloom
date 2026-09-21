package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/textblocks"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// fakeEngineHome records everything the engine host emits, standing in for a
// dialed Home.
type fakeEngineHome struct {
	mu       sync.Mutex
	identity coord.Identity
	events   []*agentcoordpb.AgentEvent
	customs  []struct {
		Name  string
		Value map[string]any
	}
	sink        func(*agentcoordpb.PeerMessage) bool
	spoolSweeps int
	exited      []struct {
		Code      int
		SessionID string
	}
	seq uint64

	// requests (C2) records every plane-2 AgentRequest the engine host
	// sent; requestFn scripts the response (nil = a canned DECLINE — safe
	// default a test that doesn't care about approvals never trips over).
	requests  []*agentcoordpb.AgentRequest
	requestFn func(*agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error)

	// turnReports records every automatic turn report the engine host composed
	// at a boundary (ReportTurnResult) — the runner half of the result plane.
	turnReports []turnReport

	// awaitedAcks and lifecycle record the exit ordering: which delivered
	// turns' consume-acks the host waited for, and whether that wait came
	// before the RunExited report.
	awaitedAcks []string
	lifecycle   []string
}

func (f *fakeEngineHome) Request(_ context.Context, req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	fn := f.requestFn
	f.mu.Unlock()
	if fn != nil {
		return fn(req)
	}
	return &agentcoordpb.CoordinatorResponse{
		Status: coord.OKStatus(""),
		Kind: &agentcoordpb.CoordinatorResponse_Approval{Approval: &agentcoordpb.ApprovalDecision{
			Decision: agentcoordpb.ApprovalDecision_DECISION_DECLINE, Note: "fakeEngineHome default",
		}},
	}, nil
}

// BindIdentity records the identity the host bound from the launch.
func (f *fakeEngineHome) BindIdentity(id coord.Identity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identity = id
}

func (f *fakeEngineHome) emitEvent(ev *agentcoordpb.AgentEvent) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.events = append(f.events, ev)
	return f.seq
}

func (f *fakeEngineHome) emitCustomEvent(name string, value map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.customs = append(f.customs, struct {
		Name  string
		Value map[string]any
	}{name, value})
}

func (f *fakeEngineHome) SetTurnSink(sink func(*agentcoordpb.PeerMessage) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sink = sink
}

// SweepSpoolIn records that the engine host asked for a spool reconciliation
// at a turn boundary. Counted rather than ignored: the file plane's whole
// boundary drain hangs off this one call, and a silently-dropped fake would
// let a refactor delete the trigger with every test still green.
func (f *fakeEngineHome) SweepSpoolIn() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spoolSweeps++
}

// ReportTurnResult records the automatic turn report the engine host composed
// at a boundary — the text AND the correlation, because the correlation is
// half of what this report is for and a fake that swallowed it would let the
// tag plumbing rot with every test still green.
func (f *fakeEngineHome) ReportTurnResult(text, inReplyTo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.turnReports = append(f.turnReports, turnReport{Text: text, InReplyTo: inReplyTo})
	return nil
}

// turnReportsSeen snapshots what the host reported, oldest first.
func (f *fakeEngineHome) turnReportsSeen() []turnReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]turnReport(nil), f.turnReports...)
}

// turnReport is one composed automatic report as the Home seam saw it.
type turnReport struct {
	Text      string
	InReplyTo string
}

// spoolSweepCount reports how many boundary sweeps were asked for.
func (f *fakeEngineHome) spoolSweepCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spoolSweeps
}

func (f *fakeEngineHome) ReportRunExited(code int, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exited = append(f.exited, struct {
		Code      int
		SessionID string
	}{code, sessionID})
	f.lifecycle = append(f.lifecycle, "exited")
}

// AwaitMailAcked records which delivered turns the engine host waited on
// before reporting its exit, in lifecycle order against ReportRunExited —
// the ordering is the whole point of the call.
func (f *fakeEngineHome) AwaitMailAcked(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.awaitedAcks = append(f.awaitedAcks, ids...)
	f.lifecycle = append(f.lifecycle, "await-acks")
	return nil
}

func (f *fakeEngineHome) customNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.customNamesLocked()
}

// customNamesLocked is customNames for a caller already holding f.mu.
func (f *fakeEngineHome) customNamesLocked() []string {
	out := make([]string, len(f.customs))
	for i, c := range f.customs {
		out[i] = c.Name
	}
	return out
}

func (f *fakeEngineHome) payloadKinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, ev := range f.events {
		out = append(out, coord.EventFromWire(ev).Kind())
	}
	return out
}

func testStartRun(runID string) *agentcoordpb.StartRun {
	l := ownerLaunch("child-harp-1", "claude-code", "fast", "claude-sonnet-5", "/work", agent.PermissionBypass)
	l.Identity.RunID = runID
	l.Identity.Depth = 1
	l.Prompt = "CTX\n\ndo the thing"
	return &agentcoordpb.StartRun{RunId: runID, Launch: coordgrpc.EncodeLaunch(l)}
}

// testRunner is the Runner double the engine-host tests bind: it decodes
// the wire launch, opens its package, and drives the host with the request
// and the first-turn lead the real runner builds (adapters/runner, which
// this package's tests cannot import), delivering nothing — the tests
// observe the drive.
type testRunner struct {
	eh *EngineHost
	// refuse, when set and true, refuses the launch the way a runner whose
	// endpoint cannot be bound does.
	refuse func() bool
}

func (r testRunner) Execute(ctx context.Context, wire *agentcoordpb.Launch) error {
	l, err := coordgrpc.DecodeLaunch(wire)
	if err != nil {
		return err
	}
	if r.refuse != nil && r.refuse() {
		return delivery.ErrEndpointUnavailable
	}
	pkg, err := composite.Open(ctx, composite.Inline{}, composite.ClaimCheck{Store: launchtest.MemStore{}}, l.Package)
	if err != nil {
		return err
	}
	prompt := l.Prompt
	if l.Resume.NativeKey == "" {
		prompt = textblocks.Join(pkg.Context.Text, l.Prompt)
	}
	return r.eh.Drive(ctx, Turn{
		Launch: l,
		Chat:   agent.ChatRequest{WorkDir: l.Cell.Workspace, Model: l.Label.Model, Permissions: l.Permission, ResumeSessionID: l.Resume.NativeKey},
		Prompt: prompt,
	})
}

// newTestEngineHost is NewEngineHost with the test runner bound.
func newTestEngineHost(ctx context.Context, backend agent.StructuredChat, harness, runID string) *EngineHost {
	eh := NewEngineHost(ctx, nil, backend, harness, runID)
	eh.BindRunner(testRunner{eh: eh})
	return eh
}

// TestEngineHost_StartRunDrivesChatInProcess pins the whole runner half of
// C1: StartRun decodes the spec, launches the backend's Chat IN-PROCESS, the
// briefing rides the first turn, native events adapt onto plane-1
// (RunStarted → turn_started → message/tool items → turn_idle), and the
// native session id is reported via the harness_session custom event.
func TestEngineHost_StartRunDrivesChatInProcess(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)

	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), "StartRun must succeed: %s", resp.GetStatus().GetMessage())
	require.NotNil(t, resp.GetStartRun())
	assert.NotZero(t, resp.GetStartRun().GetPid())

	// The briefing (context pre-joined) is the first turn, verbatim.
	require.Eventually(t, func() bool { return len(sc.RecordedTexts()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "CTX\n\ndo the thing", sc.RecordedTexts()[0])

	// The turn's native events adapted onto plane-1.
	require.Eventually(t, func() bool {
		for _, n := range home.customNames() {
			if n == coord.CustomTurnIdle {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the turn boundary must reach plane-1")

	kinds := home.payloadKinds()
	assert.Contains(t, kinds, "run_started")
	assert.Contains(t, kinds, "message_started")
	assert.Contains(t, kinds, "message_delta")
	assert.Contains(t, kinds, "message_completed")
	assert.Contains(t, kinds, "tool_call_started")
	assert.Contains(t, kinds, "tool_call_completed")
	names := home.customNames()
	assert.Contains(t, names, coord.CustomHarnessSession, "the native session id reaches the coordinator's journal path")
	assert.Contains(t, names, coord.CustomTurnStarted)

	// The automatic turn report is the runner half of the result plane: at the
	// boundary the host hands the parent this turn's FINAL-channel answer AND
	// the correlation of the mail it answers. The briefing is nobody's reply,
	// so its correlation is empty. Both halves are asserted here — a report
	// whose correlation was dropped would still deliver text, and the tag
	// plumbing would rot with every other assertion still green.
	reports := home.turnReportsSeen()
	require.Len(t, reports, 1, "one turn boundary produces exactly one report")
	assert.Equal(t, "echo: CTX\n\ndo the thing", reports[0].Text)
	assert.Empty(t, reports[0].InReplyTo, "the briefing turn answers no mail")

	// The boundary sweep is dispatched AFTER the idle event, so it is waited
	// for rather than assumed to have already run.
	require.Eventually(t, func() bool { return home.spoolSweepCount() == 1 }, 5*time.Second, 10*time.Millisecond,
		"the turn boundary must ask for one spool sweep")

	// The chat request the backend saw matches the decoded spec.
	sc.Mu.Lock()
	req := sc.Requests[0]
	sc.Mu.Unlock()
	assert.Equal(t, "/work", req.WorkDir)
	assert.Equal(t, "claude-sonnet-5", req.Model)
	assert.Equal(t, agent.PermissionBypass, req.Permissions)
}

// readCanonicalTranscript reads back harp's canonical transcript file
// (paths.HarpCanonicalTranscriptPath) into transcript.Record values, in file
// order. Mirrors the same small helper internal/lm/grpc's chat_test.go uses
// for its own S2 seam — Record's fields are exported, so each package reads
// the file directly rather than sharing a test-only helper across packages.
func readCanonicalTranscript(t *testing.T, harp string) []transcript.Record {
	t.Helper()
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	var recs []transcript.Record
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r transcript.Record
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		recs = append(recs, r)
	}
	require.NoError(t, scanner.Err())
	return recs
}

// TestEngineHost_StartRun_CapturesTranscript pins the tough-cloud S2 seam on
// the delegated-child side: enginehost.startRun tees the in-process
// backend.Chat stream (dec.SessionHarp="child-harp-1", eh.harness=
// "claude-code" — see testStartRun/NewEngineHost above) into the child's own
// canonical transcript BEFORE adapt ever sees an event, so by the time the
// turn's completion has reached plane-1 (home's turn_idle custom event), the
// same events must already be on disk, in order, unaltered.
func TestEngineHost_StartRun_CapturesTranscript(t *testing.T) {
	testsupport.Isolate(t)
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)

	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), "StartRun must succeed: %s", resp.GetStatus().GetMessage())

	require.Eventually(t, func() bool {
		for _, n := range home.customNames() {
			if n == coord.CustomTurnIdle {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the turn boundary must reach plane-1")

	// adapt still did its normal job — capture must be a pure tee, not a
	// substitute consumer.
	kinds := home.payloadKinds()
	assert.Contains(t, kinds, "message_started")
	assert.Contains(t, kinds, "tool_call_started")

	// testStartRun's HarnessSpec carries SessionHarp "child-harp-1"; the
	// briefing prompt is recorded first (edgy-ivory: eagerly, before
	// backend.Chat is even dispatched — see startRun), then the scriptedChat
	// backend for one turn emits session, thinking, assistant, tool_use,
	// tool_result, complete — seven events, seven recorded lines.
	recs := readCanonicalTranscript(t, "child-harp-1")
	require.Len(t, recs, 7)
	for _, r := range recs {
		assert.Equal(t, "child-harp-1", r.Harp)
		assert.Equal(t, "claude-code", r.Engine, "engine must be eh.harness, the RunnerHello-advertised backend name")
	}

	require.NotNil(t, recs[0].Entry)
	assert.Equal(t, "user", recs[0].Entry.Type)
	assert.Equal(t, "CTX\n\ndo the thing", recs[0].Entry.Content, "the briefing prompt must be recorded as a user entry (edgy-ivory)")

	assert.Equal(t, transcript.KindSession, recs[1].Kind)
	assert.Equal(t, "native-sess-42", recs[1].SessionID, "the native ACP session id from ChatEvent.Session must be recorded")

	require.NotNil(t, recs[2].Entry)
	assert.Equal(t, "thinking", recs[2].Entry.Type)
	assert.Equal(t, "pondering", recs[2].Entry.Content)

	require.NotNil(t, recs[3].Entry)
	assert.Equal(t, "assistant", recs[3].Entry.Type)
	assert.Equal(t, "echo: CTX\n\ndo the thing", recs[3].Entry.Content)

	require.NotNil(t, recs[4].Entry)
	assert.Equal(t, "tool_use", recs[4].Entry.Type)
	assert.Equal(t, "grep", recs[4].Entry.ToolName)
	assert.Contains(t, string(recs[4].Entry.ToolInput), "\"q\":\"x\"")

	require.NotNil(t, recs[5].Entry)
	assert.Equal(t, "tool_result", recs[5].Entry.Type)
	assert.Equal(t, "found", recs[5].Entry.ToolOutput)

	require.NotNil(t, recs[6].Complete)
	assert.Equal(t, "end_turn", recs[6].Complete.StopReason)
}

// TestEngineHost_StartRun_WithoutAHarpIsRefused: identity arrives ONCE, on
// the launch, and a launch naming no session is refused by the codec before
// anything is delivered or driven — never run harpless with capture
// silently skipped.
func TestEngineHost_StartRun_WithoutAHarpIsRefused(t *testing.T) {
	testsupport.Isolate(t)
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)

	l := ownerLaunch("", "claude-code", "fast", "claude-sonnet-5", "/work", agent.PermissionBypass)
	l.Prompt = "no harp here"
	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{
		StartRun: &agentcoordpb.StartRun{RunId: "run-1", Launch: coordgrpc.EncodeLaunch(l)},
	}})
	require.Equal(t, int32(codes.InvalidArgument), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.Empty(t, home.customNames(), "nothing was driven")
}

// TestEngineHost_TurnSinkDeliversFramedMail: a coordinator-pushed
// PeerMessage lands on the engine as a NEW TURN, framed with sender + kind
// (manly-grant (6)).
func TestEngineHost_TurnSinkDeliversFramedMail(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode())
	require.Eventually(t, func() bool { return len(sc.RecordedTexts()) == 1 }, 5*time.Second, 10*time.Millisecond)

	home.mu.Lock()
	sink := home.sink
	home.mu.Unlock()
	require.NotNil(t, sink, "startRun must register the turn sink")

	ok := sink(&agentcoordpb.PeerMessage{MessageId: "m-9", FromAgentId: "parent-harp", Text: "next assignment", Kind: agentcoordpb.MessageKind_MESSAGE_KIND_RESULT})
	require.True(t, ok)
	require.Eventually(t, func() bool { return len(sc.RecordedTexts()) == 2 }, 5*time.Second, 10*time.Millisecond)
	got := sc.RecordedTexts()[1]
	assert.Contains(t, got, "[coordinator-delivered message from=parent-harp kind=result]")
	assert.Contains(t, got, "next assignment")

	// A kind OUTSIDE the closed vocabulary is not interpolated into the header.
	// proto3 enums are open on the wire, so a number this build does not
	// declare survives decoding; it renders NO kind rather than a guess.
	require.True(t, sink(&agentcoordpb.PeerMessage{MessageId: "m-10", FromAgentId: "parent-harp", Text: "another", Kind: agentcoordpb.MessageKind(99)}))
	require.Eventually(t, func() bool { return len(sc.RecordedTexts()) == 3 }, 5*time.Second, 10*time.Millisecond)
	assert.NotContains(t, sc.RecordedTexts()[2], "kind=")
}

// TestEngineHost_ExitWaitsForDeliveredTurnsToBeAcked pins the runner side of
// the at-least-once seam: a delivered message's consume-ack (Home.turnPump,
// after the engine accepted the turn) races the engine's own exit — an engine
// that exits on the turn it just accepted can have its RunExited reach the
// coordinator BEFORE the file is renamed consumed, and the coordinator's
// leftover-mail tail then relaunches the harp for a message that is already
// answered. The host therefore waits for every accepted turn's ack before it
// reports the exit, and only for the turns the engine actually took.
func TestEngineHost_ExitWaitsForDeliveredTurnsToBeAcked(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{EndAfterTurns: 2} // the briefing, then the delivered turn, then exit
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode())
	require.Eventually(t, func() bool { return len(sc.RecordedTexts()) == 1 }, 5*time.Second, 10*time.Millisecond)

	home.mu.Lock()
	sink := home.sink
	home.mu.Unlock()
	require.True(t, sink(&agentcoordpb.PeerMessage{MessageId: "m-9", FromAgentId: "parent-harp", Text: "last assignment"}))

	require.Eventually(t, func() bool {
		home.mu.Lock()
		defer home.mu.Unlock()
		return len(home.exited) == 1
	}, 5*time.Second, 10*time.Millisecond, "the engine ends after the delivered turn and the host reports the exit")

	home.mu.Lock()
	defer home.mu.Unlock()
	assert.Equal(t, []string{"m-9"}, home.awaitedAcks, "exactly the delivered turn the engine took — the briefing carried no mail")
	assert.Equal(t, []string{"await-acks", "exited"}, home.lifecycle, "the ack wait must come BEFORE the exit report, or it protects nothing")
}

// TestEngineHost_StartRunIdempotentOnReissue: the SAME run_id reissued
// (reconnect) returns the cached result; a DIFFERENT run is refused
// (max_concurrent_runs=1), and a mismatched A9 correlation is refused.
func TestEngineHost_StartRunIdempotentOnReissue(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)

	mismatch := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-OTHER")}})
	assert.NotEqual(t, int32(0), mismatch.GetStatus().GetCode(), "A9: a run this runner was not spawned for is refused")

	first := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), first.GetStatus().GetCode())
	again := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	assert.Same(t, first, again, "a reissued StartRun (same run_id) returns the cached result")
}

// TestEngineHost_ChatEndEmitsRunCompletedAndRunExited: when the engine's
// stream ends, the adapter emits the terminal RunCompleted (usage in
// micro-USD) and reports RunExited with the native session id.
func TestEngineHost_ChatEndEmitsRunCompletedAndRunExited(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	ctx, cancel := context.WithCancel(context.Background())
	eh := newTestEngineHost(ctx, sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode())
	require.Eventually(t, func() bool {
		for _, n := range home.customNames() {
			if n == coord.CustomTurnIdle {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)

	cancel() // kill the engine: Chat returns, the stream closes

	require.Eventually(t, func() bool {
		home.mu.Lock()
		defer home.mu.Unlock()
		return len(home.exited) == 1
	}, 5*time.Second, 10*time.Millisecond, "chat end must report RunExited on the lifecycle link")

	home.mu.Lock()
	defer home.mu.Unlock()
	assert.Equal(t, "native-sess-42", home.exited[0].SessionID)
	var completed *agentcoordpb.RunCompleted
	for _, ev := range home.events {
		if rc := ev.GetRunCompleted(); rc != nil {
			completed = rc
		}
	}
	require.NotNil(t, completed, "the terminal RunCompleted must be emitted")
	require.NotNil(t, completed.GetResult().GetUsage())
	assert.Equal(t, uint64(10), completed.GetResult().GetUsage().GetInputTokens())
	// 0.0000015 USD → 1.5 micros → round-half-even → 2.
	assert.Equal(t, uint64(2), completed.GetResult().GetUsage().GetCostUsdMicros(), "money converts ONCE at the runner boundary, round-half-even")
	assert.Equal(t, uint32(1), completed.GetResult().GetNumTurns())
}
