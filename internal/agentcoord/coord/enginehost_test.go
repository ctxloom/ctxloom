package coord

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// fakeEngineHome records everything the engine host emits, standing in for a
// dialed Home.
type fakeEngineHome struct {
	mu      sync.Mutex
	events  []*agentcoordpb.AgentEvent
	customs []struct {
		Name  string
		Value map[string]any
	}
	sink        func(*agentcoordpb.PeerMessage) bool
	spoolSweeps int
	exited []struct {
		Code      int
		SessionID string
	}
	seq uint64

	// requests (C2) records every plane-2 AgentRequest the engine host
	// sent; requestFn scripts the response (nil = a canned DECLINE — safe
	// default a test that doesn't care about approvals never trips over).
	requests  []*agentcoordpb.AgentRequest
	requestFn func(*agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error)

	// ctrlHandler is whatever BindHome registered, and parked is every control
	// body the host asked to park — the two halves of plane 2's down direction
	// as seen from the Home seam.
	ctrlHandler func(context.Context, *agentcoordpb.CoordinatorRequest) *agentcoordpb.AgentResponse
	parked      []*agentcoordpb.PeerMessage
	// pendingCtl mirrors Home's unpulled-control ledger. It is the state the
	// turn-boundary re-announcer reads, so the fake must model it (and let a
	// test age or drain it) rather than pretend every parked body is pulled.
	pendingCtl []PendingControlPayload
	// turnReports records every automatic turn report the engine host composed
	// at a boundary (ReportTurnResult) — the runner half of the result plane.
	turnReports []turnReport
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
		Status: okStatus(""),
		Kind: &agentcoordpb.CoordinatorResponse_Approval{Approval: &agentcoordpb.ApprovalDecision{
			Decision: agentcoordpb.ApprovalDecision_DECISION_DECLINE, Note: "fakeEngineHome default",
		}},
	}, nil
}

func (f *fakeEngineHome) requestedApprovals() []*agentcoordpb.ApprovalRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*agentcoordpb.ApprovalRequest
	for _, r := range f.requests {
		if a := r.GetApproval(); a != nil {
			out = append(out, a)
		}
	}
	return out
}

// interactions returns every InteractionRecorded the host journaled, in emit
// order — the same accessor shape as requestedApprovals, so a test can assert
// what the RESOLUTION journal says without hand-rolling the lock + type switch
// at each call site.
func (f *fakeEngineHome) interactions() []*agentcoordpb.InteractionRecorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*agentcoordpb.InteractionRecorded
	for _, ev := range f.events {
		if ir := ev.GetInteraction(); ir != nil {
			out = append(out, ir)
		}
	}
	return out
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

func (f *fakeEngineHome) SetRequestHandler(fn func(context.Context, *agentcoordpb.CoordinatorRequest) *agentcoordpb.AgentResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ctrlHandler != nil {
		return
	}
	f.ctrlHandler = fn
}

func (f *fakeEngineHome) ParkControlPayload(pm *agentcoordpb.PeerMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.parked = append(f.parked, pm)
	f.pendingCtl = append(f.pendingCtl, PendingControlPayload{MessageID: pm.GetMessageId(), ParkedAt: time.Now()})
}

func (f *fakeEngineHome) PendingControlPayloads() []PendingControlPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PendingControlPayload(nil), f.pendingCtl...)
}

// pullControlPayload is the fake's agent_recv: the agent pulled id, so it stops
// being pending.
func (f *fakeEngineHome) pullControlPayload(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.pendingCtl {
		if p.MessageID == id {
			f.pendingCtl = append(f.pendingCtl[:i], f.pendingCtl[i+1:]...)
			return
		}
	}
}

// backdateControlPayloads moves every pending body's park time d into the past,
// so a test can age an instruction without waiting for a wall clock.
func (f *fakeEngineHome) backdateControlPayloads(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.pendingCtl {
		f.pendingCtl[i].ParkedAt = f.pendingCtl[i].ParkedAt.Add(-d)
	}
}

// customValues snapshots the recorded custom events matching name.
func (f *fakeEngineHome) customValues(name string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.customs {
		if c.Name == name {
			out = append(out, c.Value)
		}
	}
	return out
}

// parkedBodies snapshots the control bodies parked for agent_recv.
func (f *fakeEngineHome) parkedBodies() []*agentcoordpb.PeerMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*agentcoordpb.PeerMessage(nil), f.parked...)
}

// control runs whatever BindHome registered, so a test drives the executor
// through the same seam a coordinator frame would.
func (f *fakeEngineHome) control(ctx context.Context, req *agentcoordpb.CoordinatorRequest) *agentcoordpb.AgentResponse {
	f.mu.Lock()
	fn := f.ctrlHandler
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, req)
}

func (f *fakeEngineHome) ReportRunExited(code int, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exited = append(f.exited, struct {
		Code      int
		SessionID string
	}{code, sessionID})
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
		out = append(out, itemKind(ev))
	}
	return out
}

// scriptedChat is a StructuredChat whose turns are scripted: each received
// text yields a Session event (first turn only), a thinking entry, an
// assistant echo, a tool_use/tool_result pair, and a Complete.
type scriptedChat struct {
	mu       sync.Mutex
	requests []agent.ChatRequest
	texts    []string
	turnGate chan struct{} // non-nil: turns block until released

	// permission (C2), when set, is forwarded as ChatEvent.Permission
	// before the turn's normal entries — the turn parks until the matching
	// ChatMessage.Permission answer arrives, and answers records it.
	permission *agent.PermissionRequest
	answersMu  sync.Mutex
	answers    []agent.PermissionAnswer
	// resumable scripts the session event's ChatSessionInfo.Resumable — the
	// live loadSession capability a real ACP engine advertises (Slice 4 piece
	// 1). A one-shot child tears down at the turn boundary only when this is
	// true (live-confirmed), so a one-shot test must set it.
	resumable bool
}

func (s *scriptedChat) recordedAnswers() []agent.PermissionAnswer {
	s.answersMu.Lock()
	defer s.answersMu.Unlock()
	return append([]agent.PermissionAnswer(nil), s.answers...)
}

// arm (re-)schedules a permission request to forward on the NEXT turn this
// scripted chat receives (C2 tests: scripting a second approval ask after
// the first one resolved).
func (s *scriptedChat) arm(pr *agent.PermissionRequest) {
	s.mu.Lock()
	s.permission = pr
	s.mu.Unlock()
}

func (s *scriptedChat) recordedTexts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.texts...)
}

func (s *scriptedChat) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)
	s.mu.Lock()
	s.requests = append(s.requests, req)
	gate := s.turnGate
	s.mu.Unlock()
	send := func(ev agent.ChatEvent) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: req.Model, SessionID: "native-sess-42", Resumable: s.resumable}}) {
		return ctx.Err()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				return nil
			}
			if msg.Permission != nil {
				continue // a stray answer with no pending request in this fixture
			}
			if msg.Text == "" {
				continue
			}
			s.mu.Lock()
			s.texts = append(s.texts, msg.Text)
			pr := s.permission
			s.permission = nil // forward at most once, on the first matching turn
			s.mu.Unlock()
			if gate != nil {
				select {
				case <-gate:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if pr != nil {
				if !send(agent.ChatEvent{Permission: pr}) {
					return ctx.Err()
				}
				if !s.awaitPermissionAnswer(ctx, in, pr.ID) {
					return ctx.Err()
				}
			}
			if !send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeThinking, Content: "pondering"}}) ||
				!send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "echo: " + msg.Text}}) ||
				!send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "grep", ToolInput: []byte(`{"q":"x"}`)}}) ||
				!send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolOutput: "found"}}) ||
				!send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn", InputTokens: 10, CostUSD: 0.0000015}}) {
				return ctx.Err()
			}
		}
	}
}

// awaitPermissionAnswer parks until a ChatMessage.Permission answering id
// arrives on in, recording it (recordedAnswers). Returns false on ctx death
// or in closing (the caller treats either as a fatal stream end, matching
// the rest of this fixture's send() convention).
func (s *scriptedChat) awaitPermissionAnswer(ctx context.Context, in <-chan agent.ChatMessage, id string) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case msg, ok := <-in:
			if !ok {
				return false
			}
			if msg.Permission != nil && msg.Permission.ID == id {
				s.answersMu.Lock()
				s.answers = append(s.answers, *msg.Permission)
				s.answersMu.Unlock()
				return true
			}
		}
	}
}

func testStartRun(runID string) *agentcoordpb.StartRun {
	spec, err := buildHarnessSpec(HarnessSpecInput{
		Harness:     "claude-code",
		Model:       "claude-sonnet-5",
		Workspace:   "/work",
		SessionHarp: "child-harp-1",
		Permission:  agent.PermissionBypass,
	})
	if err != nil {
		panic(err)
	}
	input, _ := structpb.NewStruct(map[string]any{"prompt": "CTX\n\ndo the thing"})
	return &agentcoordpb.StartRun{RunId: runID, Harness: spec, Input: input, Role: "worker"}
}

// TestEngineHost_StartRunDrivesChatInProcess pins the whole runner half of
// C1: StartRun decodes the spec, launches the backend's Chat IN-PROCESS, the
// briefing rides the first turn, native events adapt onto plane-1
// (RunStarted → turn_started → message/tool items → turn_idle), and the
// native session id is reported via the harness_session custom event.
func TestEngineHost_StartRunDrivesChatInProcess(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)

	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), "StartRun must succeed: %s", resp.GetStatus().GetMessage())
	require.NotNil(t, resp.GetStartRun())
	assert.NotZero(t, resp.GetStartRun().GetPid())

	// The briefing (context pre-joined) is the first turn, verbatim.
	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "CTX\n\ndo the thing", sc.recordedTexts()[0])

	// The turn's native events adapted onto plane-1.
	require.Eventually(t, func() bool {
		for _, n := range home.customNames() {
			if n == CustomTurnIdle {
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
	assert.Contains(t, names, CustomHarnessSession, "the native session id reaches the coordinator's journal path")
	assert.Contains(t, names, CustomTurnStarted)

	// The chat request the backend saw matches the decoded spec.
	sc.mu.Lock()
	req := sc.requests[0]
	sc.mu.Unlock()
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
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)

	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), "StartRun must succeed: %s", resp.GetStatus().GetMessage())

	require.Eventually(t, func() bool {
		for _, n := range home.customNames() {
			if n == CustomTurnIdle {
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

// TestEngineHost_StartRun_NoHarpSkipsCaptureGracefully pins the defensive
// degrade-gracefully branch: if a StartRun somehow arrives with no
// SessionHarp in its HarnessSpec (not expected in production — the
// coordinator always assigns one before issuing StartRun — but defensive per
// the plan's "no crash" requirement), the engine still runs and adapts
// normally; it simply writes no transcript.
func TestEngineHost_StartRun_NoHarpSkipsCaptureGracefully(t *testing.T) {
	testsupport.Isolate(t)
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)

	spec, err := buildHarnessSpec(HarnessSpecInput{
		Harness:    "claude-code",
		Model:      "claude-sonnet-5",
		Workspace:  "/work",
		Permission: agent.PermissionBypass,
		// SessionHarp deliberately omitted.
	})
	require.NoError(t, err)
	input, _ := structpb.NewStruct(map[string]any{"prompt": "no harp here"})
	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{
		StartRun: &agentcoordpb.StartRun{RunId: "run-1", Harness: spec, Input: input, Role: "worker"},
	}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode())

	require.Eventually(t, func() bool {
		for _, n := range home.customNames() {
			if n == CustomTurnIdle {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "adapt must still process the turn normally with no harp")

	// No harp was ever assigned to this run, so NO canonical transcript file
	// should exist anywhere under the isolated home's sessions tree — the
	// real assertion that capture was skipped outright, not attempted and
	// silently swallowed.
	sessionsDir, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	found := false
	_ = filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr == nil && !d.IsDir() && d.Name() == paths.CanonicalTranscriptFileName {
			found = true
		}
		return nil
	})
	assert.False(t, found, "no harp on the StartRun must produce no transcript.jsonl anywhere")
}

// TestEngineHost_TurnSinkDeliversFramedMail: a coordinator-pushed
// PeerMessage lands on the engine as a NEW TURN, framed with sender + kind
// (manly-grant (6)).
func TestEngineHost_TurnSinkDeliversFramedMail(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode())
	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 1 }, 5*time.Second, 10*time.Millisecond)

	home.mu.Lock()
	sink := home.sink
	home.mu.Unlock()
	require.NotNil(t, sink, "startRun must register the turn sink")

	structured, _ := structpb.NewStruct(map[string]any{"kind": KindResult})
	ok := sink(&agentcoordpb.PeerMessage{MessageId: "m-9", FromAgentId: "parent-harp", Text: "next assignment", Structured: structured})
	require.True(t, ok)
	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 2 }, 5*time.Second, 10*time.Millisecond)
	got := sc.recordedTexts()[1]
	assert.Contains(t, got, "[coordinator-delivered message from=parent-harp kind=result]")
	assert.Contains(t, got, "next assignment")

	// A kind OUTSIDE the closed vocabulary is not interpolated into the header:
	// the value used to be rendered straight from the sender's own structured
	// payload, which is the forgery surface, so only closed-set names render.
	offVocab, _ := structpb.NewStruct(map[string]any{"kind": "task"})
	require.True(t, sink(&agentcoordpb.PeerMessage{MessageId: "m-10", FromAgentId: "parent-harp", Text: "another", Structured: offVocab}))
	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 3 }, 5*time.Second, 10*time.Millisecond)
	assert.NotContains(t, sc.recordedTexts()[2], "kind=")
}

// TestEngineHost_StartRunIdempotentOnReissue: the SAME run_id reissued
// (reconnect) returns the cached result; a DIFFERENT run is refused
// (max_concurrent_runs=1), and a mismatched A9 correlation is refused.
func TestEngineHost_StartRunIdempotentOnReissue(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
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
	eh := NewEngineHost(ctx, sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode())
	require.Eventually(t, func() bool {
		for _, n := range home.customNames() {
			if n == CustomTurnIdle {
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

// TestEngineHost_ForwardsPermissionAsApprovalRequest is the C2 runner-side
// unit test, isolated from a real coordinator: a forwarded engine
// permission request must (1) build an ApprovalRequest with the ACP tool
// kind classified onto the contract's ApprovalKind, the title, and the raw
// tool input as the payload; (2) translate the scripted ApprovalDecision
// back into the matching PermissionOption; (3) journal an InteractionRecorded
// item whose Detail carries the decision/note — the content the
// approval_test.go integration tests cannot see directly (items are counted,
// not materialized, on the coordinator's own journal).
func TestEngineHost_ForwardsPermissionAsApprovalRequest(t *testing.T) {
	home := &fakeEngineHome{}
	home.requestFn = func(req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
		return &agentcoordpb.CoordinatorResponse{
			Status: okStatus(""),
			Kind: &agentcoordpb.CoordinatorResponse_Approval{Approval: &agentcoordpb.ApprovalDecision{
				Decision: agentcoordpb.ApprovalDecision_DECISION_ACCEPT,
				Note:     "rung 2/2: relay_to_role granted",
			}},
		}, nil
	}
	sc := &scriptedChat{permission: &agent.PermissionRequest{
		ID: "perm-9", ToolName: "bash", Kind: "execute", ToolInput: []byte(`{"command":"ls"}`),
		Options: []agent.PermissionOption{
			{ID: "allow-1", Kind: "allow_once"},
			{ID: "reject-1", Kind: "reject_once"},
		},
	}}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode())

	// The engine received the matching allow option.
	require.Eventually(t, func() bool { return len(sc.recordedAnswers()) == 1 }, 5*time.Second, 10*time.Millisecond)
	ans := sc.recordedAnswers()[0]
	assert.Equal(t, "perm-9", ans.ID)
	assert.Equal(t, "allow-1", ans.OptionID)

	// The coordinator saw an ApprovalRequest with the classified kind, the
	// title, and the tool input as payload.
	require.Eventually(t, func() bool { return len(home.requestedApprovals()) == 1 }, 5*time.Second, 10*time.Millisecond)
	approval := home.requestedApprovals()[0]
	assert.Equal(t, agentcoordpb.ApprovalRequest_APPROVAL_KIND_COMMAND_EXECUTION, approval.GetKind())
	assert.Equal(t, "bash", approval.GetTitle())
	assert.Equal(t, "perm-9", approval.GetItemId())
	require.NotNil(t, approval.GetPayload())
	assert.Equal(t, "ls", approval.GetPayload().GetFields()["command"].GetStringValue())

	// The InteractionRecorded item journaled the resolution's detail.
	require.Eventually(t, func() bool {
		for _, ev := range home.events {
			if ev.GetInteraction() != nil {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "an InteractionRecorded event must be emitted")
	home.mu.Lock()
	defer home.mu.Unlock()
	var interaction *agentcoordpb.InteractionRecorded
	for _, ev := range home.events {
		if ir := ev.GetInteraction(); ir != nil {
			interaction = ir
		}
	}
	require.NotNil(t, interaction)
	assert.Equal(t, "approval", interaction.GetKind())
	assert.Equal(t, agentcoordpb.InteractionRecorded_RESOLUTION_GRANTED, interaction.GetResolution())
	assert.Equal(t, "DECISION_ACCEPT", interaction.GetDetail().GetFields()["decision"].GetStringValue())
	assert.Contains(t, interaction.GetDetail().GetFields()["note"].GetStringValue(), "rung 2/2")
}

// TestEngineHost_ApprovalDeclineCancels pins the DECLINE/CANCEL translation
// for a decision a REAL ANSWERER made: a DECLINE picks a reject option and
// journals DENIED; a CANCEL picks NO option at all (the engine's safe
// cancelled no-op — neither approves nor commits a remembered rejection) and
// journals CANCELLED.
//
// The decline arm is the boundary TestEngineHost_NoAnswererCancels must never
// erode: "nobody decided" answers cancelled, but a genuine decline — a human,
// or an auto_decline rung — still says "refused", because it really was.
func TestEngineHost_ApprovalDeclineCancels(t *testing.T) {
	for _, tc := range []struct {
		name           string
		decision       agentcoordpb.ApprovalDecision_Decision
		wantOption     string
		wantResolution agentcoordpb.InteractionRecorded_Resolution
	}{
		{name: "decline", decision: agentcoordpb.ApprovalDecision_DECISION_DECLINE, wantOption: "reject-1", wantResolution: agentcoordpb.InteractionRecorded_RESOLUTION_DENIED},
		{name: "cancel", decision: agentcoordpb.ApprovalDecision_DECISION_CANCEL, wantOption: "", wantResolution: agentcoordpb.InteractionRecorded_RESOLUTION_CANCELLED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := &fakeEngineHome{}
			home.requestFn = func(*agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
				return &agentcoordpb.CoordinatorResponse{
					Status: okStatus(""),
					Kind:   &agentcoordpb.CoordinatorResponse_Approval{Approval: &agentcoordpb.ApprovalDecision{Decision: tc.decision}},
				}, nil
			}
			sc := &scriptedChat{permission: &agent.PermissionRequest{
				ID: "perm-x", ToolName: "bash", Kind: "execute",
				Options: []agent.PermissionOption{
					{ID: "allow-1", Kind: "allow_once"},
					{ID: "reject-1", Kind: "reject_once"},
				},
			}}
			eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
			t.Cleanup(eh.Close)
			eh.BindHome(home)
			resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
			require.Equal(t, int32(0), resp.GetStatus().GetCode())

			require.Eventually(t, func() bool { return len(sc.recordedAnswers()) == 1 }, 5*time.Second, 10*time.Millisecond)
			assert.Equal(t, tc.wantOption, sc.recordedAnswers()[0].OptionID)

			require.Eventually(t, func() bool { return len(home.interactions()) == 1 }, 5*time.Second, 10*time.Millisecond,
				"an InteractionRecorded event must be emitted")
			assert.Equal(t, tc.wantResolution, home.interactions()[0].GetResolution(),
				"the journal must record what the answerer actually decided")
		})
	}
}

// TestEngineHost_NoAnswererCancels is wiry-judge's runner-side half: when the
// coordinator refuses or fails to produce a decision AT ALL — the
// !caller.IsChild() guard's PermissionDenied, an unknown-run NotFound, or a
// transport failure — NO real answerer decided anything, so the engine must be
// told the request was CANCELLED (empty OptionID -> ACP {outcome:"cancelled"}),
// never handed a reject_once option.
//
// The defect this replaces: pickPermissionOption(options, allow=false) picked
// "reject-1" on every one of these paths, which claude-code-acp renders to the
// model — and to the durable transcript — as {behavior:"deny", message:"User
// refused permission to run tool"}. A refusal ctxloom itself made, falsely
// attributed to the operator. "Nobody decided" is not "the user said no".
//
// The JOURNAL half matters as much as the wire half: a non-OK status resolves
// as CANCELLED so the InteractionRecorded item agrees with the answer the
// engine actually got. The transport-error arm keeps TIMED_OUT — that one is
// honest about WHY nobody decided, and is a different fact from a refusal.
func TestEngineHost_NoAnswererCancels(t *testing.T) {
	for _, tc := range []struct {
		name           string
		respond        func(*agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error)
		wantResolution agentcoordpb.InteractionRecorded_Resolution
	}{
		{
			// The owned-run / non-child guard: serveApproval's first
			// statement, refusing before any ladder rung runs.
			name: "guard_refusal",
			respond: func(*agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
				return &agentcoordpb.CoordinatorResponse{
					Status: statusErr(codes.PermissionDenied, "approval requests: only a delegated child's run asks the coordinator for a decision"),
				}, nil
			},
			wantResolution: agentcoordpb.InteractionRecorded_RESOLUTION_CANCELLED,
		},
		{
			// An unknown run id: the coordinator has no record to walk a
			// ladder for, so again nobody decided.
			name: "unknown_run",
			respond: func(*agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
				return &agentcoordpb.CoordinatorResponse{
					Status: statusErr(codes.NotFound, `approval: unknown run "run-gone"`),
				}, nil
			},
			wantResolution: agentcoordpb.InteractionRecorded_RESOLUTION_CANCELLED,
		},
		{
			// The request never reached an answerer at all.
			name: "transport_error",
			respond: func(*agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
				return nil, errors.New("plane 2: stream closed")
			},
			wantResolution: agentcoordpb.InteractionRecorded_RESOLUTION_TIMED_OUT,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := &fakeEngineHome{}
			home.requestFn = tc.respond
			sc := &scriptedChat{permission: &agent.PermissionRequest{
				ID: "perm-x", ToolName: "bash", Kind: "execute",
				Options: []agent.PermissionOption{
					{ID: "allow-1", Kind: "allow_once"},
					{ID: "reject-1", Kind: "reject_once"},
				},
			}}
			eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
			t.Cleanup(eh.Close)
			eh.BindHome(home)
			resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
			require.Equal(t, int32(0), resp.GetStatus().GetCode())

			require.Eventually(t, func() bool { return len(sc.recordedAnswers()) == 1 }, 5*time.Second, 10*time.Millisecond,
				"the engine must still be answered — a refusal is never a hang")
			ans := sc.recordedAnswers()[0]
			assert.Equal(t, "perm-x", ans.ID)
			assert.Empty(t, ans.OptionID,
				"no answerer decided, so the engine is told CANCELLED — never handed a reject option that reads as the operator's refusal")

			require.Eventually(t, func() bool { return len(home.interactions()) == 1 }, 5*time.Second, 10*time.Millisecond,
				"an InteractionRecorded event must be emitted")
			ir := home.interactions()[0]
			assert.Equal(t, tc.wantResolution, ir.GetResolution(),
				"the journal must agree with the answer the engine actually received")
			assert.Empty(t, ir.GetDetail().GetFields()["option_id"].GetStringValue(),
				"the journaled option_id is the one that crossed the wire")
		})
	}
}
