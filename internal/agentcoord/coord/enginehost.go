package coord

import (
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// engineHome is the slice of *Home the engine host consumes — an interface so
// the adaptation logic tests hermetically without a dialed coordinator.
type engineHome interface {
	emitEvent(ev *agentcoordpb.AgentEvent) uint64
	emitCustomEvent(name string, value map[string]any)
	SetTurnSink(sink func(*agentcoordpb.PeerMessage) bool)
	ReportRunExited(exitCode int, harnessSessionID string, terminalEventSeen bool)
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
		return &agentcoordpb.RunnerResponse{Status: okStatus(""), Kind: &agentcoordpb.RunnerResponse_StopRun{StopRun: &agentcoordpb.StopRunResult{ExitedWithinGrace: true}}}
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

	chatErr := make(chan error, 1)
	go func() {
		chatErr <- eh.backend.Chat(ctx, dec.Chat, in, out)
	}()
	go eh.adapt(ctx, home, out, chatErr)

	// The engine turn-delivery seam: coordinator mail lands as new turns
	// (the driver's own loop queues mid-turn arrivals to the next boundary).
	home.SetTurnSink(func(pm *agentcoordpb.PeerMessage) bool {
		select {
		case in <- agent.ChatMessage{Text: frameCoordinatorMessage(pm)}:
			return true
		case <-ctx.Done():
			return false
		}
	})

	// The briefing is the first turn (context already joined coordinator-side).
	if prompt != "" {
		go func() {
			select {
			case in <- agent.ChatMessage{Text: prompt}:
			case <-ctx.Done():
			}
		}()
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
		openMsg   string
		openType  agent.SessionEntryType
		msgSeq    int
		toolSeq   int
		toolFIFO  []string
		lastMeta  *agent.TurnMeta
		turns     int
		sessionID string
	)
	closeOpenMsg := func() {
		if openMsg == "" {
			return
		}
		home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageCompleted{MessageCompleted: &agentcoordpb.MessageCompleted{
			MessageId: openMsg,
		}}})
		openMsg = ""
	}
	for ev := range out {
		switch {
		case ev.Session != nil:
			if ev.Session.SessionID != "" {
				sessionID = ev.Session.SessionID
				home.emitCustomEvent(CustomHarnessSession, map[string]any{"session_id": sessionID})
			}
		case ev.Entry != nil:
			if !inTurn {
				inTurn = true
				home.emitCustomEvent(CustomTurnStarted, nil)
			}
			e := ev.Entry
			switch e.Type {
			case agent.EntryTypeToolUse:
				closeOpenMsg()
				toolSeq++
				id := fmt.Sprintf("tc-%d", toolSeq)
				toolFIFO = append(toolFIFO, id)
				home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ToolCallStarted{ToolCallStarted: &agentcoordpb.ToolCallStarted{
					ToolCallId: id,
					ToolName:   e.ToolName,
				}}})
				if len(e.ToolInput) > 0 {
					home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ToolCallArgsDelta{ToolCallArgsDelta: &agentcoordpb.ToolCallArgsDelta{
						ToolCallId:       id,
						ArgsJsonFragment: string(e.ToolInput),
					}}})
				}
			case agent.EntryTypeToolResult:
				closeOpenMsg()
				var id string
				if len(toolFIFO) > 0 {
					id, toolFIFO = toolFIFO[0], toolFIFO[1:]
				} else {
					toolSeq++
					id = fmt.Sprintf("tc-unpaired-%d", toolSeq)
				}
				home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ToolCallCompleted{ToolCallCompleted: &agentcoordpb.ToolCallCompleted{
					ToolCallId: id,
					IsError:    e.IsError,
					ResultText: e.ToolOutput,
				}}})
			default:
				if e.Content == "" {
					continue
				}
				if openMsg == "" || openType != e.Type {
					closeOpenMsg()
					msgSeq++
					openMsg = fmt.Sprintf("m-%d", msgSeq)
					openType = e.Type
					role, channel := messageRouting(e.Type)
					home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageStarted{MessageStarted: &agentcoordpb.MessageStarted{
						MessageId: openMsg,
						Role:      role,
						Channel:   channel,
					}}})
				}
				home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageDelta{MessageDelta: &agentcoordpb.MessageDelta{
					MessageId: openMsg,
					Text:      e.Content,
				}}})
			}
		case ev.Complete != nil:
			closeOpenMsg()
			lastMeta = ev.Complete
			turns++
			inTurn = false
			home.emitCustomEvent(CustomTurnIdle, map[string]any{"stop_reason": ev.Complete.StopReason})
		case ev.Permission != nil:
			// Delegated children never forward permissions in the C1 window
			// (ForwardPermissions is false; the driver auto-decides per D3).
			// Defensive: an unexpected one is logged, never load-bearing.
			clidiag.Warn("ctxloom", "engine host: unexpected forwarded permission request %q (dropped; delegated children auto-decide)", ev.Permission.ToolName)
		}
	}
	closeOpenMsg()

	err := <-chatErr
	status := agentcoordpb.Result_RUN_STATUS_SUCCEEDED
	text := ""
	exitCode := 0
	if err != nil && ctx.Err() == nil {
		status = agentcoordpb.Result_RUN_STATUS_FAILED
		text = err.Error()
		exitCode = 1
	} else if ctx.Err() != nil {
		status = agentcoordpb.Result_RUN_STATUS_CANCELLED
		text = "engine cancelled"
	}
	home.emitEvent(&agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
		Result: &agentcoordpb.Result{
			Status:   status,
			Text:     text,
			Usage:    usageFromMeta(lastMeta),
			NumTurns: uint32(turns),
		},
	}}})
	home.ReportRunExited(exitCode, sessionID, true)
}

// runStartedConfig echoes the HarnessSpec into RunStarted.config so the log
// alone shows what the run was started with (resume lineage included).
func runStartedConfig(spec *agentcoordpb.HarnessSpec) *structpb.Struct {
	if spec == nil {
		return nil
	}
	cfg, err := structpb.NewStruct(map[string]any{
		"harness":                          spec.GetHarness(),
		"model":                            spec.GetModel(),
		"workspace":                        spec.GetWorkspace(),
		"permission_mode":                  spec.GetPermissionMode(),
		"resumed_from_harness_session_id":  spec.GetResumeSessionId(),
	})
	if err != nil {
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
func usdToMicros(usd float64) uint64 {
	if usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0
	}
	return uint64(math.RoundToEven(usd * 1e6))
}

// frameCoordinatorMessage renders one coordinator-delivered mailbox message
// as the text of a new engine turn: the kind survives as frame text
// (manly-grant (6)) alongside the sender, then the body verbatim.
func frameCoordinatorMessage(pm *agentcoordpb.PeerMessage) string {
	var b strings.Builder
	b.WriteString("[coordinator-delivered message")
	if from := pm.GetFromAgentId(); from != "" {
		fmt.Fprintf(&b, " from=%s", from)
	}
	if s := pm.GetStructured(); s != nil {
		if v, ok := s.GetFields()["kind"]; ok && v.GetStringValue() != "" {
			fmt.Fprintf(&b, " kind=%s", v.GetStringValue())
		}
	}
	b.WriteString("]\n")
	b.WriteString(pm.GetText())
	return b.String()
}
