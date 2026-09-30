package coordgrpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// The codec between the coordination vocabulary (event.go, frames.go) and
// the proto. Every value crosses here exactly once in each direction; the
// coordinator holds only the domain form, the wire only the generated one.
// The enumerations are spelled identically on both sides (the domain
// constants ARE the proto enum names), so an enum crosses by name and an
// unknown number crosses as the proto's own rendering of it.

// parsed takes a vocabulary parser's answer: the member it resolved, or the
// vocabulary's unspecified member for a name this build does not spell
// (proto3 enums are open on the wire; the coordinator's vocabulary is not).
func parsed[T ~string](member T, _ bool) T { return member }

// --- structs, times, durations ---------------------------------------------------

func structToMap(s *structpb.Struct) map[string]any {
	if s == nil {
		return nil
	}
	return s.AsMap()
}

// mapToStruct projects a JSON-shaped map; a map that cannot be a Struct (a
// value type JSON has no spelling for) crosses as absent rather than as a
// partial Struct, which would mean something the sender never said.
func mapToStruct(m map[string]any) *structpb.Struct {
	if m == nil {
		return nil
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return s
}

func timeToWire(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func timeFromWire(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.AsTime()
}

func durationToWire(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}

func durationFromWire(d *durationpb.Duration) time.Duration {
	if d == nil {
		return 0
	}
	return d.AsDuration()
}

func statusToWire(s *coord.Status) *rpcstatus.Status {
	if s == nil {
		return nil
	}
	return &rpcstatus.Status{Code: s.Code, Message: s.Message}
}

func statusFromWire(s *rpcstatus.Status) *coord.Status {
	if s == nil {
		return nil
	}
	return &coord.Status{Code: s.GetCode(), Message: s.GetMessage()}
}

// --- events -----------------------------------------------------------------------

// EventFromWire decodes one plane-1 event.
func EventFromWire(ev *agentcoordpb.AgentEvent) coord.Event {
	e := coord.Event{
		TaskID:       ev.GetTaskId(),
		RunID:        ev.GetRunId(),
		Seq:          ev.GetSeq(),
		OccurredAt:   timeFromWire(ev.GetOccurredAt()),
		TurnID:       ev.GetTurnId(),
		ParentItemID: ev.GetParentItemId(),
		Traceparent:  ev.GetTraceparent(),
	}
	e.Payload = payloadFromWire(ev.GetPayload())
	return e
}

// payloadFromWire decodes a plane-1 payload by family; nil for none (or one
// this build does not know).
func payloadFromWire(payload any) coord.EventPayload {
	if v, ok := runPayloadFromWire(payload); ok {
		return v
	}
	if v, ok := streamPayloadFromWire(payload); ok {
		return v
	}
	v, _ := sidePayloadFromWire(payload)
	return v
}

// runPayloadFromWire decodes the run-lifecycle payloads: run and step
// boundaries, status, interactions.
func runPayloadFromWire(payload any) (v coord.EventPayload, ok bool) {
	switch p := payload.(type) {
	case *agentcoordpb.AgentEvent_RunStarted:
		v = coord.RunStarted{
			Input:       structToMap(p.RunStarted.GetInput()),
			Agent:       agentIdentityFromWire(p.RunStarted.GetAgent()),
			Config:      structToMap(p.RunStarted.GetConfig()),
			ParentRunID: p.RunStarted.GetParentRunId(),
		}
	case *agentcoordpb.AgentEvent_StepStarted:
		v = coord.StepStarted{StepID: p.StepStarted.GetStepId(), Title: p.StepStarted.GetTitle(), Ordinal: p.StepStarted.GetOrdinal()}
	case *agentcoordpb.AgentEvent_StepCompleted:
		v = coord.StepCompleted{StepID: p.StepCompleted.GetStepId(), Outcome: parsed(coord.ParseStepOutcome(p.StepCompleted.GetOutcome().String())), Detail: p.StepCompleted.GetDetail()}
	case *agentcoordpb.AgentEvent_StatusChanged:
		v = coord.StatusChanged{Phase: parsed(coord.ParseRunPhase(p.StatusChanged.GetPhase().String())), Detail: p.StatusChanged.GetDetail()}
	case *agentcoordpb.AgentEvent_Interaction:
		v = coord.InteractionRecorded{
			RequestID:  p.Interaction.GetRequestId(),
			Kind:       p.Interaction.GetKind(),
			Resolution: parsed(coord.ParseInteractionResolution(p.Interaction.GetResolution().String())),
			Detail:     structToMap(p.Interaction.GetDetail()),
		}
	case *agentcoordpb.AgentEvent_RunCompleted:
		v = coord.RunCompleted{Result: resultFromWire(p.RunCompleted.GetResult())}
	default:
		return nil, false
	}
	return v, true
}

// streamPayloadFromWire decodes the streamed message and tool-call payloads.
func streamPayloadFromWire(payload any) (v coord.EventPayload, ok bool) {
	switch p := payload.(type) {
	case *agentcoordpb.AgentEvent_MessageStarted:
		v = coord.MessageStarted{
			MessageID: p.MessageStarted.GetMessageId(),
			Role:      parsed(coord.ParseMessageRole(p.MessageStarted.GetRole().String())),
			Channel:   parsed(coord.ParseMessageChannel(p.MessageStarted.GetChannel().String())),
		}
	case *agentcoordpb.AgentEvent_MessageDelta:
		v = coord.MessageDelta{MessageID: p.MessageDelta.GetMessageId(), Text: p.MessageDelta.GetText()}
	case *agentcoordpb.AgentEvent_MessageCompleted:
		v = coord.MessageCompleted{MessageID: p.MessageCompleted.GetMessageId(), FullText: p.MessageCompleted.GetFullText()}
	case *agentcoordpb.AgentEvent_ToolCallStarted:
		v = coord.ToolCallStarted{ToolCallID: p.ToolCallStarted.GetToolCallId(), ToolName: p.ToolCallStarted.GetToolName()}
	case *agentcoordpb.AgentEvent_ToolCallArgsDelta:
		v = coord.ToolCallArgsDelta{ToolCallID: p.ToolCallArgsDelta.GetToolCallId(), ArgsJSONFragment: p.ToolCallArgsDelta.GetArgsJsonFragment()}
	case *agentcoordpb.AgentEvent_ToolCallCompleted:
		v = coord.ToolCallCompleted{
			ToolCallID:  p.ToolCallCompleted.GetToolCallId(),
			Args:        structToMap(p.ToolCallCompleted.GetArgs()),
			IsError:     p.ToolCallCompleted.GetIsError(),
			ResultText:  p.ToolCallCompleted.GetResultText(),
			ArtifactIDs: p.ToolCallCompleted.GetArtifactIds(),
			Elapsed:     durationFromWire(p.ToolCallCompleted.GetElapsed()),
		}
	default:
		return nil, false
	}
	return v, true
}

// sidePayloadFromWire decodes the out-of-band payloads: artifacts, summaries,
// loss markers, raw and custom events.
func sidePayloadFromWire(payload any) (v coord.EventPayload, ok bool) {
	switch p := payload.(type) {
	case *agentcoordpb.AgentEvent_ArtifactProduced:
		v = ArtifactProducedFromWire(p.ArtifactProduced)
	case *agentcoordpb.AgentEvent_Summary:
		v = SummaryFromWire(p.Summary)
	case *agentcoordpb.AgentEvent_EventsLost:
		lost := coord.EventsLost{}
		for _, r := range p.EventsLost.GetLost() {
			lost.Lost = append(lost.Lost, coord.LostRange{RunID: r.GetRunId(), FirstSeq: r.GetFirstSeq(), LastSeq: r.GetLastSeq()})
		}
		v = lost
	case *agentcoordpb.AgentEvent_Raw:
		v = coord.RawEvent{Source: p.Raw.GetSource(), Event: structToMap(p.Raw.GetEvent())}
	case *agentcoordpb.AgentEvent_Custom:
		v = coord.CustomEvent{Name: p.Custom.GetName(), Value: structToMap(p.Custom.GetValue())}
	default:
		return nil, false
	}
	return v, true
}

// EventToWire encodes one plane-1 event.
func EventToWire(e coord.Event) *agentcoordpb.AgentEvent {
	ev := &agentcoordpb.AgentEvent{
		TaskId:       e.TaskID,
		RunId:        e.RunID,
		Seq:          e.Seq,
		OccurredAt:   timeToWire(e.OccurredAt),
		TurnId:       e.TurnID,
		ParentItemId: e.ParentItemID,
		Traceparent:  e.Traceparent,
	}
	setPayload(ev, e.Payload)
	return ev
}

// setPayload encodes a plane-1 payload onto ev by family; a nil (or
// unknown) payload leaves ev's unset.
func setPayload(ev *agentcoordpb.AgentEvent, payload coord.EventPayload) {
	if setRunPayload(ev, payload) || setStreamPayload(ev, payload) {
		return
	}
	setSidePayload(ev, payload)
}

// setRunPayload encodes the run-lifecycle payloads onto ev, reporting whether
// payload was one.
func setRunPayload(ev *agentcoordpb.AgentEvent, payload coord.EventPayload) bool {
	switch p := payload.(type) {
	case coord.RunStarted:
		ev.Payload = &agentcoordpb.AgentEvent_RunStarted{RunStarted: &agentcoordpb.RunStarted{
			Input: mapToStruct(p.Input), Agent: agentIdentityToWire(p.Agent), Config: mapToStruct(p.Config), ParentRunId: p.ParentRunID,
		}}
	case coord.StepStarted:
		ev.Payload = &agentcoordpb.AgentEvent_StepStarted{StepStarted: &agentcoordpb.StepStarted{StepId: p.StepID, Title: p.Title, Ordinal: p.Ordinal}}
	case coord.StepCompleted:
		ev.Payload = &agentcoordpb.AgentEvent_StepCompleted{StepCompleted: &agentcoordpb.StepCompleted{
			StepId: p.StepID, Outcome: agentcoordpb.StepCompleted_Outcome(agentcoordpb.StepCompleted_Outcome_value[string(p.Outcome)]), Detail: p.Detail,
		}}
	case coord.StatusChanged:
		ev.Payload = &agentcoordpb.AgentEvent_StatusChanged{StatusChanged: &agentcoordpb.StatusChanged{
			Phase: agentcoordpb.StatusChanged_Phase(agentcoordpb.StatusChanged_Phase_value[string(p.Phase)]), Detail: p.Detail,
		}}
	case coord.InteractionRecorded:
		ev.Payload = &agentcoordpb.AgentEvent_Interaction{Interaction: &agentcoordpb.InteractionRecorded{
			RequestId:  p.RequestID,
			Kind:       p.Kind,
			Resolution: agentcoordpb.InteractionRecorded_Resolution(agentcoordpb.InteractionRecorded_Resolution_value[string(p.Resolution)]),
			Detail:     mapToStruct(p.Detail),
		}}
	case coord.RunCompleted:
		ev.Payload = &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{Result: resultToWire(p.Result)}}
	default:
		return false
	}
	return true
}

// setStreamPayload encodes the streamed message and tool-call payloads onto
// ev, reporting whether payload was one.
func setStreamPayload(ev *agentcoordpb.AgentEvent, payload coord.EventPayload) bool {
	switch p := payload.(type) {
	case coord.MessageStarted:
		ev.Payload = &agentcoordpb.AgentEvent_MessageStarted{MessageStarted: &agentcoordpb.MessageStarted{
			MessageId: p.MessageID,
			Role:      agentcoordpb.MessageRole(agentcoordpb.MessageRole_value[string(p.Role)]),
			Channel:   agentcoordpb.MessageChannel(agentcoordpb.MessageChannel_value[string(p.Channel)]),
		}}
	case coord.MessageDelta:
		ev.Payload = &agentcoordpb.AgentEvent_MessageDelta{MessageDelta: &agentcoordpb.MessageDelta{MessageId: p.MessageID, Text: p.Text}}
	case coord.MessageCompleted:
		ev.Payload = &agentcoordpb.AgentEvent_MessageCompleted{MessageCompleted: &agentcoordpb.MessageCompleted{MessageId: p.MessageID, FullText: p.FullText}}
	case coord.ToolCallStarted:
		ev.Payload = &agentcoordpb.AgentEvent_ToolCallStarted{ToolCallStarted: &agentcoordpb.ToolCallStarted{ToolCallId: p.ToolCallID, ToolName: p.ToolName}}
	case coord.ToolCallArgsDelta:
		ev.Payload = &agentcoordpb.AgentEvent_ToolCallArgsDelta{ToolCallArgsDelta: &agentcoordpb.ToolCallArgsDelta{ToolCallId: p.ToolCallID, ArgsJsonFragment: p.ArgsJSONFragment}}
	case coord.ToolCallCompleted:
		ev.Payload = &agentcoordpb.AgentEvent_ToolCallCompleted{ToolCallCompleted: &agentcoordpb.ToolCallCompleted{
			ToolCallId: p.ToolCallID, Args: mapToStruct(p.Args), IsError: p.IsError, ResultText: p.ResultText, ArtifactIds: p.ArtifactIDs, Elapsed: durationToWire(p.Elapsed),
		}}
	default:
		return false
	}
	return true
}

// setSidePayload encodes the out-of-band payloads onto ev, reporting whether
// payload was one.
func setSidePayload(ev *agentcoordpb.AgentEvent, payload coord.EventPayload) bool {
	switch p := payload.(type) {
	case coord.ArtifactProduced:
		ev.Payload = &agentcoordpb.AgentEvent_ArtifactProduced{ArtifactProduced: ArtifactProducedToWire(p)}
	case coord.Summary:
		ev.Payload = &agentcoordpb.AgentEvent_Summary{Summary: SummaryToWire(p)}
	case coord.EventsLost:
		lost := &agentcoordpb.EventsLost{}
		for _, r := range p.Lost {
			lost.Lost = append(lost.Lost, &agentcoordpb.EventsLost_Range{RunId: r.RunID, FirstSeq: r.FirstSeq, LastSeq: r.LastSeq})
		}
		ev.Payload = &agentcoordpb.AgentEvent_EventsLost{EventsLost: lost}
	case coord.RawEvent:
		ev.Payload = &agentcoordpb.AgentEvent_Raw{Raw: &agentcoordpb.RawEvent{Source: p.Source, Event: mapToStruct(p.Event)}}
	case coord.CustomEvent:
		ev.Payload = &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{Name: p.Name, Value: mapToStruct(p.Value)}}
	default:
		return false
	}
	return true
}

func agentIdentityFromWire(a *agentcoordpb.AgentIdentity) *coord.AgentIdentity {
	if a == nil {
		return nil
	}
	return &coord.AgentIdentity{
		AgentID:        a.GetAgentId(),
		DisplayName:    a.GetDisplayName(),
		Harness:        a.GetHarness(),
		HarnessVersion: a.GetHarnessVersion(),
		Model:          a.GetModel(),
		Role:           a.GetRole(),
		RunnerID:       a.GetRunnerId(),
		ContainerName:  a.GetContainerName(),
	}
}

func agentIdentityToWire(a *coord.AgentIdentity) *agentcoordpb.AgentIdentity {
	if a == nil {
		return nil
	}
	return &agentcoordpb.AgentIdentity{
		AgentId:        a.AgentID,
		DisplayName:    a.DisplayName,
		Harness:        a.Harness,
		HarnessVersion: a.HarnessVersion,
		Model:          a.Model,
		Role:           a.Role,
		RunnerId:       a.RunnerID,
		ContainerName:  a.ContainerName,
	}
}

func resultFromWire(r *agentcoordpb.Result) *coord.Result {
	if r == nil {
		return nil
	}
	return &coord.Result{
		Status:           parsed(coord.ParseRunStatus(r.GetStatus().String())),
		Text:             r.GetText(),
		StructuredOutput: structToMap(r.GetStructuredOutput()),
		OutputSchemaID:   r.GetOutputSchemaId(),
		Error:            statusFromWire(r.GetError()),
		Retryable:        r.GetRetryable(),
		Usage:            usageFromWire(r.GetUsage()),
		WallTime:         durationFromWire(r.GetWallTime()),
		NumTurns:         r.GetNumTurns(),
		ArtifactIDs:      r.GetArtifactIds(),
		ExitCode:         r.ExitCode,
	}
}

func resultToWire(r *coord.Result) *agentcoordpb.Result {
	if r == nil {
		return nil
	}
	return &agentcoordpb.Result{
		Status:           agentcoordpb.Result_RunStatus(agentcoordpb.Result_RunStatus_value[string(r.Status)]),
		Text:             r.Text,
		StructuredOutput: mapToStruct(r.StructuredOutput),
		OutputSchemaId:   r.OutputSchemaID,
		Error:            statusToWire(r.Error),
		Retryable:        r.Retryable,
		Usage:            usageToWire(r.Usage),
		WallTime:         durationToWire(r.WallTime),
		NumTurns:         r.NumTurns,
		ArtifactIds:      r.ArtifactIDs,
		ExitCode:         r.ExitCode,
	}
}

func usageFromWire(u *agentcoordpb.Usage) *coord.Usage {
	if u == nil {
		return nil
	}
	out := &coord.Usage{
		InputTokens:              u.GetInputTokens(),
		OutputTokens:             u.GetOutputTokens(),
		CacheReadInputTokens:     u.GetCacheReadInputTokens(),
		CacheCreationInputTokens: u.GetCacheCreationInputTokens(),
		CostUSDMicros:            u.GetCostUsdMicros(),
	}
	if pm := u.GetPerModel(); len(pm) > 0 {
		out.PerModel = make(map[string]*coord.Usage, len(pm))
		for k, v := range pm {
			out.PerModel[k] = usageFromWire(v)
		}
	}
	return out
}

func usageToWire(u *coord.Usage) *agentcoordpb.Usage {
	if u == nil {
		return nil
	}
	out := &agentcoordpb.Usage{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
		CostUsdMicros:            u.CostUSDMicros,
	}
	if len(u.PerModel) > 0 {
		out.PerModel = make(map[string]*agentcoordpb.Usage, len(u.PerModel))
		for k, v := range u.PerModel {
			out.PerModel[k] = usageToWire(v)
		}
	}
	return out
}

// ArtifactProducedFromWire decodes an artifact manifest.
func ArtifactProducedFromWire(a *agentcoordpb.ArtifactProduced) coord.ArtifactProduced {
	out := coord.ArtifactProduced{
		ArtifactID:           a.GetArtifactId(),
		Revision:             a.GetRevision(),
		Kind:                 parsed(coord.ParseArtifactKind(a.GetKind().String())),
		Name:                 a.GetName(),
		MediaType:            a.GetMediaType(),
		SizeBytes:            a.GetSizeBytes(),
		SHA256:               a.GetSha256(),
		ProducedByItemID:     a.GetProducedByItemId(),
		Labels:               a.GetLabels(),
		AddressesFeedbackIDs: a.GetAddressesFeedbackIds(),
	}
	switch c := a.GetContent().(type) {
	case *agentcoordpb.ArtifactProduced_Inline:
		out.Inline = c.Inline
	case *agentcoordpb.ArtifactProduced_ExternalUri:
		out.ExternalURI = c.ExternalUri
	case *agentcoordpb.ArtifactProduced_UploadId:
		out.UploadID = c.UploadId
	}
	return out
}

// ArtifactProducedToWire encodes an artifact manifest.
func ArtifactProducedToWire(a coord.ArtifactProduced) *agentcoordpb.ArtifactProduced {
	out := &agentcoordpb.ArtifactProduced{
		ArtifactId:           a.ArtifactID,
		Revision:             a.Revision,
		Kind:                 agentcoordpb.ArtifactKind(agentcoordpb.ArtifactKind_value[string(a.Kind)]),
		Name:                 a.Name,
		MediaType:            a.MediaType,
		SizeBytes:            a.SizeBytes,
		Sha256:               a.SHA256,
		ProducedByItemId:     a.ProducedByItemID,
		Labels:               a.Labels,
		AddressesFeedbackIds: a.AddressesFeedbackIDs,
	}
	switch {
	case a.Inline != nil:
		out.Content = &agentcoordpb.ArtifactProduced_Inline{Inline: a.Inline}
	case a.ExternalURI != "":
		out.Content = &agentcoordpb.ArtifactProduced_ExternalUri{ExternalUri: a.ExternalURI}
	case a.UploadID != "":
		out.Content = &agentcoordpb.ArtifactProduced_UploadId{UploadId: a.UploadID}
	}
	return out
}

// SummaryFromWire decodes a report.
func SummaryFromWire(s *agentcoordpb.Summary) coord.Summary {
	return coord.Summary{
		Scope:            parsed(coord.ParseSummaryScope(s.GetScope().String())),
		StepID:           s.GetStepId(),
		Text:             s.GetText(),
		Structured:       structToMap(s.GetStructured()),
		CoversThroughSeq: s.GetCoversThroughSeq(),
		ArtifactIDs:      s.GetArtifactIds(),
		PublishPaths:     s.GetPublishPaths(),
	}
}

// SummaryToWire encodes a report.
func SummaryToWire(s coord.Summary) *agentcoordpb.Summary {
	return &agentcoordpb.Summary{
		Scope:            agentcoordpb.Summary_Scope(agentcoordpb.Summary_Scope_value[string(s.Scope)]),
		StepId:           s.StepID,
		Text:             s.Text,
		Structured:       mapToStruct(s.Structured),
		CoversThroughSeq: s.CoversThroughSeq,
		ArtifactIds:      s.ArtifactIDs,
		PublishPaths:     s.PublishPaths,
	}
}

// --- the runner plane -----------------------------------------------------------------

// RunnerHelloFromWire decodes a runner's handshake.
func RunnerHelloFromWire(h *agentcoordpb.RunnerHello) coord.RunnerHello {
	return coord.RunnerHello{
		Version:           h.GetVersion(),
		Harnesses:         h.GetHarnesses(),
		MaxConcurrentRuns: h.GetMaxConcurrentRuns(),
		Labels:            h.GetLabels(),
		ActiveRunIDs:      h.GetActiveRunIds(),
	}
}

// RunExitedFromWire decodes a runner's exit fact.
func RunExitedFromWire(x *agentcoordpb.RunExited) coord.RunExited {
	return coord.RunExited{
		RunID:             x.GetRunId(),
		ExitCode:          x.GetExitCode(),
		Signal:            x.GetSignal(),
		TerminalEventSeen: x.GetTerminalEventSeen(),
		HarnessSessionID:  x.GetHarnessSessionId(),
	}
}

// RunnerRequestToWire encodes a coordinator-initiated request; encodeLaunch
// is the launch codec (coordgrpc.EncodeLaunch), handed in because the
// launch's projection is the wire adapter's and not this file's.
func RunnerRequestToWire(req coord.RunnerRequest, encodeLaunch func(coord.StartRun) *agentcoordpb.Launch) *agentcoordpb.RunnerRequest {
	out := &agentcoordpb.RunnerRequest{RequestId: req.RequestID, Timeout: durationToWire(req.Timeout)}
	switch k := req.Kind.(type) {
	case coord.StartRun:
		out.Kind = &agentcoordpb.RunnerRequest_StartRun{StartRun: &agentcoordpb.StartRun{RunId: k.RunID, Launch: encodeLaunch(k)}}
	case coord.PauseRun:
		out.Kind = &agentcoordpb.RunnerRequest_PauseRun{PauseRun: &agentcoordpb.PauseRun{RunId: k.RunID, Reason: k.Reason}}
	case coord.ResumeRun:
		out.Kind = &agentcoordpb.RunnerRequest_ResumeRun{ResumeRun: &agentcoordpb.ResumeRun{RunId: k.RunID}}
	case coord.TurnRequest:
		out.Kind = &agentcoordpb.RunnerRequest_Turn{Turn: &agentcoordpb.Turn{Prompt: k.Turn.Prompt, Resume: k.Turn.Resume}}
	case coord.InterruptRun:
		out.Kind = &agentcoordpb.RunnerRequest_InterruptRun{InterruptRun: &agentcoordpb.InterruptRun{RunId: k.RunID}}
	case coord.StopRun:
		out.Kind = &agentcoordpb.RunnerRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: k.RunID, Reason: k.Reason, Grace: durationToWire(k.Grace)}}
	case coord.SetGrants:
		out.Kind = &agentcoordpb.RunnerRequest_SetGrants{SetGrants: &agentcoordpb.SetGrants{RunId: k.RunID, Rules: k.Rules}}
	}
	return out
}

// RunnerResponseFromWire decodes a runner's answer: a non-OK status becomes
// Err — UNAVAILABLE wrapped in ErrRunnerUnavailable, every other code a
// plain error carrying the runner's message.
func RunnerResponseFromWire(resp *agentcoordpb.RunnerResponse) coord.RunnerResponse {
	out := coord.RunnerResponse{RequestID: resp.GetRequestId()}
	if st := resp.GetStatus(); st.GetCode() != int32(codes.OK) {
		if st.GetCode() == int32(codes.Unavailable) {
			out.Err = fmt.Errorf("%w: %s", coord.ErrRunnerUnavailable, st.GetMessage())
		} else {
			out.Err = errors.New(st.GetMessage())
		}
	}
	switch k := resp.GetKind().(type) {
	case *agentcoordpb.RunnerResponse_StartRun:
		out.Kind = coord.StartRunResult{HarnessSessionID: k.StartRun.GetHarnessSessionId(), PID: k.StartRun.GetPid()}
	case *agentcoordpb.RunnerResponse_PauseRun:
		out.Kind = coord.PauseRunResult{NewlyPaused: k.PauseRun.GetNewlyPaused()}
	case *agentcoordpb.RunnerResponse_ResumeRun:
		out.Kind = coord.ResumeRunResult{NewlyResumed: k.ResumeRun.GetNewlyResumed()}
	case *agentcoordpb.RunnerResponse_Turn:
		out.Kind = coord.TurnResult{Result: engine.TurnResult{NativeKey: k.Turn.GetNativeKey(), Answer: k.Turn.GetAnswer()}}
	}
	return out
}

// --- the run plane --------------------------------------------------------------------

// RunHelloFromWire decodes a run's handshake.
func RunHelloFromWire(h *agentcoordpb.Hello) coord.RunHello {
	return coord.RunHello{
		TaskID:          h.GetTaskId(),
		RunID:           h.GetRunId(),
		Agent:           agentIdentityFromWire(h.GetAgent()),
		ResumeFromSeq:   h.GetResumeFromSeq(),
		ProtocolVersion: h.GetProtocolVersion(),
		Capabilities:    h.GetCapabilities(),
	}
}

// AgentRequestFromWire decodes one plane-2 request. Only the DECODE refuses
// here (a Struct value of the wrong JSON kind, a frame that cannot mean a
// request); what the request may say is each verb's Validate.
func AgentRequestFromWire(req *agentcoordpb.AgentRequest) (coord.AgentRequest, error) {
	out := coord.AgentRequest{RequestID: req.GetRequestId(), Timeout: durationFromWire(req.GetTimeout())}
	switch k := req.GetKind().(type) {
	case *agentcoordpb.AgentRequest_SpawnAgent:
		sr, err := spawnRequestFromWire(k.SpawnAgent)
		if err != nil {
			return out, err
		}
		out.Kind = sr
	case *agentcoordpb.AgentRequest_ListRuns:
		out.Kind = coord.RosterRequest{Role: k.ListRuns.GetRole(), IncludeTerminal: k.ListRuns.GetIncludeTerminal()}
	case *agentcoordpb.AgentRequest_StopRun:
		out.Kind = coord.StopRun{RunID: k.StopRun.GetRunId(), Reason: k.StopRun.GetReason(), Grace: durationFromWire(k.StopRun.GetGrace())}
	case *agentcoordpb.AgentRequest_ControlRun:
		cr, err := controlRequestFromWire(k.ControlRun)
		if err != nil {
			return out, err
		}
		out.Kind = cr
	case *agentcoordpb.AgentRequest_Host:
		hr, err := hostRequestFromWire(k.Host)
		if err != nil {
			return out, err
		}
		out.Kind = hr
	case *agentcoordpb.AgentRequest_Approval:
		ar, err := ApprovalRequestFromWire(k.Approval)
		if err != nil {
			return out, err
		}
		out.Kind = ar
	case *agentcoordpb.AgentRequest_PeerSend:
		return out, coord.ErrPeerSendIsLocal
	default:
		return out, coord.ErrUnsupportedRequest
	}
	return out, nil
}

// spawnRequestFromWire decodes a spawn: the role names the agent, and the
// input's prompt, workspace and dirty_tree_handler must each be a string when
// present.
func spawnRequestFromWire(sa *agentcoordpb.SpawnAgentRequest) (coord.SpawnRequest, error) {
	sr := coord.SpawnRequest{Agent: sa.GetRole()}
	in := sa.GetInput()
	if in == nil {
		return sr, nil
	}
	for key, dst := range map[string]*string{"prompt": &sr.Prompt, "workspace": &sr.Workspace, "dirty_tree_handler": &sr.DirtyTree} {
		v, err := spawnInputString(in, key)
		if err != nil {
			return coord.SpawnRequest{}, err
		}
		*dst = v
	}
	return sr, nil
}

// hostRequestFromWire decodes a host-tool call, its args re-encoded as JSON.
func hostRequestFromWire(h *agentcoordpb.HostRequest) (coord.HostRequest, error) {
	args, err := protojson.Marshal(h.GetArgs())
	if err != nil {
		return coord.HostRequest{}, fmt.Errorf("%s: decode args: %v", h.GetTool(), err)
	}
	return coord.HostRequest{Tool: h.GetTool(), Args: args}, nil
}

// spawnInputString reads a STRING value out of agent_run's free-form input
// Struct. A key that is absent yields "" — the caller said nothing, and every
// consumer treats that as "defer to the configured default".
//
// A key that is PRESENT but carries a non-string JSON value is an ERROR.
// structpb.Value.GetStringValue() answers "" for every other kind, which
// makes `{"dirty_tree_handler": 4}` indistinguishable from omitting the key —
// and these keys select postures whose unset path has a default that writes
// to the user's repository. Unset and unusable are different inputs and get
// different answers.
func spawnInputString(in *structpb.Struct, key string) (string, error) {
	v, ok := in.GetFields()[key]
	if !ok {
		return "", nil
	}
	sv, ok := v.GetKind().(*structpb.Value_StringValue)
	if !ok {
		return "", fmt.Errorf("agent_run: input.%s must be a string (got %s)", key, v.String())
	}
	return sv.StringValue, nil
}

// controlRequestFromWire decodes a ControlRun into the Control verb's request.
// The wire's per-tool argument refusals keep their wording: a missing harp or
// body is refused here, by the tool's name, before the verb sees it.
func controlRequestFromWire(req *agentcoordpb.ControlRun) (coord.ControlRequest, error) {
	switch v := req.GetVerb().(type) {
	case *agentcoordpb.ControlRun_Steer:
		req, err := bodyControl("agent_steer", coord.ControlVerbSteer, v.Steer.GetHarp(), "text", v.Steer.GetText())
		req.Interrupt = v.Steer.GetInterrupt()
		return req, err
	case *agentcoordpb.ControlRun_Question:
		return bodyControl("agent_ask", coord.ControlVerbQuestion, v.Question.GetHarp(), "text", v.Question.GetText())
	case *agentcoordpb.ControlRun_Summarize:
		return bodyControl("agent_summarize", coord.ControlVerbSummarize, v.Summarize.GetHarp(), "focus", v.Summarize.GetFocus())
	case *agentcoordpb.ControlRun_Pause:
		return bareControl("agent_pause", coord.ControlVerbPause, v.Pause.GetHarp(), v.Pause.GetReason())
	case *agentcoordpb.ControlRun_Resume:
		return bareControl("agent_resume", coord.ControlVerbResume, v.Resume.GetHarp(), "")
	default:
		return coord.ControlRequest{}, errors.New("control_run: no verb set — a ControlRun names exactly one of steer, question, summarize, pause, resume")
	}
}

// bodyControl is a control verb whose body (bodyField) is required.
func bodyControl(tool, verb, harp, bodyField, body string) (coord.ControlRequest, error) {
	if err := controlArgs(tool, harp, bodyField, &body); err != nil {
		return coord.ControlRequest{}, err
	}
	return coord.ControlRequest{Verb: verb, Harp: harp, Body: body}, nil
}

// bareControl is a control verb that requires only its harp; body is the
// optional reason it carries.
func bareControl(tool, verb, harp, body string) (coord.ControlRequest, error) {
	if err := controlArgs(tool, harp, "", nil); err != nil {
		return coord.ControlRequest{}, err
	}
	return coord.ControlRequest{Verb: verb, Harp: harp, Body: body}, nil
}

func controlArgs(tool, harp, bodyField string, body *string) error {
	if harp == "" {
		return errors.New(tool + ": harp is required (the child to control, from agent_run's harp or the roster)")
	}
	if body != nil && *body == "" {
		return fmt.Errorf("%s: %s is required", tool, bodyField)
	}
	return nil
}

// AgentReplyToWire encodes a plane-2 answer.
func AgentReplyToWire(r coord.AgentReply) *agentcoordpb.CoordinatorResponse {
	out := &agentcoordpb.CoordinatorResponse{RequestId: r.RequestID}
	if r.Err != nil {
		out.Status = StatusFromErr(r.Err)
		return out
	}
	out.Status = OKStatus(r.Message)
	switch res := r.Result.(type) {
	case coord.SpawnResult:
		out.Kind = &agentcoordpb.CoordinatorResponse_SpawnAgent{SpawnAgent: &agentcoordpb.SpawnAgentResult{ChildRunId: res.RunID, ChildAgentId: res.Harp}}
	case coord.RunsSnapshot:
		out.Kind = &agentcoordpb.CoordinatorResponse_ListRuns{ListRuns: RunsSnapshotToWire(res)}
	case coord.StopResult:
		result := &agentcoordpb.StopRunResult{}
		for _, sc := range res.Children {
			result.Children = append(result.Children, &agentcoordpb.StopRunResult_Child{
				Harp: sc.Harp, RunId: sc.RunID, Agent: sc.Agent, Outcome: sc.Outcome, Detail: sc.Detail,
			})
		}
		out.Kind = &agentcoordpb.CoordinatorResponse_StopRun{StopRun: result}
	case coord.ControlResult:
		cr, err := controlResultToWire(res)
		if err != nil {
			out.Status = StatusErr(codes.Internal, err.Error())
			return out
		}
		out.Kind = &agentcoordpb.CoordinatorResponse_ControlRun{ControlRun: cr}
	case coord.HostResult:
		body := &structpb.Struct{}
		if err := protojson.Unmarshal(res.Body, body); err != nil {
			out.Status = StatusErr(codes.Internal, fmt.Sprintf("encode result: %v", err))
			return out
		}
		out.Kind = &agentcoordpb.CoordinatorResponse_Host{Host: &agentcoordpb.HostResult{Body: body}}
	case coord.ApprovalDecision:
		d, err := ApprovalDecisionToWire(res)
		if err != nil {
			out.Status = StatusErr(codes.Internal, err.Error())
			return out
		}
		out.Kind = &agentcoordpb.CoordinatorResponse_Approval{Approval: d}
	}
	return out
}

func controlResultToWire(res coord.ControlResult) (*agentcoordpb.ControlRunResult, error) {
	switch res.Verb {
	case coord.ControlVerbSteer:
		return &agentcoordpb.ControlRunResult{Verb: &agentcoordpb.ControlRunResult_Steer{Steer: &agentcoordpb.ControlSteerResult{Delivery: res.Delivery, MessageId: res.MessageID}}}, nil
	case coord.ControlVerbQuestion, coord.ControlVerbSummarize:
		ans, err := askResultToWire(res.Answer)
		if err != nil {
			return nil, err
		}
		if res.Verb == coord.ControlVerbQuestion {
			return &agentcoordpb.ControlRunResult{Verb: &agentcoordpb.ControlRunResult_Question{Question: ans}}, nil
		}
		return &agentcoordpb.ControlRunResult{Verb: &agentcoordpb.ControlRunResult_Summarize{Summarize: ans}}, nil
	case coord.ControlVerbPause:
		return &agentcoordpb.ControlRunResult{Verb: &agentcoordpb.ControlRunResult_Pause{Pause: &agentcoordpb.ControlPauseResult{NewlyPaused: res.Changed}}}, nil
	case coord.ControlVerbResume:
		return &agentcoordpb.ControlRunResult{Verb: &agentcoordpb.ControlRunResult_Resume{Resume: &agentcoordpb.ControlResumeResult{NewlyResumed: res.Changed}}}, nil
	}
	return nil, fmt.Errorf("control_run: no result for verb %q", res.Verb)
}

func askResultToWire(ans *coord.AskAnswer) (*agentcoordpb.ControlAskResult, error) {
	if ans == nil {
		return &agentcoordpb.ControlAskResult{}, nil
	}
	out := &agentcoordpb.ControlAskResult{AskId: ans.AskID, From: ans.From, Text: ans.Text}
	if len(ans.Structured) > 0 {
		st := &structpb.Struct{}
		if err := st.UnmarshalJSON(ans.Structured); err != nil {
			return nil, fmt.Errorf("the answer from %s carried a structured companion that does not decode: %v", ans.From, err)
		}
		out.Structured = st
	}
	return out, nil
}

// OutFrameToWire encodes one coordinator-to-run frame.
func OutFrameToWire(f coord.OutFrame) *agentcoordpb.CoordinatorFrame {
	switch {
	case f.Ack != nil:
		return &agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_Ack{Ack: &agentcoordpb.Ack{CommittedSeq: f.Ack.CommittedSeq}}}
	case f.Reply != nil:
		return &agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_Response{Response: AgentReplyToWire(*f.Reply)}}
	case f.Notice != nil && f.Notice.SpoolChanged != nil:
		msg, err := SpoolChangedProto(*f.Notice.SpoolChanged)
		if err != nil {
			return nil
		}
		return &agentcoordpb.CoordinatorFrame{Kind: &agentcoordpb.CoordinatorFrame_Notice{Notice: &agentcoordpb.CoordinatorNotice{
			Kind: &agentcoordpb.CoordinatorNotice_SpoolChanged{SpoolChanged: msg},
		}}}
	}
	return nil
}

// --- projections ------------------------------------------------------------------------

// RunsSnapshotToWire encodes the roster.
func RunsSnapshotToWire(s coord.RunsSnapshot) *agentcoordpb.ListRunsResult {
	out := &agentcoordpb.ListRunsResult{}
	for _, r := range s.Runs {
		out.Runs = append(out.Runs, &agentcoordpb.ListRunsResult_RunInfo{
			TaskId:         r.TaskID,
			RunId:          r.RunID,
			Agent:          agentIdentityToWire(r.Agent),
			Phase:          r.Phase,
			LatestSummary:  r.LatestSummary,
			LastEventAt:    timeToWire(r.LastEventAt),
			ParentRunId:    r.ParentRunID,
			PermissionMode: r.PermissionMode,
			McpServers:     r.MCPServers,
		})
	}
	return out
}

// SpoolStatsToWire encodes the spool counters.
func SpoolStatsToWire(s coord.SpoolStats) *agentcoordpb.SpoolStatsResult {
	return &agentcoordpb.SpoolStatsResult{
		Delivered:        s.Delivered,
		Consumed:         s.Consumed,
		Failed:           s.Failed,
		DoorbellDropped:  s.DoorbellDropped,
		DoorbellRejected: s.DoorbellRejected,
	}
}

// ControlInitiatorKindFromWire decodes an initiator kind by name; a number
// this build does not declare crosses as the proto's rendering of it, which
// ControlInitiator.Validate refuses.
func ControlInitiatorKindFromWire(k agentcoordpb.ControlInitiatorKind) coord.ControlInitiatorKind {
	return parsed(coord.ParseControlInitiatorKind(k.String()))
}

// ControlInitiatorKindToWire encodes an initiator kind.
func ControlInitiatorKindToWire(k coord.ControlInitiatorKind) agentcoordpb.ControlInitiatorKind {
	return agentcoordpb.ControlInitiatorKind(agentcoordpb.ControlInitiatorKind_value[string(k)])
}

// --- mail --------------------------------------------------------------------------------

// PeerMessageToWire projects a mailbox message onto the wire shape. Kind
// rides the typed PeerMessage.kind field, spelled from the mailbox
// vocabulary; a kind outside that vocabulary is an ERROR rather than
// UNSPECIFIED, so a message nobody mapped cannot reach a recipient as
// "unset". Structured is the caller's companion carried verbatim — no key is
// merged into it, and the receive side reads no kind out of it.
//
// A payload that cannot be carried is an ERROR, not an empty result: the
// caller (the runner's in/ sweep) moves such a file to in/failed/ rather than
// delivering a hollow message.
func PeerMessageToWire(m coord.Message) (*agentcoordpb.PeerMessage, error) {
	kind, err := agentcoordpb.MessageKindForLegacyName(m.Kind)
	if err != nil {
		return nil, err
	}
	pm := &agentcoordpb.PeerMessage{
		MessageId:   m.ID,
		FromAgentId: m.From,
		Text:        m.Body,
		InReplyTo:   m.InReplyTo,
		Kind:        kind,
	}
	if len(m.Structured) > 0 {
		var fields map[string]any
		if err := json.Unmarshal(m.Structured, &fields); err != nil {
			return nil, fmt.Errorf("decode structured payload: %w", err)
		}
		if len(fields) > 0 {
			s, err := structpb.NewStruct(fields)
			if err != nil {
				return nil, fmt.Errorf("encode structured payload: %w", err)
			}
			pm.Structured = s
		}
	}
	return pm, nil
}

// SendRequestFromWire decodes the wire's PeerSendRequest into the verb's
// request: exactly one of to_agent_id / to_role names the recipient, the
// kind enum becomes its name, and the structured companion its JSON. Only
// the DECODE refuses here (a frame that cannot mean a request); what the
// request may say is Validate's.
func SendRequestFromWire(send *agentcoordpb.PeerSendRequest) (coord.SendRequest, error) {
	to := send.GetToAgentId()
	if role := send.GetToRole(); role != "" {
		if to != "" {
			return coord.SendRequest{}, fmt.Errorf("%w: agent_send: set exactly one of to_agent_id / to_role, not both", coord.ErrInvalidRequest)
		}
		to = role
	}
	sr := coord.SendRequest{To: to, Body: send.GetText(), InReplyTo: send.GetInReplyTo()}
	if k := send.GetKind(); k != agentcoordpb.MessageKind_MESSAGE_KIND_UNSPECIFIED {
		// proto3 enums are OPEN on the wire: a number this build does not
		// declare survives Unmarshal as itself, and it must be refused BY
		// NUMBER here — never mapped to "" and then answered as "kind is
		// required", which would hide which value was wrong.
		sr.Kind = agentcoordpb.LegacyKindName(k)
		if sr.Kind == "" {
			return coord.SendRequest{}, fmt.Errorf("%w: agent_send: kind %d is not a message kind this build knows; use one of: %s",
				coord.ErrInvalidRequest, int32(k), strings.Join(coord.SenderMailKinds(), " | "))
		}
	}
	if st := send.GetStructured(); st != nil {
		raw, err := protojson.Marshal(st)
		if err != nil {
			return coord.SendRequest{}, fmt.Errorf("%w: agent_send: structured payload cannot be encoded, refusing to send it stripped: %v", coord.ErrInvalidRequest, err)
		}
		sr.Structured = raw
	}
	return sr, nil
}

// --- spool doorbells ----------------------------------------------------------------------

var spoolDirToWire = map[spool.Dir]agentcoordpb.SpoolDir{
	spool.DirIn:          agentcoordpb.SpoolDir_SPOOL_DIR_IN,
	spool.DirOut:         agentcoordpb.SpoolDir_SPOOL_DIR_OUT,
	spool.DirInConsumed:  agentcoordpb.SpoolDir_SPOOL_DIR_IN_CONSUMED,
	spool.DirOutConsumed: agentcoordpb.SpoolDir_SPOOL_DIR_OUT_CONSUMED,
	spool.DirInWithdrawn: agentcoordpb.SpoolDir_SPOOL_DIR_IN_WITHDRAWN,
}

var spoolDirFromWire = func() map[agentcoordpb.SpoolDir]spool.Dir {
	inv := make(map[agentcoordpb.SpoolDir]spool.Dir, len(spoolDirToWire))
	for d, w := range spoolDirToWire {
		if prev, dup := inv[w]; dup {
			panic(fmt.Sprintf("coord: spool dir table maps %q and %q onto the same wire value %s", prev, d, w))
		}
		inv[w] = d
	}
	return inv
}()

// SpoolDirToWire projects a spool directory onto the wire enum. An unknown
// directory is an error, never SPOOL_DIR_UNSPECIFIED: the zero value is
// invalid at every consumer, so encoding an unmappable directory as it would
// turn a sender-side table gap into a receiver-side rejection far from the
// cause.
func SpoolDirToWire(d spool.Dir) (agentcoordpb.SpoolDir, error) {
	w, ok := spoolDirToWire[d]
	if !ok {
		return agentcoordpb.SpoolDir_SPOOL_DIR_UNSPECIFIED,
			fmt.Errorf("coord: spool directory %q has no wire representation", string(d))
	}
	return w, nil
}

// SpoolDirFromWire resolves a wire enum value to a spool directory.
// SPOOL_DIR_UNSPECIFIED and any value this build does not know are errors —
// forward compatibility for a doorbell means the receiver refuses it loudly,
// not that it guesses a directory.
func SpoolDirFromWire(w agentcoordpb.SpoolDir) (spool.Dir, error) {
	d, ok := spoolDirFromWire[w]
	if !ok {
		return "", fmt.Errorf("coord: unknown wire spool directory %s (%d)", w, int32(w))
	}
	return d, nil
}

// SpoolChangedProto projects a Ref onto the doorbell frame. The ref is
// validated first: a sender that rings about a name it could not itself
// resolve is a bug worth failing at the writer, where the stack still says who
// did it.
func SpoolChangedProto(ref spool.Ref) (*agentcoordpb.SpoolChanged, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	dir, err := SpoolDirToWire(ref.Dir)
	if err != nil {
		return nil, err
	}
	return &agentcoordpb.SpoolChanged{Harp: ref.Harp, Dir: dir, Name: ref.Name}, nil
}

// SpoolRefFromProto is THE receive chokepoint: it turns a wire doorbell into a
// Ref, or refuses it. Everything it accepts has passed spool.Ref.Validate —
// harp grammar, closed directory, bare-filename grammar — so no caller
// downstream has to re-check before resolving a path, and no unvalidated ref
// can reach a path join.
func SpoolRefFromProto(msg *agentcoordpb.SpoolChanged) (spool.Ref, error) {
	if msg == nil {
		return spool.Ref{}, fmt.Errorf("coord: spool doorbell carried no reference")
	}
	dir, err := SpoolDirFromWire(msg.GetDir())
	if err != nil {
		return spool.Ref{}, err
	}
	ref := spool.Ref{Harp: msg.GetHarp(), Dir: dir, Name: msg.GetName()}
	if err := ref.Validate(); err != nil {
		return spool.Ref{}, err
	}
	return ref, nil
}

// --- statuses -----------------------------------------------------------------------------

// OKStatus is an accepted answer carrying its disposition.
func OKStatus(msg string) *rpcstatus.Status {
	return &rpcstatus.Status{Code: int32(codes.OK), Message: msg}
}

// StatusErr is a refusal with an explicit code.
func StatusErr(code codes.Code, msg string) *rpcstatus.Status {
	return &rpcstatus.Status{Code: int32(code), Message: msg}
}

// StatusFromErr maps a verb's refusal onto a wire status: the code is chosen
// by the sentinel the error wraps, the message is the error's own. This is
// the ONE table between the coordinator's error vocabulary and the wire's
// codes.
func StatusFromErr(err error) *rpcstatus.Status {
	code := codes.Internal
	switch {
	case errors.Is(err, coord.ErrPeerRouting), errors.Is(err, coord.ErrControlRefused), errors.Is(err, coord.ErrNotAChild),
		errors.Is(err, coord.ErrRosterIsTheOwners), errors.Is(err, coord.ErrRunNotIssued), errors.Is(err, coord.ErrForbidden):
		code = codes.PermissionDenied
	case errors.Is(err, coord.ErrRecvTimeout), errors.Is(err, coord.ErrAskTimeout):
		code = codes.DeadlineExceeded
	case errors.Is(err, coord.ErrSenderMailKind), errors.Is(err, coord.ErrInvalidRequest):
		code = codes.InvalidArgument
	case errors.Is(err, coord.ErrDraining):
		code = codes.Unavailable
	case errors.Is(err, coord.ErrNotInjectable), errors.Is(err, coord.ErrNotFound):
		code = codes.NotFound
	case errors.Is(err, coord.ErrCapabilityUnavailable), errors.Is(err, coord.ErrAskUnavailable):
		code = codes.FailedPrecondition
	case errors.Is(err, coord.ErrNoHostApp), errors.Is(err, coord.ErrUnknownHostTool), errors.Is(err, coord.ErrUnsupportedRequest), errors.Is(err, coord.ErrPeerSendIsLocal):
		code = codes.Unimplemented
	case errors.Is(err, coord.ErrHostAnswerTooLarge):
		code = codes.ResourceExhausted
	}
	return StatusErr(code, err.Error())
}
