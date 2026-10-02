package coord

import (
	"errors"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// The frames below are the coordination vocabulary as the coordinator
// speaks it: what a runner says on arrival, what the coordinator asks a
// runner, what a run asks the coordinator and how it is answered, and what
// the coordinator pushes to a run unasked. The wire adapter projects each
// onto the proto and back; nothing here names a generated type, and the
// coordinator's answers are Go errors — the adapter chooses the status code
// for each (its own table, keyed by the sentinel errors this package
// exports).

// DefaultRequestTimeout bounds a request on either plane when the caller's ctx
// carries no deadline.
const DefaultRequestTimeout = 60 * time.Second

// --- the runner plane (RunnerChannel) ---------------------------------------

// RunnerHello is what a runner says on every (re)connect: its
// self-description and the runs it already holds. It never carries an
// identity claim — identity is the connection credential's.
type RunnerHello struct {
	Version           string
	Harnesses         []string
	MaxConcurrentRuns uint32
	Labels            map[string]string
	ActiveRunIDs      []string
}

// RunnerRequest is one coordinator-initiated request to a runner, answered
// by a RunnerResponse correlated on RequestID.
type RunnerRequest struct {
	RequestID string
	Timeout   time.Duration
	Kind      RunnerRequestKind
}

// RunnerRequestKind is the sealed set of requests the coordinator issues.
type RunnerRequestKind interface{ runnerRequestKind() }

// StartRun hands a runner the resolved launch for the run it was spawned
// to host.
type StartRun struct {
	RunID  string
	Launch launch.Launch
}

// PauseRun asks the runner to hold the engine at its next boundary.
type PauseRun struct {
	RunID  string
	Reason string
}

// ResumeRun releases a PauseRun.
type ResumeRun struct {
	RunID string
}

// TurnRequest drives one engine turn on a parked one-shot runner; engine.Turn
// is the port's own turn input, carried as-is.
type TurnRequest struct {
	Turn engine.Turn
}

// InterruptRun cuts the run's turn in flight short; the run lives and parks
// for its next turn.
type InterruptRun struct {
	RunID string
}

// SetGrants replaces the run's session grants, from its next turn.
type SetGrants struct {
	RunID string
	Rules []string
}

func (StartRun) runnerRequestKind()     {}
func (PauseRun) runnerRequestKind()     {}
func (ResumeRun) runnerRequestKind()    {}
func (TurnRequest) runnerRequestKind()  {}
func (InterruptRun) runnerRequestKind() {}
func (SetGrants) runnerRequestKind()    {}

// StopRun is ALSO the runner request that ends one run: interrupt its turn in
// flight, wait up to Grace for it to end, then close the run.
func (StopRun) runnerRequestKind() {}

// RunnerResponse answers one RunnerRequest. Err is the runner's refusal (nil
// on success); the adapter wraps the wire's UNAVAILABLE in
// ErrRunnerUnavailable, the one refusal the coordinator answers with a
// rebind, and every other code as a plain error carrying the message.
type RunnerResponse struct {
	RequestID string
	Err       error
	Kind      RunnerResultKind
}

// RunnerResultKind is the sealed set of runner answers.
type RunnerResultKind interface{ runnerResultKind() }

// StartRunResult is a StartRun's answer.
type StartRunResult struct {
	HarnessSessionID string
	PID              int64
}

// PauseRunResult is a PauseRun's answer.
type PauseRunResult struct{ NewlyPaused bool }

// ResumeRunResult is a ResumeRun's answer.
type ResumeRunResult struct{ NewlyResumed bool }

// TurnResult is a Turn's answer; engine.TurnResult is the port's own.
type TurnResult struct {
	Result engine.TurnResult
}

func (StartRunResult) runnerResultKind()  {}
func (PauseRunResult) runnerResultKind()  {}
func (ResumeRunResult) runnerResultKind() {}
func (TurnResult) runnerResultKind()      {}

// ErrRunnerUnavailable is the runner's UNAVAILABLE: the recorded endpoint
// could not be bound. The spawn path answers it with ONE rebind.
var ErrRunnerUnavailable = errors.New("coord: the runner could not bind the recorded endpoint")

// ErrRunnerSessionEnded answers every request still in flight when its
// runner session dies before answering.
var ErrRunnerSessionEnded = errors.New("coord: runner session ended before answering")

// RunExited is the runner's process-level exit fact for one run.
type RunExited struct {
	RunID             string
	ExitCode          int32
	Signal            string
	TerminalEventSeen bool
	HarnessSessionID  string
}

// --- the run plane (RunChannel) --------------------------------------------

// RunHello is what a run says on every (re)connect of its channel: the run
// it is (checked against the credential's), the seq it resumes from, and
// the capabilities its hosted engine advertises.
type RunHello struct {
	TaskID          string
	RunID           string
	Agent           *AgentIdentity
	ResumeFromSeq   uint64
	ProtocolVersion uint32
	// Capabilities is DISCOVERY, not permission: what this run's endpoint can
	// execute, per Hello, so a resumed harp may advertise differently from the
	// run before it. "peer_messaging" and "terminal_delivery" are RETIRED
	// names and must not be reissued; a receiver that reads one from an older
	// peer must ignore it.
	Capabilities []string
}

// ErrRunNotIssued refuses a handshake naming a run the presenting credential
// was not issued.
var ErrRunNotIssued = errors.New("coord: run was not issued to this credential")

// AgentRequest is one plane-2 request from a run, answered by an AgentReply
// correlated on RequestID. RequestID is the responder-side idempotency key:
// it survives the channel it arrived on (reqTrack).
type AgentRequest struct {
	RequestID string
	Timeout   time.Duration
	Kind      AgentRequestKind
}

// AgentRequestKind is the sealed set of plane-2 requests: each verb's own
// request type, plus the two shapes the wire spells differently from the
// verb (a roster query and a stop by run id).
type AgentRequestKind interface{ agentRequestKind() }

// RosterRequest is agent_list on the wire.
type RosterRequest struct {
	Role            string
	IncludeTerminal bool
}

// StopRun is agent_stop on the wire: by run id (the caller must be the run's
// parent), or with no run id the bulk sweep of the caller's children, whose
// reason the Stop verb requires. Grace is how long a stopped run's turn in
// flight gets to end once interrupted; zero means DefaultStopGrace.
type StopRun struct {
	RunID  string
	Reason string
	Grace  time.Duration
}

func (SpawnRequest) agentRequestKind()    {}
func (RosterRequest) agentRequestKind()   {}
func (StopRun) agentRequestKind()         {}
func (ControlRequest) agentRequestKind()  {}
func (HostRequest) agentRequestKind()     {}
func (ApprovalRequest) agentRequestKind() {}

// ErrNotAChild refuses a send or stop naming a run that is not the caller's
// own direct child.
var ErrNotAChild = errors.New("the target is not a child of the calling session")

// ErrUnsupportedRequest refuses a plane-2 request kind this coordinator does
// not serve over the wire.
var ErrUnsupportedRequest = errors.New("request kind not offered in this window")

// ErrPeerSendIsLocal refuses agent_send over the wire: it is a local spool
// write at the runner and never reaches the coordinator as a request.
var ErrPeerSendIsLocal = errors.New("agent_send is a local spool write at the runner and is never served here; a runner that sent it over the wire is older than this coordinator")

// ControlToolName is the wire tool a Control verb answers as; the reply's
// disposition is worded by it.
func ControlToolName(verb string) string {
	switch verb {
	case ControlVerbSteer:
		return "agent_steer"
	case ControlVerbQuestion:
		return "agent_ask"
	case ControlVerbSummarize:
		return "agent_summarize"
	case ControlVerbPause:
		return "agent_pause"
	case ControlVerbResume:
		return "agent_resume"
	}
	return "control_run"
}

// AgentReply answers one AgentRequest. Err is the verb's refusal (nil on
// success); Message is the disposition an accepted request reports beside
// its result.
type AgentReply struct {
	RequestID string
	Err       error
	Message   string
	Result    AgentResult
}

// AgentResult is the sealed set of plane-2 answers.
type AgentResult interface{ agentResult() }

func (SpawnResult) agentResult()      {}
func (RunsSnapshot) agentResult()     {}
func (StopResult) agentResult()       {}
func (ControlResult) agentResult()    {}
func (HostResult) agentResult()       {}
func (ApprovalDecision) agentResult() {}

// OutFrame is one frame the coordinator pushes down a run channel: exactly
// one of the three is set. Acks are cumulative, replies are correlated, and
// notices are fire-and-forget.
type OutFrame struct {
	Ack    *Ack
	Reply  *AgentReply
	Notice *Notice
}

// Ack is the plane-3 watermark: every event at or below CommittedSeq is
// durable.
type Ack struct{ CommittedSeq uint64 }

// Notice is a plane-3 push. SpoolChanged rings the runner's doorbell for a
// spool file the coordinator wrote or consumed.
type Notice struct {
	SpoolChanged *spool.Ref
}

// --- projections -------------------------------------------------------------

// RunInfo is one roster row as the consumer plane sees it.
type RunInfo struct {
	TaskID         string
	RunID          string
	Agent          *AgentIdentity
	Phase          string
	LatestSummary  string
	LastEventAt    time.Time
	ParentRunID    string
	PermissionMode string
	MCPServers     []string
}

// RunsSnapshot is the roster at one instant.
type RunsSnapshot struct {
	Runs []RunInfo
}

// SpoolStats is the spool counters snapshot the consumer plane reports.
type SpoolStats struct {
	Delivered        uint64
	Consumed         uint64
	Failed           uint64
	DoorbellDropped  uint64
	DoorbellRejected uint64
}

// ControlInitiatorKind says who is asking for a control action.
type ControlInitiatorKind string

const (
	InitiatorUnspecified ControlInitiatorKind = "CONTROL_INITIATOR_KIND_UNSPECIFIED"
	InitiatorHuman       ControlInitiatorKind = "CONTROL_INITIATOR_KIND_HUMAN"
	InitiatorAgent       ControlInitiatorKind = "CONTROL_INITIATOR_KIND_AGENT"
)

// ControlInitiatorKinds is every member, in wire order.
var ControlInitiatorKinds = []ControlInitiatorKind{InitiatorUnspecified, InitiatorHuman, InitiatorAgent}

// ParseControlInitiatorKind resolves a member by its wire name. An unknown name is the
// vocabulary's unspecified member and ok is false — the receiving side's
// posture toward a value a newer build may spell.
func ParseControlInitiatorKind(name string) (ControlInitiatorKind, bool) {
	return parseMember(ControlInitiatorKinds, InitiatorUnspecified, name)
}
