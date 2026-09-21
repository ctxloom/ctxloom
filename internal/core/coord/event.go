package coord

import (
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// Event is one plane-1 event as the coordinator sees it: the run it belongs
// to, its sequence number (the dedupe and ack key), and one payload. It is
// the domain form of the wire's AgentEvent — the proto is a PROJECTION of
// this type, held to it field for field by the parity pairs in tests/arch,
// and only the wire adapter ever holds both. Every payload kind the wire can
// carry has a domain form, so the watch fan-out (WatchRuns) loses nothing
// between the runner that emitted an event and the viewer that reads it.
type Event struct {
	TaskID       string
	RunID        string
	Seq          uint64
	OccurredAt   time.Time
	TurnID       string
	ParentItemID string
	Traceparent  string
	Payload      EventPayload
}

// EventPayload is the sealed set of event payload kinds.
type EventPayload interface {
	eventPayload()
	kind() string
}

// Kind names the payload's case in the wire's own vocabulary — the proto
// field name of the oneof member ("run_completed", "message_delta", …) — or
// "" for an event without a payload. It is the item journal's kind string,
// so it must be spelled the way the schema spells it.
func (e Event) Kind() string {
	if e.Payload == nil {
		return ""
	}
	return e.Payload.kind()
}

// RunStarted opens a run.
type RunStarted struct {
	Input       map[string]any
	Agent       *AgentIdentity
	Config      map[string]any
	ParentRunID string
}

// StepStarted opens a step.
type StepStarted struct {
	StepID  string
	Title   string
	Ordinal uint32
}

// StepCompleted closes a step.
type StepCompleted struct {
	StepID  string
	Outcome StepOutcome
	Detail  string
}

// StatusChanged reports a phase transition.
type StatusChanged struct {
	Phase  RunPhase
	Detail string
}

// InteractionRecorded records a resolved request.
type InteractionRecorded struct {
	RequestID  string
	Kind       string
	Resolution InteractionResolution
	Detail     map[string]any
}

// RunCompleted is the run's terminal event — exactly one per run.
type RunCompleted struct {
	Result *Result
}

// Result is a run's outcome.
type Result struct {
	Status           RunStatus
	Text             string
	StructuredOutput map[string]any
	OutputSchemaID   string
	Error            *Status
	Retryable        bool
	Usage            *Usage
	WallTime         time.Duration
	NumTurns         uint32
	ArtifactIDs      []string
}

// Usage is a run's token and cost accounting; PerModel breaks it down by
// model id, leaves carrying no PerModel of their own.
type Usage struct {
	InputTokens              uint64
	OutputTokens             uint64
	CacheReadInputTokens     uint64
	CacheCreationInputTokens uint64
	CostUSDMicros            uint64
	PerModel                 map[string]*Usage
}

// MessageStarted opens a message.
type MessageStarted struct {
	MessageID string
	Role      MessageRole
	Channel   MessageChannel
}

// MessageDelta appends text to an open message (UTF-8; concatenate in seq
// order).
type MessageDelta struct {
	MessageID string
	Text      string
}

// MessageCompleted closes a message with its full text.
type MessageCompleted struct {
	MessageID string
	FullText  string
}

// ToolCallStarted opens a tool call.
type ToolCallStarted struct {
	ToolCallID string
	ToolName   string
}

// ToolCallArgsDelta appends to a tool call's argument JSON.
type ToolCallArgsDelta struct {
	ToolCallID       string
	ArgsJSONFragment string
}

// ToolCallCompleted closes a tool call.
type ToolCallCompleted struct {
	ToolCallID  string
	Args        map[string]any
	IsError     bool
	ResultText  string
	ArtifactIDs []string
	Elapsed     time.Duration
}

// ArtifactProduced is an artifact manifest: the content address and where
// the bytes are (exactly one of Inline, ExternalURI, UploadID).
type ArtifactProduced struct {
	ArtifactID           string
	Revision             uint32
	Kind                 ArtifactKind
	Name                 string
	MediaType            string
	SizeBytes            uint64
	SHA256               []byte
	Inline               []byte
	ExternalURI          string
	UploadID             string
	ProducedByItemID     string
	Labels               map[string]string
	AddressesFeedbackIDs []string
}

// Summary is a filed report (the agent_report verb's payload).
type Summary struct {
	Scope            SummaryScope
	StepID           string
	Text             string
	Structured       map[string]any
	CoversThroughSeq uint64
	ArtifactIDs      []string
	PublishPaths     []string
}

// EventsLost is the watch hub's own marker for a lagging subscriber: the
// ranges it dropped. Coordinator-emitted only; a runner sending one is
// dropped by name (HandleEvent).
type EventsLost struct {
	Lost []LostRange
}

// LostRange is one contiguous run of dropped seqs (both ends inclusive).
type LostRange struct {
	RunID    string
	FirstSeq uint64
	LastSeq  uint64
}

// RawEvent carries an engine-native event verbatim.
type RawEvent struct {
	Source string
	Event  map[string]any
}

// CustomEvent is the open extension point: a namespaced name and a value.
// The ctxloom/* names the coordinator serves are the Custom* constants.
type CustomEvent struct {
	Name  string
	Value map[string]any
}

// AgentIdentity describes the agent a run is for.
type AgentIdentity struct {
	AgentID        string
	DisplayName    string
	Harness        string
	HarnessVersion string
	Model          string
	Role           string
	RunnerID       string
	ContainerName  string
}

// Status is a status carried inside a domain value (a run's terminal error,
// a refused handshake's reason): the canonical code number and a message.
// The coordinator's own answers are Go errors; this type exists for the
// statuses the coordinator forwards without interpreting.
type Status struct {
	Code    int32
	Message string
}

func (RunStarted) eventPayload()          {}
func (StepStarted) eventPayload()         {}
func (StepCompleted) eventPayload()       {}
func (StatusChanged) eventPayload()       {}
func (InteractionRecorded) eventPayload() {}
func (RunCompleted) eventPayload()        {}
func (MessageStarted) eventPayload()      {}
func (MessageDelta) eventPayload()        {}
func (MessageCompleted) eventPayload()    {}
func (ToolCallStarted) eventPayload()     {}
func (ToolCallArgsDelta) eventPayload()   {}
func (ToolCallCompleted) eventPayload()   {}
func (ArtifactProduced) eventPayload()    {}
func (Summary) eventPayload()             {}
func (EventsLost) eventPayload()          {}
func (RawEvent) eventPayload()            {}
func (CustomEvent) eventPayload()         {}

func (RunStarted) kind() string          { return "run_started" }
func (StepStarted) kind() string         { return "step_started" }
func (StepCompleted) kind() string       { return "step_completed" }
func (StatusChanged) kind() string       { return "status_changed" }
func (InteractionRecorded) kind() string { return "interaction" }
func (RunCompleted) kind() string        { return "run_completed" }
func (MessageStarted) kind() string      { return "message_started" }
func (MessageDelta) kind() string        { return "message_delta" }
func (MessageCompleted) kind() string    { return "message_completed" }
func (ToolCallStarted) kind() string     { return "tool_call_started" }
func (ToolCallArgsDelta) kind() string   { return "tool_call_args_delta" }
func (ToolCallCompleted) kind() string   { return "tool_call_completed" }
func (ArtifactProduced) kind() string    { return "artifact_produced" }
func (Summary) kind() string             { return "summary" }
func (EventsLost) kind() string          { return "events_lost" }
func (RawEvent) kind() string            { return "raw" }
func (CustomEvent) kind() string         { return "custom" }

// The enumerations below spell their values the way the wire's enums name
// them: the journal records these strings (a summary's scope, an artifact's
// kind), so the spelling is an on-disk contract, and the wire adapter's
// parity tests hold the proto enums to these tables.

// RunStatus is a run's terminal status.
type RunStatus string

const (
	RunStatusUnspecified    RunStatus = "RUN_STATUS_UNSPECIFIED"
	RunStatusSucceeded      RunStatus = "RUN_STATUS_SUCCEEDED"
	RunStatusFailed         RunStatus = "RUN_STATUS_FAILED"
	RunStatusCancelled      RunStatus = "RUN_STATUS_CANCELLED"
	RunStatusBudgetExceeded RunStatus = "RUN_STATUS_BUDGET_EXCEEDED"
	RunStatusTimedOut       RunStatus = "RUN_STATUS_TIMED_OUT"
)

// RunStatuses is every member, in wire order.
var RunStatuses = []RunStatus{RunStatusUnspecified, RunStatusSucceeded, RunStatusFailed, RunStatusCancelled, RunStatusBudgetExceeded, RunStatusTimedOut}

// ParseRunStatus resolves a member by its wire name. An unknown name is the
// vocabulary's unspecified member and ok is false — the receiving side's
// posture toward a value a newer build may spell.
func ParseRunStatus(name string) (RunStatus, bool) {
	return parseMember(RunStatuses, RunStatusUnspecified, name)
}

// StepOutcome is a step's outcome.
type StepOutcome string

const (
	StepOutcomeUnspecified StepOutcome = "OUTCOME_UNSPECIFIED"
	StepOutcomeSucceeded   StepOutcome = "OUTCOME_SUCCEEDED"
	StepOutcomeFailed      StepOutcome = "OUTCOME_FAILED"
	StepOutcomeSkipped     StepOutcome = "OUTCOME_SKIPPED"
)

// StepOutcomes is every member, in wire order.
var StepOutcomes = []StepOutcome{StepOutcomeUnspecified, StepOutcomeSucceeded, StepOutcomeFailed, StepOutcomeSkipped}

// ParseStepOutcome resolves a member by its wire name. An unknown name is the
// vocabulary's unspecified member and ok is false — the receiving side's
// posture toward a value a newer build may spell.
func ParseStepOutcome(name string) (StepOutcome, bool) {
	return parseMember(StepOutcomes, StepOutcomeUnspecified, name)
}

// RunPhase is a StatusChanged phase.
type RunPhase string

const (
	PhaseUnspecified     RunPhase = "PHASE_UNSPECIFIED"
	PhaseInitializing    RunPhase = "PHASE_INITIALIZING"
	PhasePlanning        RunPhase = "PHASE_PLANNING"
	PhaseExecuting       RunPhase = "PHASE_EXECUTING"
	PhaseWaitingApproval RunPhase = "PHASE_WAITING_APPROVAL"
	PhaseWaitingInput    RunPhase = "PHASE_WAITING_INPUT"
	PhaseWaitingPeer     RunPhase = "PHASE_WAITING_PEER"
	PhasePaused          RunPhase = "PHASE_PAUSED"
	PhaseFinalizing      RunPhase = "PHASE_FINALIZING"
)

// RunPhases is every member, in wire order.
var RunPhases = []RunPhase{PhaseUnspecified, PhaseInitializing, PhasePlanning, PhaseExecuting, PhaseWaitingApproval, PhaseWaitingInput, PhaseWaitingPeer, PhasePaused, PhaseFinalizing}

// ParseRunPhase resolves a member by its wire name. An unknown name is the
// vocabulary's unspecified member and ok is false — the receiving side's
// posture toward a value a newer build may spell.
func ParseRunPhase(name string) (RunPhase, bool) {
	return parseMember(RunPhases, PhaseUnspecified, name)
}

// InteractionResolution is how a recorded interaction resolved.
type InteractionResolution string

const (
	ResolutionUnspecified InteractionResolution = "RESOLUTION_UNSPECIFIED"
	ResolutionGranted     InteractionResolution = "RESOLUTION_GRANTED"
	ResolutionDenied      InteractionResolution = "RESOLUTION_DENIED"
	ResolutionTimedOut    InteractionResolution = "RESOLUTION_TIMED_OUT"
	ResolutionCancelled   InteractionResolution = "RESOLUTION_CANCELLED"
)

// InteractionResolutions is every member, in wire order.
var InteractionResolutions = []InteractionResolution{ResolutionUnspecified, ResolutionGranted, ResolutionDenied, ResolutionTimedOut, ResolutionCancelled}

// ParseInteractionResolution resolves a member by its wire name. An unknown name is the
// vocabulary's unspecified member and ok is false — the receiving side's
// posture toward a value a newer build may spell.
func ParseInteractionResolution(name string) (InteractionResolution, bool) {
	return parseMember(InteractionResolutions, ResolutionUnspecified, name)
}

// MessageRole is who a message is from.
type MessageRole string

const (
	RoleUnspecified MessageRole = "MESSAGE_ROLE_UNSPECIFIED"
	RoleAssistant   MessageRole = "MESSAGE_ROLE_ASSISTANT"
	RoleTool        MessageRole = "MESSAGE_ROLE_TOOL"
	RoleSystem      MessageRole = "MESSAGE_ROLE_SYSTEM"
)

// MessageRoles is every member, in wire order.
var MessageRoles = []MessageRole{RoleUnspecified, RoleAssistant, RoleTool, RoleSystem}

// ParseMessageRole resolves a member by its wire name. An unknown name is the
// vocabulary's unspecified member and ok is false — the receiving side's
// posture toward a value a newer build may spell.
func ParseMessageRole(name string) (MessageRole, bool) {
	return parseMember(MessageRoles, RoleUnspecified, name)
}

// MessageChannel is which audience a message addresses.
type MessageChannel string

const (
	ChannelUnspecified MessageChannel = "MESSAGE_CHANNEL_UNSPECIFIED"
	ChannelFinal       MessageChannel = "MESSAGE_CHANNEL_FINAL"
	ChannelReasoning   MessageChannel = "MESSAGE_CHANNEL_REASONING"
	ChannelLog         MessageChannel = "MESSAGE_CHANNEL_LOG"
)

// MessageChannels is every member, in wire order.
var MessageChannels = []MessageChannel{ChannelUnspecified, ChannelFinal, ChannelReasoning, ChannelLog}

// ParseMessageChannel resolves a member by its wire name. An unknown name is the
// vocabulary's unspecified member and ok is false — the receiving side's
// posture toward a value a newer build may spell.
func ParseMessageChannel(name string) (MessageChannel, bool) {
	return parseMember(MessageChannels, ChannelUnspecified, name)
}

// ArtifactKind classifies an artifact.
type ArtifactKind string

const (
	ArtifactKindUnspecified        ArtifactKind = "ARTIFACT_KIND_UNSPECIFIED"
	ArtifactKindTaskList           ArtifactKind = "ARTIFACT_KIND_TASK_LIST"
	ArtifactKindImplementationPlan ArtifactKind = "ARTIFACT_KIND_IMPLEMENTATION_PLAN"
	ArtifactKindCodeDiff           ArtifactKind = "ARTIFACT_KIND_CODE_DIFF"
	ArtifactKindWalkthrough        ArtifactKind = "ARTIFACT_KIND_WALKTHROUGH"
	ArtifactKindScreenshot         ArtifactKind = "ARTIFACT_KIND_SCREENSHOT"
	ArtifactKindRecording          ArtifactKind = "ARTIFACT_KIND_RECORDING"
	ArtifactKindReport             ArtifactKind = "ARTIFACT_KIND_REPORT"
	ArtifactKindDataset            ArtifactKind = "ARTIFACT_KIND_DATASET"
	ArtifactKindDocument           ArtifactKind = "ARTIFACT_KIND_DOCUMENT"
	ArtifactKindOther              ArtifactKind = "ARTIFACT_KIND_OTHER"
)

// ArtifactKinds is every member, in wire order.
var ArtifactKinds = []ArtifactKind{ArtifactKindUnspecified, ArtifactKindTaskList, ArtifactKindImplementationPlan, ArtifactKindCodeDiff, ArtifactKindWalkthrough, ArtifactKindScreenshot, ArtifactKindRecording, ArtifactKindReport, ArtifactKindDataset, ArtifactKindDocument, ArtifactKindOther}

// ParseArtifactKind resolves a member by its wire name. An unknown name is the
// vocabulary's unspecified member and ok is false — the receiving side's
// posture toward a value a newer build may spell.
func ParseArtifactKind(name string) (ArtifactKind, bool) {
	return parseMember(ArtifactKinds, ArtifactKindUnspecified, name)
}

// SummaryScope is a report's scope.
type SummaryScope string

const (
	ScopeUnspecified SummaryScope = "SCOPE_UNSPECIFIED"
	ScopeProgress    SummaryScope = "SCOPE_PROGRESS"
	ScopeStep        SummaryScope = "SCOPE_STEP"
	ScopeCheckpoint  SummaryScope = "SCOPE_CHECKPOINT"
	ScopeFinal       SummaryScope = "SCOPE_FINAL"
)

// SummaryScopes is every scope the Report verb accepts, in wire order.
var SummaryScopes = []SummaryScope{ScopeUnspecified, ScopeProgress, ScopeStep, ScopeCheckpoint, ScopeFinal}

// ParseSummaryScope resolves a scope by its wire name. An unknown name is
// the unspecified scope and ok is false.
func ParseSummaryScope(name string) (SummaryScope, bool) {
	return parseMember(SummaryScopes, ScopeUnspecified, name)
}

// parseMember is the membership lookup under every Parse<Vocabulary> here:
// a miss answers the vocabulary's unspecified member rather than the empty
// string, because these vocabularies spell their unspecified member the way
// the wire does.
func parseMember[T ~string](members []T, unspecified T, name string) (T, bool) {
	if m, ok := collections.Member(members, name); ok {
		return m, true
	}
	return unspecified, false
}
