package coord

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// engineHome is the slice of *Home the engine host consumes — an interface so
// the adaptation logic tests hermetically without a dialed coordinator.
type engineHome interface {
	emitEvent(ev *agentcoordpb.AgentEvent) uint64
	emitCustomEvent(name string, value map[string]any)
	SetTurnSink(sink func(*agentcoordpb.PeerMessage) bool)
	// SweepSpoolIn asks the runner to reconcile its inbound spool — the file
	// plane's turn-boundary drain. A no-op for a run that is not cut over.
	SweepSpoolIn()
	ReportRunExited(exitCode int, harnessSessionID string)
	// SetRequestHandler registers this host as the executor for
	// coordinator-initiated plane-2 control requests (BindHome does it).
	SetRequestHandler(fn func(context.Context, *agentcoordpb.CoordinatorRequest) *agentcoordpb.AgentResponse)
	// ParkControlPayload puts a control request's body where agent_recv finds
	// it, WITHOUT routing it to the turn sink — the control verbs inject their
	// own reminder turn and the agent pulls the body.
	ParkControlPayload(pm *agentcoordpb.PeerMessage)
	// PendingControlPayloads reports the parked bodies the agent has not pulled
	// yet, oldest first — what the turn-boundary re-announcer reads to decide
	// whether an announced instruction is still sitting unread.
	PendingControlPayloads() []PendingControlPayload
	// Request runs one plane-2 request to completion (Home.Request) — the
	// engine host's seam for forwarding a permission request as an
	// ApprovalRequest (Wave C2, resolveApproval below).
	Request(ctx context.Context, req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error)
}

// Compile-time assertion that Home satisfies the engine host's seam.
var _ engineHome = (*Home)(nil)

// homeBindTimeout bounds Handle's wait for BindHome. StartRun can only arrive
// after the Home dialed in (Hello handshake), so in practice the bind (the
// very next statement after NewHome in llm_serve.go) has always happened;
// the timeout is a defensive bound, not an expected path.
const homeBindTimeout = 10 * time.Second

// EngineHost hosts ONE delegated run's engine inside the runner process (`llm
// serve`): it answers the coordinator's StartRun by launching the backend's
// StructuredChat conversation IN-PROCESS — the go-plugin Chat RPC is never
// dialed on this path; go-plugin remains only the process-spawn/kill
// transport — and adapts the engine's native event stream onto plane-1
// AgentEvents on the RunChannel.
type EngineHost struct {
	backend agent.StructuredChat
	harness string // the backend name RunnerHello advertised
	runID   string // CTXLOOM_RUN_ID — the one run this runner may host

	baseCtx context.Context

	homeReady chan struct{}
	home      engineHome

	mu      sync.Mutex
	started bool
	result  *agentcoordpb.RunnerResponse // cached StartRunResult (request reissue idempotency)
	in      chan agent.ChatMessage
	cancel  context.CancelFunc
	runCtx  context.Context     // the hosted run's own ctx (nil before startRun)
	rec     transcript.Recorder // this run's canonical transcript (nil if unopenable)
	briefed chan struct{}       // closed once the briefing turn's own send completed
	inTurn  bool                // the engine is mid-turn (adapt maintains it)

	// pendingTags is the turn-attribution FIFO: enqueueTurn pushes one tag per
	// locally-originated turn, in send order, and the adapt loop pops one at
	// each turn start. It is what lets a control verb know WHICH turn was its
	// own even when other turns are queued around it — the substrate the
	// question/summarize capture rides. Turn order equals send order because
	// enqueueTurn serializes the sends onto the unbuffered `in`.
	pendingTags []turnTag
	currentTag  turnTag

	// paused, when non-nil, is the PAUSE GATE (spoolcontrol.go's ControlPause):
	// a channel every locally-originated turn waits on before it may reach the
	// engine, closed by ResumeRun. Nil means running — the gate is absent
	// rather than open, so an unpaused run does not so much as select on it.
	//
	// It gates the HAND-OFF and nothing else. A turn already inside the engine
	// runs to its end (no surface ctxloom drives takes an interrupt), and mail
	// stays unconsumed in its spool, which is what makes a pause survivable
	// across a relaunch: nothing was taken that was not delivered.
	paused chan struct{}

	// reannounce is the turn-boundary re-announcer's state (F10): what keeps
	// an announced-but-never-pulled control body from sitting in the recv
	// buffer, unread and unreported, until the process dies. See
	// enginehost_reannounce.go.
	reannounce reannounceState

	// enqueueMu serializes the (push tag, send turn) pair so the FIFO cannot
	// desynchronise from the order the engine actually receives turns. Two
	// goroutines sending on an unbuffered channel is a race Go does not resolve
	// in send order, so the ordering has to be imposed here.
	enqueueMu sync.Mutex

	// tracked owns every goroutine startRun/adapt dispatches beyond its
	// spawning call's own return (the in-process backend.Chat call, the adapt
	// loop, the briefing's first-turn send, and one resolveApproval per
	// forwarded engine permission request). Close joins it before returning, so
	// a runner-side teardown leaves no goroutine still touching eh/home state —
	// the same discipline as Coordinator's and Home's groups, mirrored here
	// for the runner-hosted engine half. A still-in-flight resolveApproval, or
	// a startRun reissue landing exactly as Close begins, is what the seal in
	// trackedGroup is for.
	tracked   trackedGroup
	closeOnce sync.Once
}

// NewEngineHost builds the host for the runner's one hostable run. ctx bounds
// the engine's whole lifetime (the runner process's serve context).
func NewEngineHost(ctx context.Context, backend agent.StructuredChat, harness, runID string) *EngineHost {
	return &EngineHost{
		backend:   backend,
		harness:   harness,
		runID:     runID,
		baseCtx:   ctx,
		homeReady: make(chan struct{}),
	}
}

// engineHostCloseJoinBudget bounds Close's wait — see Coordinator's
// closeJoinBudget for the identical reasoning (every tracked goroutine here
// keys off the run's own ctx, cancelled by Close before waitTracked runs).
const engineHostCloseJoinBudget = 3 * time.Second

// goTracked runs fn on a new goroutine Close joins before returning — see
// trackedGroup.
func (eh *EngineHost) goTracked(fn func()) { eh.tracked.dispatch(fn) }

// waitTracked joins every eh.goTracked goroutine, with a bounded escape.
func (eh *EngineHost) waitTracked() {
	eh.tracked.wait(engineHostCloseJoinBudget, "engine host close", "a leaked goroutine may still touch home/backend state")
}

// Close cancels the hosted run (if StartRun ever launched one) and joins
// every tracked goroutine before returning — the runner-side teardown
// counterpart to Coordinator.Close/Home.Close. Idempotent
// (closeOnce-guarded) and safe to call even when no run was ever started.
func (eh *EngineHost) Close() {
	eh.closeOnce.Do(func() {
		eh.tracked.seal()
		eh.mu.Lock()
		cancel := eh.cancel
		eh.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		eh.waitTracked()
	})
}

// BindHome wires the dialed Home (event emission + turn sink + RunExited).
// Called once, immediately after NewHome; Handle blocks briefly for it.
func (eh *EngineHost) BindHome(h engineHome) {
	eh.mu.Lock()
	defer eh.mu.Unlock()
	if eh.home != nil {
		return
	}
	eh.home = h
	// The engine host IS the control executor: registering here means a Home
	// with an engine never keeps the no-engine UNIMPLEMENTED fallback.
	h.SetRequestHandler(eh.HandleControl)
	close(eh.homeReady)
}

// Handle answers coordinator-initiated RunnerRequests — the RunnerRequestHandler
// wired into HomeConfig.Engine.
func (eh *EngineHost) Handle(req *agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse {
	select {
	case <-eh.homeReady:
	case <-time.After(homeBindTimeout):
		return &agentcoordpb.RunnerResponse{Status: statusErr(codes.Unavailable, "runner engine host is not bound to its coordinator link yet")}
	}
	switch kind := req.GetKind().(type) {
	case *agentcoordpb.RunnerRequest_StartRun:
		return eh.startRun(kind.StartRun)
	case *agentcoordpb.RunnerRequest_PauseRun:
		return eh.pauseRun(kind.PauseRun)
	case *agentcoordpb.RunnerRequest_ResumeRun:
		return eh.resumeRun(kind.ResumeRun)
	case *agentcoordpb.RunnerRequest_KillRun, *agentcoordpb.RunnerRequest_StopRun:
		// C1-minimal termination: cancel the engine context (Chat returns,
		// RunExited flows). The graceful interrupt-then-escalate StopRun
		// ladder is Wave C2's; the coordinator's terminal path kills the
		// runner PROCESS via go-plugin either way.
		eh.mu.Lock()
		cancel := eh.cancel
		eh.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if _, isKill := kind.(*agentcoordpb.RunnerRequest_KillRun); isKill {
			return &agentcoordpb.RunnerResponse{Status: okStatus(""), Kind: &agentcoordpb.RunnerResponse_KillRun{KillRun: &agentcoordpb.KillRunResult{}}}
		}
		return &agentcoordpb.RunnerResponse{Status: okStatus(""), Kind: &agentcoordpb.RunnerResponse_StopRun{StopRun: &agentcoordpb.StopRunResult{}}}
	default:
		return &agentcoordpb.RunnerResponse{Status: statusErr(codes.Unimplemented, "request kind not offered by this runner")}
	}
}

// startRun launches the hosted engine for the run this runner was spawned
// for. Idempotent on reissue (same run_id after a reconnect returns the
// cached result); a second DIFFERENT run is refused (MaxConcurrentRuns=1).
func (eh *EngineHost) startRun(sr *agentcoordpb.StartRun) *agentcoordpb.RunnerResponse {
	eh.mu.Lock()
	if eh.started {
		if sr.GetRunId() == eh.runID && eh.result != nil {
			cached := eh.result
			eh.mu.Unlock()
			return cached
		}
		eh.mu.Unlock()
		return &agentcoordpb.RunnerResponse{Status: statusErr(codes.ResourceExhausted, fmt.Sprintf("runner already hosts run %s (max_concurrent_runs=1)", eh.runID))}
	}
	if sr.GetRunId() != eh.runID {
		eh.mu.Unlock()
		return &agentcoordpb.RunnerResponse{Status: statusErr(codes.PermissionDenied, fmt.Sprintf("this runner was spawned for run %s, not %s (A9 correlation)", eh.runID, sr.GetRunId()))}
	}
	if h := sr.GetHarness().GetHarness(); h != eh.harness {
		eh.mu.Unlock()
		return &agentcoordpb.RunnerResponse{Status: statusErr(codes.FailedPrecondition, fmt.Sprintf("this runner drives %q, StartRun asked for %q (RunnerHello is the advertisement)", eh.harness, h))}
	}
	dec, err := decodeHarnessSpec(sr.GetHarness())
	if err != nil {
		eh.mu.Unlock()
		return &agentcoordpb.RunnerResponse{Status: statusErr(codes.InvalidArgument, err.Error())}
	}
	injectMCPSocketEnv(dec.Chat.MCPServers, os.Getenv(EnvMCPSocket))
	prompt := ""
	if in := sr.GetInput(); in != nil {
		if v, ok := in.GetFields()["prompt"]; ok {
			prompt = v.GetStringValue()
		}
	}

	ctx, cancel := context.WithCancel(eh.baseCtx)
	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent, 64)
	eh.started = true
	eh.in = in
	eh.cancel = cancel
	eh.runCtx = ctx
	eh.briefed = make(chan struct{})
	briefed := eh.briefed
	home := eh.home
	result := &agentcoordpb.RunnerResponse{
		Status: okStatus(""),
		Kind: &agentcoordpb.RunnerResponse_StartRun{StartRun: &agentcoordpb.StartRunResult{
			// The runner process is the engine chain's root (killing it kills
			// the harness); the harness-native session id rides the
			// ctxloom/harness_session event the moment the engine reports it.
			Pid: int64(os.Getpid()),
		}},
	}
	eh.result = result
	eh.mu.Unlock()

	// RunStarted first: the log is self-contained (input + config echo,
	// including whether this attempt resumed a prior native session).
	home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_RunStarted{RunStarted: &agentcoordpb.RunStarted{
		Input:       sr.GetInput(),
		Agent:       &agentcoordpb.AgentIdentity{Role: sr.GetRole()},
		Config:      runStartedConfig(sr.GetHarness()),
		ParentRunId: sr.GetParentRunId(),
	}}})

	// Capture this delegated child's canonical transcript on
	// the runner process that hosts it — the in-process backend.Chat seam
	// (plan §2c seam 2). dec.SessionHarp is the child's own harp (decoded from
	// StartRun's HarnessSpec.config, "ctxloom.session_harp" — see
	// harnessspec.go), always assigned by the coordinator before StartRun is
	// issued (buildHarnessSpec's caller always passes rt.harp), so unlike the
	// GRPCClient.Chat seam this is not expected to hit the no-harp gap in
	// practice; the emptiness check stays as defensive degrade-gracefully
	// discipline, not a documented live gap.
	//
	// rec is opened HERE, before backend.Chat is ever dispatched,
	// so it can ALSO record the user turns this host writes to `in` below —
	// the briefing prompt and any later coordinator-delivered mail
	// (SetTurnSink). TeeAndClose only ever sees the outbound `out`/ChatEvent
	// stream, so without this a delegated child's canonical transcript
	// carried assistant output but no user turns at all, same as the
	// GRPCClient.Chat seam. The briefing is recorded synchronously right
	// below, strictly before backend.Chat's own goroutine starts — the one
	// case here where we know the full user text up front, so there is no
	// need to race it against the backend's own first ChatEvent (its Session
	// event, typically) the way a later, dynamically-arriving SetTurnSink
	// message necessarily does.
	var rec transcript.Recorder
	if dec.SessionHarp != "" {
		r, rerr := transcript.NewRecorder(dec.SessionHarp, eh.harness, transcript.WithRawPolicy(transcript.RawPolicy(dec.Chat.TranscriptRawPolicy)))
		if rerr != nil {
			clidiag.Warn("ctxloom", "transcript capture: open recorder for harp %s (engine %s): %v", dec.SessionHarp, eh.harness, rerr)
		} else {
			rec = r
		}
	}
	if prompt != "" {
		transcript.RecordUserText(rec, prompt)
	}
	eh.mu.Lock()
	eh.rec = rec
	if prompt != "" {
		// The briefing's tag is pushed HERE, synchronously, before anything
		// else can enqueue a turn — so the FIFO's first entry is the briefing
		// even though its send happens on a goroutine below. enqueueTurn's own
		// pushes wait on `briefed`, which keeps the two in step.
		eh.pendingTags = append(eh.pendingTags, turnTag{})
	}
	eh.mu.Unlock()

	chatErr := make(chan error, 1)
	eh.goTracked(func() {
		chatErr <- eh.backend.Chat(ctx, dec.Chat, in, out)
	})

	adaptOut := (<-chan agent.ChatEvent)(out)
	if rec != nil {
		adaptOut = transcript.TeeAndClose(rec, out)
	}
	eh.goTracked(func() { eh.adapt(ctx, home, adaptOut, chatErr) })

	// SetTurnSink's closure and the briefing goroutine below both
	// send to the same UNBUFFERED `in`, from two different goroutines, with
	// no ordering between them — a Go select/send race Go itself does not
	// resolve in send order. If mail is already queued at standup
	// (issueStartRun's pushMail, immediately after startRun returns), the
	// coordinator's mail delivery can win that race and land as the
	// child's FIRST turn, with the briefing (composed context + prompt)
	// arriving second — every signal still reports success. `briefed`
	// gates the turn sink on the briefing's OWN send actually completing
	// first; a run with no briefing (prompt == "") has nothing to gate on.
	if prompt == "" {
		close(briefed)
	}

	// The engine turn-delivery seam: coordinator mail lands as new turns
	// (the driver's own loop queues mid-turn arrivals to the next boundary).
	// It rides enqueueTurn like every other locally-originated turn, so mail
	// and the control verbs cannot reach `in` by two different disciplines.
	home.SetTurnSink(func(pm *agentcoordpb.PeerMessage) bool {
		return eh.enqueueTurn(ctx, turnTag{}, frameCoordinatorMessage(pm)) == nil
	})

	// The briefing is the first turn (context already joined coordinator-side).
	if prompt != "" {
		eh.goTracked(func() {
			defer close(briefed)
			select {
			case in <- agent.ChatMessage{Text: prompt}:
			case <-ctx.Done():
			}
		})
	}
	return result
}

// adapt is the NATIVE-EVENT ADAPTATION: it translates the engine's
// agent.ChatEvent stream onto plane-1 AgentEvents until the stream closes,
// then emits the terminal RunCompleted and reports RunExited on the
// lifecycle link. Message items follow the contract's started→delta*→
// completed lifecycle (contiguous same-type entries share one message);
// tool calls pair results to starts FIFO (agent.SessionEntry carries no
// tool-call id — the synthesized-id latitude call, approved).
func (eh *EngineHost) adapt(ctx context.Context, home engineHome, out <-chan agent.ChatEvent, chatErr <-chan error) {
	var (
		inTurn    bool
		lastMeta  *agent.TurnMeta
		turns     int
		sessionID string
	)
	items := &itemStream{home: home}
	for ev := range out {
		switch {
		case ev.Session != nil:
			if ev.Session.SessionID != "" {
				sessionID = ev.Session.SessionID
				// resumable carries the engine's LIVE loadSession capability
				// (ChatSessionInfo.Resumable) up to the coordinator's run
				// record — the one-shot resume gate's live half (one-shot-
				// resume plan, Slice 4). It rides the SAME generic custom
				// event as the session id (no proto change) because both are
				// known at the same instant (session start) and both are the
				// coordinator's to journal per run.
				home.emitCustomEvent(CustomHarnessSession, map[string]any{
					"session_id": sessionID,
					"resumable":  ev.Session.Resumable,
				})
			}
		case ev.Entry != nil:
			if !inTurn {
				inTurn = true
				// Pop the turn-attribution FIFO: whatever enqueueTurn pushed
				// for THIS turn becomes the current tag, so a control verb can
				// tell its own turn from the ones queued around it.
				eh.beginTurn()
				home.emitCustomEvent(CustomTurnStarted, nil)
			}
			items.entry(ev.Entry)
		case ev.Complete != nil:
			items.closeOpen()
			lastMeta = ev.Complete
			turns++
			inTurn = false
			eh.endTurn()
			home.emitCustomEvent(CustomTurnIdle, map[string]any{"stop_reason": ev.Complete.StopReason})
			// THE TURN BOUNDARY IS THE TRIGGER (F10). A control body that
			// was announced and not pulled during the turn that just ended
			// gets re-announced here — the mechanism §6.1 assumed and never
			// had. It dispatches and returns; this loop must keep draining.
			eh.reannounceAtBoundary(home)
			// TURN-BOUNDARY SWEEP (the §6a drain, file plane): mail that
			// arrived mid-turn becomes the next turn here. It is a no-op
			// unless this run is cut over, and it dispatches rather than
			// blocks — this loop must keep draining.
			home.SweepSpoolIn()
		case ev.Permission != nil:
			// C2: HarnessSpec sets ForwardPermissions unconditionally on this
			// path now, so every engine permission request round-trips
			// through the coordinator's escalation ladder (approval.go)
			// instead of being auto-decided here. Its own goroutine — the
			// adapt loop must keep draining `out` while a relay rung parks
			// on a human, possibly for minutes.
			eh.goTracked(func() { eh.resolveApproval(ctx, home, ev.Permission) })
		}
	}
	items.closeOpen()

	result, exitCode := terminalResult(<-chatErr, ctx.Err(), lastMeta, turns)
	home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
		Result: result,
	}}})
	home.ReportRunExited(exitCode, sessionID)
}

// terminalResult classifies one ended engine stream: chatErr is what
// backend.Chat returned, ctxErr the run context's own state. Cancellation wins
// over the error Chat reports for it — a cancelled engine's error IS the
// cancellation, not a failure — and only a genuine failure exits non-zero.
func terminalResult(chatErr, ctxErr error, lastMeta *agent.TurnMeta, turns int) (*agentcoordpb.Result, int) {
	status := agentcoordpb.Result_RUN_STATUS_SUCCEEDED
	text := ""
	exitCode := 0
	switch {
	case ctxErr != nil:
		status = agentcoordpb.Result_RUN_STATUS_CANCELLED
		text = "engine cancelled"
	case chatErr != nil:
		status = agentcoordpb.Result_RUN_STATUS_FAILED
		text = chatErr.Error()
		exitCode = 1
	}
	return &agentcoordpb.Result{
		Status:   status,
		Text:     text,
		Usage:    usageFromMeta(lastMeta),
		NumTurns: uint32(turns),
	}, exitCode
}

// itemStream is adapt's per-run ITEM state: the currently open message and the
// tool-call FIFO. It owns the contract's started→delta*→completed lifecycle
// (contiguous same-type entries share one message) and the FIFO pairing of tool
// results to starts; adapt itself keeps only turn and session state.
type itemStream struct {
	home     engineHome
	openMsg  string
	openType agent.SessionEntryType
	msgSeq   int
	toolSeq  int
	toolFIFO []string
}

// entry adapts ONE native session entry.
func (s *itemStream) entry(e *agent.SessionEntry) {
	switch e.Type {
	case agent.EntryTypeToolUse:
		s.toolUse(e)
	case agent.EntryTypeToolResult:
		s.toolResult(e)
	default:
		s.text(e)
	}
}

// closeOpen completes the open message, if any.
func (s *itemStream) closeOpen() {
	if s.openMsg == "" {
		return
	}
	s.home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageCompleted{MessageCompleted: &agentcoordpb.MessageCompleted{
		MessageId: s.openMsg,
	}}})
	s.openMsg = ""
}

func (s *itemStream) toolUse(e *agent.SessionEntry) {
	s.closeOpen()
	s.toolSeq++
	id := fmt.Sprintf("tc-%d", s.toolSeq)
	s.toolFIFO = append(s.toolFIFO, id)
	s.home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ToolCallStarted{ToolCallStarted: &agentcoordpb.ToolCallStarted{
		ToolCallId: id,
		ToolName:   e.ToolName,
	}}})
	if len(e.ToolInput) > 0 {
		s.home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ToolCallArgsDelta{ToolCallArgsDelta: &agentcoordpb.ToolCallArgsDelta{
			ToolCallId:       id,
			ArgsJsonFragment: string(e.ToolInput),
		}}})
	}
}

// toolResult pairs a result to the oldest unpaired start (agent.SessionEntry
// carries no tool-call id — the synthesized-id latitude call, approved). A
// result with nothing to pair to still reports, under its own id.
func (s *itemStream) toolResult(e *agent.SessionEntry) {
	s.closeOpen()
	var id string
	if len(s.toolFIFO) > 0 {
		id, s.toolFIFO = s.toolFIFO[0], s.toolFIFO[1:]
	} else {
		s.toolSeq++
		id = fmt.Sprintf("tc-unpaired-%d", s.toolSeq)
	}
	s.home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ToolCallCompleted{ToolCallCompleted: &agentcoordpb.ToolCallCompleted{
		ToolCallId: id,
		IsError:    e.IsError,
		ResultText: e.ToolOutput,
	}}})
}

// text appends to the open message, opening a new one when the entry TYPE
// changes (role/channel are per-message, so a thinking entry cannot continue an
// assistant message). An empty entry is not a message at all.
func (s *itemStream) text(e *agent.SessionEntry) {
	if e.Content == "" {
		return
	}
	if s.openMsg == "" || s.openType != e.Type {
		s.closeOpen()
		s.msgSeq++
		s.openMsg = fmt.Sprintf("m-%d", s.msgSeq)
		s.openType = e.Type
		role, channel := messageRouting(e.Type)
		s.home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageStarted{MessageStarted: &agentcoordpb.MessageStarted{
			MessageId: s.openMsg,
			Role:      role,
			Channel:   channel,
		}}})
	}
	s.home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageDelta{MessageDelta: &agentcoordpb.MessageDelta{
		MessageId: s.openMsg,
		Text:      e.Content,
	}}})
}

// runStartedConfig echoes the HarnessSpec into RunStarted.config so the log
// alone shows what the run was started with (resume lineage included).
func runStartedConfig(spec *agentcoordpb.HarnessSpec) *structpb.Struct {
	if spec == nil {
		return nil
	}
	cfg, err := structpb.NewStruct(map[string]any{
		"harness":                         spec.GetHarness(),
		"model":                           spec.GetModel(),
		"workspace":                       spec.GetWorkspace(),
		"permission_mode":                 spec.GetPermissionMode(),
		"resumed_from_harness_session_id": spec.GetResumeSessionId(),
	})
	if err != nil {
		clidiag.Warn("ctxloom", "engine host: RunStarted config echo for harness %q: %v", spec.GetHarness(), err)
		return nil
	}
	return cfg
}

// messageRouting maps a ctxloom entry type onto the contract's role/channel
// split: assistant narrative → FINAL, thinking → REASONING, system/other →
// LOG (operational chatter).
func messageRouting(t agent.SessionEntryType) (agentcoordpb.MessageRole, agentcoordpb.MessageChannel) {
	switch t {
	case agent.EntryTypeThinking:
		return agentcoordpb.MessageRole_MESSAGE_ROLE_ASSISTANT, agentcoordpb.MessageChannel_MESSAGE_CHANNEL_REASONING
	case agent.EntryTypeSystem:
		return agentcoordpb.MessageRole_MESSAGE_ROLE_SYSTEM, agentcoordpb.MessageChannel_MESSAGE_CHANNEL_LOG
	case agent.EntryTypeAssistant:
		return agentcoordpb.MessageRole_MESSAGE_ROLE_ASSISTANT, agentcoordpb.MessageChannel_MESSAGE_CHANNEL_FINAL
	default:
		return agentcoordpb.MessageRole_MESSAGE_ROLE_SYSTEM, agentcoordpb.MessageChannel_MESSAGE_CHANNEL_LOG
	}
}

// usageFromMeta projects the engine's cumulative turn accounting onto the
// contract's Usage — money converted ONCE, here at the runner boundary, to
// micro-USD (A2: round-half-even; only integer micros exist on the wire).
func usageFromMeta(m *agent.TurnMeta) *agentcoordpb.Usage {
	if m == nil {
		return nil
	}
	return &agentcoordpb.Usage{
		InputTokens:              nonNegU64(m.InputTokens),
		OutputTokens:             nonNegU64(m.OutputTokens),
		CacheReadInputTokens:     nonNegU64(m.CacheReadTokens),
		CacheCreationInputTokens: nonNegU64(m.CacheCreationTokens),
		CostUsdMicros:            usdToMicros(m.CostUSD),
	}
}

func nonNegU64(n int) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n)
}

// usdToMicros converts a harness-reported float dollar amount to micro-USD
// with round-half-even. Non-finite or negative input reads as 0 (costs are
// non-negative by construction; a poisoned float must not wrap).
//
// MAGNITUDE saturates as well as sign: Go leaves a float→integer conversion
// whose value is out of the target's range implementation-defined, so a cost
// above ~1.8e13 USD (uint64 micros' ceiling) yielded an arbitrary number —
// on amd64 a value SMALLER than the truthful one, journaled as if measured.
// A saturated MaxUint64 is at least monotone in the input and unmistakably
// out-of-band.
func usdToMicros(usd float64) uint64 {
	if usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0
	}
	micros := math.RoundToEven(usd * 1e6)
	if micros >= math.MaxUint64 {
		return math.MaxUint64
	}
	return uint64(micros)
}

// coordinatorFrameOpen is the provenance header's opening literal. It is the
// one byte sequence a delivered turn may contain exactly once, and only where
// this package wrote it: a receiving model reads the header as the coordinator's
// own attribution of who sent the body.
const coordinatorFrameOpen = "[coordinator-delivered message"

// coordinatorFrameQuote opens the header literal's rewritten form wherever
// untrusted bytes carry it. What matters is that "[quoted-" is not a prefix of
// coordinatorFrameOpen, so the rewritten text cannot contain the literal at any
// offset — including one a neighbouring '[' might manufacture once the matched
// bracket is gone.
const coordinatorFrameQuote = "[quoted-"

// frameCoordinatorDelivery renders one coordinator-delivered message as the text
// of a new engine turn: a provenance header naming the sender and the message
// kind, then the body.
//
// INVARIANTS, all three of them about what the SENDER cannot do:
//   - the framed text contains exactly one header literal, and it is this
//     function's — every occurrence inside body is rewritten inert;
//   - the rendered kind is a name from the closed mail vocabulary
//     (knownMailKind), never sender bytes;
//   - the rendered sender id holds header-safe characters only, so it cannot
//     close the header early and append attributes of its own.
//
// The header is hand-written here and in exactly one other place (the legacy
// mail path funnels through this function). Rendering frames from generated
// encoders is what makes the invariants structural rather than remembered.
func frameCoordinatorDelivery(from, kind, body string) string {
	var b strings.Builder
	b.WriteString(coordinatorFrameOpen)
	if f := frameHeaderToken(from); f != "" {
		fmt.Fprintf(&b, " from=%s", f)
	}
	if knownMailKind(kind) {
		fmt.Fprintf(&b, " kind=%s", kind)
	}
	b.WriteString("]\n")
	b.WriteString(quoteFrameHeaders(body))
	return b.String()
}

// frameCoordinatorMessage projects the wire shape onto frameCoordinatorDelivery:
// the kind rides the sender's structured companion (retired once kind is a typed
// field), so it is read here and validated there.
func frameCoordinatorMessage(pm *agentcoordpb.PeerMessage) string {
	kind := ""
	if s := pm.GetStructured(); s != nil {
		if v, ok := s.GetFields()["kind"]; ok {
			kind = v.GetStringValue()
		}
	}
	return frameCoordinatorDelivery(pm.GetFromAgentId(), kind, pm.GetText())
}

// frameHeaderToken reduces one value to characters that cannot alter the
// header's structure. Coordinator-minted harps and UserSender already satisfy
// this, so the substitution is a no-op on every value produced today — it exists
// so a future producer cannot make the header lie by accident.
func frameHeaderToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// quoteFrameHeaders rewrites every occurrence of the provenance header literal
// inside untrusted text, case-insensitively (a differently-cased header reads
// just as authoritative to a model). The original bytes survive, visibly quoted,
// so the body still reaches the model in full; the rewrite is idempotent.
func quoteFrameHeaders(text string) string {
	// ASCII-only folding, byte for byte: strings.ToLower can change a string's
	// LENGTH on some non-ASCII runes, which would desync the fold's offsets from
	// text's and rewrite the wrong bytes. The marker is pure ASCII, so folding
	// only A-Z loses no match.
	fold := asciiLower(text)
	marker := asciiLower(coordinatorFrameOpen)
	if !strings.Contains(fold, marker) {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		if strings.HasPrefix(fold[i:], marker) {
			// The matched bytes keep their own case; only the opening bracket is
			// replaced, by a prefix that cannot re-form the literal.
			b.WriteString(coordinatorFrameQuote)
			b.WriteString(text[i+1 : i+len(marker)])
			i += len(marker)
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// asciiLower folds A-Z and leaves every other byte untouched, so the result has
// the same length and the same byte offsets as its input.
func asciiLower(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + ('a' - 'A')
		}
	}
	return string(out)
}

// approvalRequestBudget bounds the runner's OWN wait for the coordinator's
// answer to a forwarded ApprovalRequest — generous headroom above any
// plausible ladder configuration (a relay rung's own timeout is what
// actually paces escalation, typically minutes; this is a defensive
// ceiling against a wedged coordinator, never the pacing mechanism).
const approvalRequestBudget = 24 * time.Hour

// resolveApproval is the C2 runner-side half of the escalation ladder:
// forward one engine permission request as a plane-2 ApprovalRequest,
// translate the coordinator's ApprovalDecision back into the engine's
// PermissionAnswer, and journal the resolution as an InteractionRecorded
// AgentEvent — "the agent emits one per resolved request it initiated" per
// the contract's own doc comment. Runs on its own goroutine (see adapt).
func (eh *EngineHost) resolveApproval(ctx context.Context, home engineHome, pr *agent.PermissionRequest) {
	reqCtx, cancel := context.WithTimeout(ctx, approvalRequestBudget)
	defer cancel()
	agentReq := &agentcoordpb.AgentRequest{
		Timeout: durationpb.New(approvalRequestBudget),
		Kind: &agentcoordpb.AgentRequest_Approval{Approval: &agentcoordpb.ApprovalRequest{
			Kind:    classifyApprovalKind(pr.Kind),
			Title:   pr.ToolName,
			Payload: structFromJSON(pr.ToolInput),
			ItemId:  pr.ID,
		}},
	}
	resp, err := home.Request(reqCtx, agentReq)

	var (
		decision   *agentcoordpb.ApprovalDecision
		resolution agentcoordpb.InteractionRecorded_Resolution
		note       string
	)
	switch {
	case err != nil:
		clidiag.Warn("ctxloom", "engine host: approval request %q: %v (cancelling)", pr.ID, err)
		note = err.Error()
		resolution = agentcoordpb.InteractionRecorded_RESOLUTION_TIMED_OUT
	case resp.GetStatus().GetCode() != int32(codes.OK):
		note = resp.GetStatus().GetMessage()
		// CANCELLED, not DENIED: a non-OK status means the coordinator
		// REFUSED to decide (the !caller.IsChild() guard, an unknown run),
		// not that anyone decided against the request. The journal must
		// record the same thing the engine is told below.
		resolution = agentcoordpb.InteractionRecorded_RESOLUTION_CANCELLED
	default:
		decision = resp.GetApproval()
		note = decision.GetNote()
		resolution = interactionResolution(decision.GetDecision())
	}

	allow := decision != nil &&
		interactionResolution(decision.GetDecision()) == agentcoordpb.InteractionRecorded_RESOLUTION_GRANTED
	// NO DECISION -> NO OPTION. An empty OptionID is the ACP
	// {outcome:"cancelled"} reply (internal/acp/session.go's forwardPermission,
	// internal/acpagent/server.go), which says exactly what happened: nothing
	// answered. Running pickPermissionOption here instead would hand the engine
	// a reject_once option, and claude-code-acp renders that to the model — and
	// into the durable transcript — as {behavior:"deny", message:"User refused
	// permission to run tool"}: ctxloom's own refusal, attributed to an operator
	// who was never asked. A REAL DECISION_DECLINE (decision != nil) still picks
	// reject_once, because then someone genuinely did refuse.
	optionID := ""
	if decision != nil && decision.GetDecision() != agentcoordpb.ApprovalDecision_DECISION_CANCEL {
		optionID = pickPermissionOption(pr.Options, allow)
	}
	select {
	case eh.in <- agent.ChatMessage{Permission: &agent.PermissionAnswer{ID: pr.ID, OptionID: optionID}}:
	case <-ctx.Done():
	}

	detail, _ := structpb.NewStruct(map[string]any{
		"decision": decision.GetDecision().String(), "note": note, "option_id": optionID,
	})
	home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_Interaction{Interaction: &agentcoordpb.InteractionRecorded{
		RequestId:  agentReq.GetRequestId(),
		Kind:       "approval",
		Resolution: resolution,
		Detail:     detail,
	}}})
}

// injectMCPSocketEnv stamps the runner's own CTXLOOM_MCP_SOCKET onto the
// ctxloom forwarder MCP server entry's OWN env, so the reach-back socket is
// delivered EXPLICITLY over the ACP session/new mcpServers env (mapping.go's
// mcpServersToACP renders each stdio server's Env verbatim) rather than
// relying on the engine adapter to propagate its ambient process env down to
// the MCP subprocess it spawns.
//
// The two shipping ACP adapters differ exactly here: claude-code-acp passes
// its own env through to a spawned stdio MCP server (so the ambient
// CTXLOOM_MCP_SOCKET reached the shim and its agent_send forwarded to the
// coordinator), but codex-acp does NOT — the shim then found no socket, fell
// back to its LOCAL surface, stood up a second rogue coordinator in-process,
// and every `agent_send(to:"parent")` failed with "this session is the
// coordinator — it has no parent" (j002300 @live, codex-child). Injecting the
// value into the entry's declared env removes the dependency on adapter
// behavior entirely — the isolation-must-not-negotiate discipline applied to
// reach-back delivery: make it a property of what we send, not a promise we
// hope the vendor keeps.
//
// socket=="" (no runner MCP endpoint — a bare/degraded runner) injects
// nothing. An entry that already declares the var (a user override) is left
// untouched.
func injectMCPSocketEnv(servers []agent.ChatMCPServer, socket string) {
	if socket == "" {
		return
	}
	for i := range servers {
		if servers[i].Name != agent.MCPServerName {
			continue
		}
		if servers[i].Env == nil {
			servers[i].Env = map[string]string{}
		}
		if _, ok := servers[i].Env[EnvMCPSocket]; !ok {
			servers[i].Env[EnvMCPSocket] = socket
		}
	}
}

// classifyApprovalKind buckets a backend-classified permission request's
// Kind (ACP's ToolCallKind vocabulary today: execute/edit/delete/move/read/
// search/fetch/think/other) onto the contract's ApprovalKind. Unclassified
// or unrecognized values fall back to TOOL_USE — the generic bucket, never
// silently dropped.
//
// Backend-parity recon: this mapping needs NO per-backend cases.
// claude/codex/kiro/generic-acp all reach here through the SAME code path —
// internal/acp/mapping.go's permissionRequestEvent decodes every adapter's
// session/request_permission via the one pinned ACP SDK's api.ToolCallKind,
// so "kind" is already protocol-normalized before it gets here regardless of
// which real adapter binary (claude-code-acp/codex-acp/kiro-cli) produced
// it — there is no adapter-specific vocabulary to extend against. A future
// backend that classifies permissions OUTSIDE the ACP driver (a non-ACP
// StructuredChat implementation) is the only case that would need a new
// case here.
func classifyApprovalKind(kind string) agentcoordpb.ApprovalRequest_ApprovalKind {
	switch kind {
	case "execute":
		return agentcoordpb.ApprovalRequest_APPROVAL_KIND_COMMAND_EXECUTION
	case "edit", "delete", "move":
		return agentcoordpb.ApprovalRequest_APPROVAL_KIND_FILE_CHANGE
	default:
		return agentcoordpb.ApprovalRequest_APPROVAL_KIND_TOOL_USE
	}
}

// structFromJSON best-effort projects a permission request's raw tool input
// onto a Struct payload: a JSON object marshals directly; any other JSON
// shape (array, scalar) wraps as {"value": ...} so a non-object input never
// fails the whole approval. Input that is not valid JSON at all wraps as its
// own RAW TEXT under the same key rather than vanishing — this payload is
// what a human on the escalation ladder reads to decide, and a nil payload
// left them the tool name and nothing about what it was asked to do. Only
// genuinely empty input yields no payload.
func structFromJSON(raw json.RawMessage) *structpb.Struct {
	if len(raw) == 0 {
		return nil
	}
	s := &structpb.Struct{}
	if err := protojson.Unmarshal(raw, s); err == nil {
		return s
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Not JSON. Carry the bytes as text, UTF-8-repaired: a Struct holding
		// invalid UTF-8 marshals nowhere, so an unrepaired string would drop
		// the payload again one layer further out.
		v = strings.ToValidUTF8(string(raw), "�")
	}
	wrapped, err := structpb.NewStruct(map[string]any{"value": v})
	if err != nil {
		clidiag.Warn("ctxloom", "engine host: approval payload could not be projected onto a Struct (%d bytes dropped): %v", len(raw), err)
		return nil
	}
	return wrapped
}

// interactionResolution is the ENFORCEMENT allow-list: only an explicit
// ACCEPT/ACCEPT_FOR_SESSION grants, everything else — including
// DECISION_UNSPECIFIED and any value proto3's open enums let through the
// wire — denies. Fail-CLOSED by construction, and the single definition
// approvalResolution (approval.go) mirrors so the child's
// InteractionRecorded and the coordinator's audit journal can never
// disagree about the same event.
func interactionResolution(d agentcoordpb.ApprovalDecision_Decision) agentcoordpb.InteractionRecorded_Resolution {
	switch d {
	case agentcoordpb.ApprovalDecision_DECISION_ACCEPT, agentcoordpb.ApprovalDecision_DECISION_ACCEPT_FOR_SESSION:
		return agentcoordpb.InteractionRecorded_RESOLUTION_GRANTED
	case agentcoordpb.ApprovalDecision_DECISION_CANCEL:
		return agentcoordpb.InteractionRecorded_RESOLUTION_CANCELLED
	default:
		return agentcoordpb.InteractionRecorded_RESOLUTION_DENIED
	}
}

// pickPermissionOption mirrors the acp driver's own pickOption (mapping.go)
// for the ladder's resolved decision: an ACCEPT[/_FOR_SESSION] selects an
// allow_* option, everything else a reject_* option (one-shot preferred
// over "always"). No matching option answers "" — the engine's safe
// cancelled no-op (neither approves nor commits a remembered rejection).
func pickPermissionOption(options []agent.PermissionOption, allow bool) string {
	want := []string{"reject_once", "reject_always"}
	if allow {
		want = []string{"allow_once", "allow_always"}
	}
	for _, k := range want {
		for _, o := range options {
			if o.Kind == k {
				return o.ID
			}
		}
	}
	return ""
}
