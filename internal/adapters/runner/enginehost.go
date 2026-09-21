package runner

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// engineHome is the slice of *Home the engine host consumes — an interface so
// the adaptation logic tests hermetically without a dialed coordinator.
type engineHome interface {
	// BindIdentity binds the run's identity from the Launch, once, before
	// anything is driven — the spool's harp, the turn report's depth.
	BindIdentity(id coord.Identity)
	emitEvent(ev *agentcoordpb.AgentEvent) uint64
	emitCustomEvent(name string, value map[string]any)
	SetTurnSink(sink func(*agentcoordpb.PeerMessage) bool)
	// SweepSpoolIn asks the runner to reconcile its inbound spool — the
	// turn-boundary drain.
	SweepSpoolIn()
	// AwaitMailAcked blocks until the delivered messages ids have been
	// consume-acked (Home.AwaitMailAcked) — the exit path's guard against
	// reporting RunExited while an accepted turn's file is still in in/.
	AwaitMailAcked(ctx context.Context, ids []string) error
	ReportRunExited(exitCode int, harnessSessionID string)
	// ReportTurnResult writes this turn's own output to the parent as the
	// automatic turn report.
	ReportTurnResult(text, inReplyTo string) error
	// Request runs one plane-2 request to completion (Home.Request) — the
	// engine host's seam for issuing an agent-initiated request to the
	// coordinator and awaiting its answer.
	Request(ctx context.Context, req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error)
}

// Compile-time assertion that Home satisfies the engine host's seam.
var _ engineHome = (*Home)(nil)

// Runner is the tail the engine host hands a StartRun's launch to: it
// decodes the wire launch (the codec is adapters/coordgrpc's), redeems and
// decodes the package, delivers it into the cell, and drives the engine back
// through Drive. adapters/runner implements it; the host lives here until
// the engine-host files move beside Execute (Part 4.1, slice 14a).
type Runner interface {
	Execute(ctx context.Context, wire *agentcoordpb.Launch) error
}

// Turn is what the runner asks the engine host to drive once the launch is
// delivered: the launch itself, the structured-chat request built from it,
// the first turn's lead (the composed context ahead of the prompt on a
// fresh spawn; the prompt alone on a native-key resume), and what the
// static delivery produced — the presentations the engine's exec is
// composed over (engine.Instance.Exec).
type Turn struct {
	Launch    launch.Launch
	Chat      agent.ChatRequest
	Prompt    string
	Presented []present.Presentation
}

// Terminal drives an INTERACTIVE launch on the runner's own terminal — the
// pty slave the originator holds the master of, or the container's -it tty
// — and returns the engine's exit code once the human's session ends. The
// composition root binds it (BindTerminal); the engine host never touches
// the process's stdio itself.
type Terminal interface {
	Run(ctx context.Context, t Turn) (int, error)
}

var (
	// ErrNoRunner is startRun's refusal when no Runner was bound: a StartRun
	// with nothing to deliver it never launches an engine over nothing.
	ErrNoRunner = errors.New("engine host: no runner is bound to deliver the launch")
	// ErrNoTerminal refuses an interactive launch on a runner composed
	// without a terminal to drive it on.
	ErrNoTerminal = errors.New("engine host: no terminal is bound to drive an interactive launch")
)

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
	rep     report.Reporter
	backend agent.StructuredChat
	harness string // the backend name RunnerHello advertised
	runID   string // CTXLOOM_RUN_ID — the one run this runner may host

	baseCtx context.Context

	homeReady chan struct{}
	home      engineHome
	runner    Runner
	terminal  Terminal

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

	// The runner OUTLIVES a one-shot turn. oneShot is the launch identity's:
	// at each clean turn boundary the ENGINE process ends (the chat context
	// is cancelled) and the host PARKS — parked is true, nativeKey is the
	// engine's own session key as it last reported it — until the next turn
	// (delivered mail, or a Turn frame) starts a fresh engine process resumed
	// by that key. chatReq is the request every engine process is driven
	// with; resumable is the engine's live loadSession capability, without
	// which a one-shot boundary keeps the engine warm instead of parking.
	oneShot   bool
	resumable bool
	parked    bool
	nativeKey string
	chatReq   agent.ChatRequest

	// turnFinal accumulates the CURRENT turn's FINAL-channel text — the
	// engine's answer, excluding its reasoning and its system chatter — for
	// the automatic turn report (spoolturnresult.go).
	//
	// It accumulates the SAME deltas, in the same order, that the
	// coordinator's own accumulator does (children.go's accumulateFinalText,
	// which folds the plane-1 MessageDelta events these entries become), and
	// joins them the same way. That is deliberate and is what makes the two
	// carriers' reports byte-identical: one substrate changed, not what the
	// parent reads.
	turnFinal []string

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

	// enqueueMu serializes the (push tag, send turn) pair so the FIFO cannot
	// desynchronise from the order the engine actually receives turns. Two
	// goroutines sending on an unbuffered channel is a race Go does not resolve
	// in send order, so the ordering has to be imposed here.
	enqueueMu sync.Mutex

	// tracked owns every goroutine startRun/adapt dispatches beyond its
	// spawning call's own return (the in-process backend.Chat call, the adapt
	// loop, and the briefing's first-turn send). Close joins it before
	// returning, so a runner-side teardown leaves no goroutine still touching
	// eh/home state — the same discipline as Coordinator's and Home's groups,
	// mirrored here for the runner-hosted engine half. A still-in-flight
	// tracked goroutine, or a startRun reissue landing exactly as Close
	// begins, is what the seal in trackedGroup is for.
	tracked   coord.TrackedGroup
	closeOnce sync.Once
}

// NewEngineHost builds the host for the runner's one hostable run. ctx bounds
// the engine's whole lifetime (the runner process's serve context).
func NewEngineHost(ctx context.Context, rep report.Sink, backend agent.StructuredChat, harness, runID string) *EngineHost {
	return &EngineHost{
		rep:       report.To(rep),
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
func (eh *EngineHost) goTracked(fn func()) { eh.tracked.Dispatch(fn) }

// waitTracked joins every eh.goTracked goroutine, with a bounded escape.
func (eh *EngineHost) waitTracked() {
	eh.tracked.Wait(engineHostCloseJoinBudget, "engine host close", "a leaked goroutine may still touch home/backend state")
}

// Close cancels the hosted run (if StartRun ever launched one) and joins
// every tracked goroutine before returning — the runner-side teardown
// counterpart to Coordinator.Close/Home.Close. Idempotent
// (closeOnce-guarded) and safe to call even when no run was ever started.
func (eh *EngineHost) Close() {
	eh.closeOnce.Do(func() {
		eh.tracked.Seal()
		eh.mu.Lock()
		cancel := eh.cancel
		eh.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		eh.waitTracked()
		eh.closeRecorder()
	})
}

// closeRecorder closes the run's transcript recorder once, at the run's end
// — the recorder outlives the engine processes a parked session drives.
func (eh *EngineHost) closeRecorder() {
	eh.mu.Lock()
	rec := eh.rec
	eh.rec = nil
	eh.mu.Unlock()
	if rec != nil {
		_ = rec.Close()
	}
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
	close(eh.homeReady)
}

// BindRunner wires the runner tail a StartRun is executed through. Called
// once at composition, before any frame arrives.
func (eh *EngineHost) BindRunner(r Runner) {
	eh.mu.Lock()
	defer eh.mu.Unlock()
	eh.runner = r
}

// BindTerminal wires the terminal an interactive launch is driven on. Called
// once at composition; a runner without one refuses interactive launches.
func (eh *EngineHost) BindTerminal(t Terminal) {
	eh.mu.Lock()
	defer eh.mu.Unlock()
	eh.terminal = t
}

// Handle answers coordinator-initiated RunnerRequests — the RunnerRequestHandler
// wired into HomeConfig.Engine.
func (eh *EngineHost) Handle(req *agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse {
	select {
	case <-eh.homeReady:
	case <-time.After(homeBindTimeout):
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.FailedPrecondition, "runner engine host is not bound to its coordinator link yet")}
	}
	switch kind := req.GetKind().(type) {
	case *agentcoordpb.RunnerRequest_StartRun:
		return eh.startRun(kind.StartRun)
	case *agentcoordpb.RunnerRequest_PauseRun:
		return eh.pauseRun(kind.PauseRun)
	case *agentcoordpb.RunnerRequest_ResumeRun:
		return eh.resumeRun(kind.ResumeRun)
	case *agentcoordpb.RunnerRequest_Turn:
		return eh.turnFrame(kind.Turn)
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
			return &agentcoordpb.RunnerResponse{Status: coordgrpc.OKStatus(""), Kind: &agentcoordpb.RunnerResponse_KillRun{KillRun: &agentcoordpb.KillRunResult{}}}
		}
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.OKStatus(""), Kind: &agentcoordpb.RunnerResponse_StopRun{StopRun: &agentcoordpb.StopRunResult{}}}
	default:
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.Unimplemented, "request kind not offered by this runner")}
	}
}

// startRun launches the hosted engine for the run this runner was spawned
// for: the bound Runner executes the launch the frame carries (redeem →
// decode → deliver → Drive), and the result is this runner's pid.
// Idempotent on reissue (same run_id after a reconnect returns the cached
// result); a second DIFFERENT run is refused (MaxConcurrentRuns=1).
func (eh *EngineHost) startRun(sr *agentcoordpb.StartRun) *agentcoordpb.RunnerResponse {
	eh.mu.Lock()
	if eh.started {
		if sr.GetRunId() == eh.runID && eh.result != nil {
			cached := eh.result
			eh.mu.Unlock()
			return cached
		}
		eh.mu.Unlock()
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.ResourceExhausted, fmt.Sprintf("runner already hosts run %s (max_concurrent_runs=1)", eh.runID))}
	}
	if sr.GetRunId() != eh.runID {
		eh.mu.Unlock()
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.PermissionDenied, fmt.Sprintf("this runner was spawned for run %s, not %s (A9 correlation)", eh.runID, sr.GetRunId()))}
	}
	runner := eh.runner
	eh.mu.Unlock()
	if runner == nil {
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.FailedPrecondition, ErrNoRunner.Error())}
	}
	if err := runner.Execute(eh.baseCtx, sr.GetLaunch()); err != nil {
		// The ONE refusal the coordinator answers with a rebind rides a code
		// of its own: the recorded endpoint could not be bound (another
		// process took the port between two incarnations). Everything else
		// is the launch's own fault.
		if errors.Is(err, delivery.ErrEndpointUnavailable) {
			return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.Unavailable, err.Error())}
		}
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.InvalidArgument, err.Error())}
	}
	eh.mu.Lock()
	result := eh.result
	eh.mu.Unlock()
	if result == nil {
		return &agentcoordpb.RunnerResponse{Status: coordgrpc.StatusErr(codes.Internal, "the runner executed the launch but drove no engine")}
	}
	return result
}

// Drive is the engine-drive half of runner.Execute, called once the launch
// is delivered: it launches the backend's StructuredChat conversation
// IN-PROCESS over the request the runner built, records the transcript,
// and adapts the engine's native event stream onto plane-1 AgentEvents.
// The engine this runner advertised is the one the launch names — the
// runner refused any other before delivering.
func (eh *EngineHost) Drive(_ context.Context, t Turn) error {
	eh.mu.Lock()
	if eh.started {
		eh.mu.Unlock()
		return fmt.Errorf("engine host: run %s is already driven", eh.runID)
	}
	if string(t.Launch.Engine) != eh.harness {
		eh.mu.Unlock()
		return fmt.Errorf("this runner drives %q, the launch names %q (RunnerHello is the advertisement)", eh.harness, t.Launch.Engine)
	}
	if t.Launch.Mode == engine.Interactive {
		term := eh.terminal
		if term == nil {
			eh.mu.Unlock()
			return ErrNoTerminal
		}
		eh.started = true
		home := eh.home
		eh.result = eh.startRunResult()
		eh.mu.Unlock()
		return eh.driveInteractive(home, term, t)
	}
	prompt := t.Prompt
	eh.started = true
	eh.oneShot = t.Launch.Identity.OneShot
	eh.chatReq = t.Chat
	eh.nativeKey = t.Chat.ResumeSessionID
	home := eh.home
	eh.result = eh.startRunResult()
	eh.mu.Unlock()

	// Identity arrives ONCE, on the launch: the home learns which run it is
	// here, before the first frame it emits.
	home.BindIdentity(t.Launch.Identity)

	// RunStarted first: the log is self-contained (the first turn and the
	// launch's facts, including whether this attempt resumed a prior native
	// session).
	home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_RunStarted{RunStarted: &agentcoordpb.RunStarted{
		Input:  runStartedInput(prompt),
		Config: runStartedConfig(eh.rep, t),
	}}})

	// Capture this run's canonical transcript on the runner process that
	// hosts it. The harp is the launch's — the one carrier of identity to
	// the runner. rec is opened HERE, before any engine process starts, and
	// it OUTLIVES every one of them: a parked-and-resumed session writes one
	// transcript through as many engine processes as it takes turns. It
	// also records the user turns this host writes to `in` — the briefing
	// and any later coordinator-delivered mail (SetTurnSink).
	var rec transcript.Recorder
	if harp := t.Launch.Identity.Harp; harp != "" {
		r, rerr := transcript.NewRecorder(harp, eh.harness, transcript.WithRawPolicy(transcript.RawPolicy(t.Chat.TranscriptRawPolicy)))
		if rerr != nil {
			eh.rep.Warnf("transcript capture: open recorder for harp %s (engine %s): %v", harp, eh.harness, rerr)
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

	eh.startEngine(home, t.Chat, prompt)

	// The engine turn-delivery seam: coordinator mail lands as new turns
	// (the driver's own loop queues mid-turn arrivals to the next boundary;
	// a parked one-shot host starts a fresh engine process for it). It rides
	// enqueueTurn like every other locally-originated turn, so mail and the
	// control verbs cannot reach `in` by two different disciplines.
	home.SetTurnSink(func(pm *agentcoordpb.PeerMessage) bool {
		// The delivered message's id rides the turn's attribution tag, so the
		// report this turn produces can quote it (spoolturnresult.go). It is
		// the id the DELIVERY used — the file's origin id — which is exactly
		// what the sender registered its waiter under.
		return eh.enqueueTurn(eh.baseCtx, turnTag{mail: pm.GetMessageId()}, FrameCoordinatorMessage(pm)) == nil
	})
	return nil
}

// startRunResult is the StartRun answer for a driven run: the runner process
// is the engine chain's root (killing it kills the harness); the
// harness-native session id rides the ctxloom/harness_session event the
// moment the engine reports it.
func (eh *EngineHost) startRunResult() *agentcoordpb.RunnerResponse {
	return &agentcoordpb.RunnerResponse{
		Status: coordgrpc.OKStatus(""),
		Kind: &agentcoordpb.RunnerResponse_StartRun{StartRun: &agentcoordpb.StartRunResult{
			Pid: int64(os.Getpid()),
		}},
	}
}

// driveInteractive drives an interactive launch on the bound terminal: the
// human owns the session, so there is no briefing to send and no turn sink
// — coordinator mail reaches the engine through the terminal injector's
// nudge (Home.SetTerminalNudge). The engine's exit is the run's terminal:
// RunCompleted carries its status and RunExited its code.
func (eh *EngineHost) driveInteractive(home engineHome, term Terminal, t Turn) error {
	ctx, cancel := context.WithCancel(eh.baseCtx)
	eh.mu.Lock()
	eh.cancel = cancel
	eh.runCtx = ctx
	eh.mu.Unlock()
	home.BindIdentity(t.Launch.Identity)
	home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_RunStarted{RunStarted: &agentcoordpb.RunStarted{
		Input:  runStartedInput(t.Prompt),
		Config: runStartedConfig(eh.rep, t),
	}}})
	eh.goTracked(func() {
		code, err := term.Run(ctx, t)
		result := &agentcoordpb.Result{Status: agentcoordpb.Result_RUN_STATUS_SUCCEEDED}
		switch {
		case ctx.Err() != nil:
			result.Status = agentcoordpb.Result_RUN_STATUS_CANCELLED
			result.Text = "engine cancelled"
		case err != nil || code != 0:
			result.Status = agentcoordpb.Result_RUN_STATUS_FAILED
			if err != nil {
				result.Text = err.Error()
				result.Error = &rpcstatus.Status{Message: err.Error()}
			}
			if code == 0 {
				code = 1
			}
		}
		home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{Result: result}}})
		home.ReportRunExited(code, "")
	})
	return nil
}

// startEngine starts ONE engine process: the backend's in-process
// StructuredChat conversation over req, adapted onto plane-1 by adapt, with
// briefing as its first turn when non-empty. On the first drive it is the
// launch's request; on a one-shot unpark it is the same request with the
// engine's own session key as ResumeSessionID. The host's `in`, its run
// context and `briefed` are this process's; enqueueTurn reads them under
// the lock, so a turn always lands on the live process.
func (eh *EngineHost) startEngine(home engineHome, req agent.ChatRequest, briefing string) {
	ctx, cancel := context.WithCancel(eh.baseCtx)
	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent, 64)
	briefed := make(chan struct{})
	eh.mu.Lock()
	eh.in = in
	eh.cancel = cancel
	eh.runCtx = ctx
	eh.briefed = briefed
	eh.parked = false
	rec := eh.rec
	eh.mu.Unlock()

	chatErr := make(chan error, 1)
	eh.goTracked(func() {
		chatErr <- eh.backend.Chat(ctx, req, in, out)
	})

	adaptOut := (<-chan agent.ChatEvent)(out)
	if rec != nil {
		adaptOut = transcript.Tee(rec, out)
	}
	eh.goTracked(func() { eh.adapt(ctx, home, adaptOut, chatErr) })

	// SetTurnSink's closure and the briefing goroutine below both send to
	// the same UNBUFFERED `in`, from two different goroutines, with no
	// ordering between them — a Go select/send race Go itself does not
	// resolve in send order. If mail is already in the spool at standup
	// (the startup sweep runs the moment the run is up), that mail's
	// delivery can win that race and land as the child's FIRST turn, with
	// the briefing (composed context + prompt) arriving second — every
	// signal still reports success. `briefed` gates the turn sink on the
	// briefing's OWN send actually completing first; a process with no
	// briefing has nothing to gate on.
	if briefing == "" {
		close(briefed)
		return
	}
	eh.goTracked(func() {
		defer close(briefed)
		select {
		case in <- agent.ChatMessage{Text: briefing}:
		case <-ctx.Done():
		}
	})
}

// unpark starts a fresh engine process for a parked one-shot host, resumed
// by the native key the previous process reported. Called under enqueueMu
// by the turn that ends the park, so exactly one process is started per
// boundary. It is a no-op when the host is not parked.
func (eh *EngineHost) unpark() {
	eh.mu.Lock()
	if !eh.parked {
		eh.mu.Unlock()
		return
	}
	req := eh.chatReq
	req.ResumeSessionID = eh.nativeKey
	home := eh.home
	eh.mu.Unlock()
	eh.startEngine(home, req, "")
}

// parkAtBoundary decides, at a clean turn boundary, whether this engine
// process ends and the host parks: a one-shot session whose engine has
// live-confirmed it can be resumed by key. The engine process ends by
// cancellation (Chat returns, adapt's loop drains); the host stays, its
// endpoint bound, and the next turn resumes by key.
func (eh *EngineHost) parkAtBoundary(sessionID string, resumable bool) bool {
	eh.mu.Lock()
	defer eh.mu.Unlock()
	if !eh.oneShot || sessionID == "" || !resumable {
		return false
	}
	eh.parked = true
	eh.nativeKey = sessionID
	eh.resumable = resumable
	if eh.cancel != nil {
		eh.cancel()
	}
	return true
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
		resumable bool
		parked    bool
		// accepted is every delivered message whose turn the engine TOOK
		// (its tag was popped by beginTurn), completed or not — see
		// awaitAcceptedAcks.
		accepted []string
	)
	items := &itemStream{home: home, final: eh.appendTurnFinal}
	for ev := range out {
		switch {
		case ev.Session != nil:
			if ev.Session.SessionID != "" {
				sessionID = ev.Session.SessionID
				resumable = ev.Session.Resumable
				// resumable carries the engine's LIVE loadSession capability
				// (ChatSessionInfo.Resumable) up to the coordinator's run
				// record — the one-shot resume gate's live half (one-shot-
				// resume plan, Slice 4). It rides the SAME generic custom
				// event as the session id (no proto change) because both are
				// known at the same instant (session start) and both are the
				// coordinator's to journal per run.
				home.emitCustomEvent(coord.CustomHarnessSession, map[string]any{
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
				home.emitCustomEvent(coord.CustomTurnStarted, nil)
			}
			items.entry(ev.Entry)
		case ev.Complete != nil:
			items.closeOpen()
			lastMeta = ev.Complete
			turns++
			inTurn = false
			tag := eh.endTurn()
			accepted = appendMail(accepted, tag)
			final := eh.takeTurnFinal()
			// THE AUTOMATIC TURN REPORT (spoolturnresult.go). It runs BEFORE
			// the turn-idle event, so the child's answer is durable before
			// the coordinator is told the child is idle — which is the
			// moment a leftover-mail resume decision reads the spool.
			if err := home.ReportTurnResult(final, tag.mail); err != nil {
				eh.rep.Warnf("engine host: this turn's report was not written: %v", err)
			}
			if tag.done != nil {
				// A Turn frame is waiting on this turn: answer it with the
				// turn's final text and the key the next turn resumes by.
				tag.done <- engine.TurnResult{NativeKey: sessionID, Answer: final}
			}
			// THE ONE-SHOT BOUNDARY: the engine process ends here and the
			// host parks, keeping the run, its endpoint and this recorder; the
			// coordinator sees an idle run, never a terminal. Decided BEFORE
			// the idle event so a turn the coordinator sends on seeing "idle"
			// finds the host already parked and starts the next process.
			parked = eh.parkAtBoundary(sessionID, resumable)
			home.emitCustomEvent(coord.CustomTurnIdle, map[string]any{"stop_reason": ev.Complete.StopReason})
			// TURN-BOUNDARY SWEEP (the §6a drain): mail that arrived mid-turn
			// becomes the next turn here. It dispatches rather than blocks —
			// this loop must keep draining.
			home.SweepSpoolIn()
		}
	}
	items.closeOpen()
	// An engine that died mid-turn had taken that turn's message too; when
	// no turn is open endTurn hands back the zero tag and nothing is added.
	accepted = appendMail(accepted, eh.endTurn())
	awaitAcceptedAcks(eh.rep, home, accepted)

	chatEnd := <-chatErr
	if parked {
		// The engine process ended at its boundary by design; the run did
		// not. No terminal, no RunExited — the runner stays for the next turn.
		return
	}
	eh.closeRecorder()
	result, exitCode := terminalResult(chatEnd, ctx.Err(), lastMeta, turns)
	home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
		Result: result,
	}}})
	home.ReportRunExited(exitCode, sessionID)
}

// appendMail adds the delivered message a turn was started by, when one was.
func appendMail(accepted []string, tag turnTag) []string {
	if tag.mail == "" {
		return accepted
	}
	return append(accepted, tag.mail)
}

// mailAckFlushBudget bounds adapt's wait for accepted turns' consume-acks at
// exit. Generous against a slow filesystem, small against a hung one.
const mailAckFlushBudget = 5 * time.Second

// awaitAcceptedAcks is AT-LEAST-ONCE, RUNNER SIDE: every turn the engine took
// must be consumed on disk before ANY terminal signal leaves this runner. The
// coordinator's terminateRun reads in/ to decide whether to relaunch the harp
// for leftover mail; an exit that overtakes the pump's rename makes it launch
// a second run for a message this one already answered, and that run finds
// nothing and idles forever. The wait is short (the pump is in that id's ack
// tail) and bounded: a stuck rename is warned about, never allowed to hold
// the exit.
func awaitAcceptedAcks(rep report.Reporter, home engineHome, accepted []string) {
	if len(accepted) == 0 {
		return
	}
	actx, cancel := context.WithTimeout(context.Background(), mailAckFlushBudget)
	defer cancel()
	if err := home.AwaitMailAcked(actx, accepted); err != nil {
		rep.Warnf("engine host: exiting with %d delivered turn(s) not yet marked consumed (%v) — the coordinator may relaunch this harp for mail it already answered", len(accepted), err)
	}
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
	home engineHome
	// final receives every FINAL-channel fragment as it is emitted — the
	// engine's answer, as opposed to its reasoning (REASONING) or its
	// chatter (LOG). It is fed HERE, at the one place channels are decided
	// (messageRouting), so a future entry type cannot start counting as the
	// answer without passing through this switch.
	final    func(text string)
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
	if _, channel := messageRouting(e.Type); channel == agentcoordpb.MessageChannel_MESSAGE_CHANNEL_FINAL && s.final != nil {
		s.final(e.Content)
	}
}

// runStartedInput is the first turn as RunStarted.input carries it: the
// {prompt} object the log has always recorded. nil for an empty lead.
func runStartedInput(prompt string) *structpb.Struct {
	if prompt == "" {
		return nil
	}
	in, err := structpb.NewStruct(map[string]any{"prompt": prompt})
	if err != nil {
		return nil
	}
	return in
}

// runStartedConfig echoes the launch's facts into RunStarted.config so the
// log alone shows what the run was started with (resume lineage included).
func runStartedConfig(rep report.Reporter, t Turn) *structpb.Struct {
	cfg, err := structpb.NewStruct(map[string]any{
		"harness":                         string(t.Launch.Engine),
		"model":                           t.Launch.Label.Model,
		"workspace":                       t.Launch.Cell.Workspace,
		"permission_mode":                 t.Launch.Permission.String(),
		"resumed_from_harness_session_id": t.Launch.Resume.NativeKey,
	})
	if err != nil {
		rep.Warnf("engine host: RunStarted config echo for harness %q: %v", t.Launch.Engine, err)
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

// appendTurnFinal accumulates one FINAL-channel fragment for the current turn.
func (eh *EngineHost) appendTurnFinal(text string) {
	if text == "" {
		return
	}
	eh.mu.Lock()
	eh.turnFinal = append(eh.turnFinal, text)
	eh.mu.Unlock()
}

// takeTurnFinal returns the current turn's answer and CLEARS the accumulator,
// so the next turn starts empty. Joined with no separator, matching the
// coordinator accumulator's migrated-path join: these are fragments of one
// message, not a list of messages.
func (eh *EngineHost) takeTurnFinal() string {
	eh.mu.Lock()
	defer eh.mu.Unlock()
	text := strings.Join(eh.turnFinal, "")
	eh.turnFinal = nil
	return text
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

// CoordinatorFrameOpen is the provenance header's opening literal. It is the
// one byte sequence a delivered turn may contain exactly once, and only where
// this package wrote it: a receiving model reads the header as the coordinator's
// own attribution of who sent the body.
const CoordinatorFrameOpen = "[coordinator-delivered message"

// coordinatorFrameQuote opens the header literal's rewritten form wherever
// untrusted bytes carry it. What matters is that "[quoted-" is not a prefix of
// CoordinatorFrameOpen, so the rewritten text cannot contain the literal at any
// offset — including one a neighbouring '[' might manufacture once the matched
// bracket is gone.
const coordinatorFrameQuote = "[quoted-"

// FrameCoordinatorDelivery renders one coordinator-delivered message as the text
// of a new engine turn: a provenance header naming the sender and the message
// kind, then the body. It is THE model-visible shape of delivered mail on
// every path — the hosted turn sink here, and the session owner's turn-start
// hook (`ctxloom hook mail-drain`), which is why it is exported: a second
// renderer would be a second place for the invariants below to be forgotten.
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
func FrameCoordinatorDelivery(from, kind, body string) string {
	var b strings.Builder
	b.WriteString(CoordinatorFrameOpen)
	if f := frameHeaderToken(from); f != "" {
		fmt.Fprintf(&b, " from=%s", f)
	}
	if coord.KnownMailKind(kind) {
		fmt.Fprintf(&b, " kind=%s", kind)
	}
	b.WriteString("]\n")
	b.WriteString(quoteFrameHeaders(body))
	return b.String()
}

// FrameCoordinatorMessage projects the wire shape onto FrameCoordinatorDelivery.
// The kind is PeerMessage.kind, the typed field, spelled back into the mailbox
// vocabulary the renderer validates against; `structured` is the sender's
// opaque companion and is never consulted — a "kind" key in it is sender bytes,
// which is exactly what the header must not be built from.
func FrameCoordinatorMessage(pm *agentcoordpb.PeerMessage) string {
	return FrameCoordinatorDelivery(pm.GetFromAgentId(), agentcoordpb.LegacyKindName(pm.GetKind()), pm.GetText())
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
	marker := asciiLower(CoordinatorFrameOpen)
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
