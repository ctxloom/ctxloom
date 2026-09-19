package coord

import (
	"context"
	"errors"
	"fmt"

	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// THE AGENT INITIATOR'S TRANSPORT onto the control verbs: a ControlRun frame
// on plane 2 (the runner's agent_steer / agent_ask / agent_summarize /
// agent_pause / agent_resume tools) lands here and is answered by the SAME
// Coordinator.Control* verb the human's viewer path calls. This file adds no
// policy of its own beyond the edge's argument check: who may control whom is
// controlTarget's, and what each verb does is the verb's. The audit found a
// second orchestrator behind every existing agent_* tool; this one has one.

// serveControlRun answers one ControlRun. The initiator is derived from the
// requester's CREDENTIAL (caller), never from anything the request claims, so
// the ownership guard's whole input is a fact the coordinator established at
// Hello.
//
// It takes the coordinator's base ctx, which carries no deadline: each verb
// applies its own budget (controlRequestBudget / askBudget), and the verdict
// that budget produces — "unanswered, still in its spool" for an ask — is the
// one the caller must read, so the wire hop must not expire first (the runner
// side gives the ask tools a longer wire budget for exactly this reason).
func (c *Coordinator) serveControlRun(ctx context.Context, caller Identity, req *agentcoordpb.ControlRun) *agentcoordpb.CoordinatorResponse {
	by := ControlInitiator{Kind: agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_AGENT, Harp: caller.Harp}
	switch v := req.GetVerb().(type) {
	case *agentcoordpb.ControlRun_Steer:
		harp, text := v.Steer.GetHarp(), v.Steer.GetText()
		if st := controlArgs("agent_steer", harp, "text", &text); st != nil {
			return &agentcoordpb.CoordinatorResponse{Status: st}
		}
		out, err := c.ControlSteer(ctx, by, harp, text)
		if err != nil {
			return &agentcoordpb.CoordinatorResponse{Status: controlStatus("agent_steer", err)}
		}
		return controlResponse(fmt.Sprintf("steered %s (%s)", harp, out.Delivery), &agentcoordpb.ControlRunResult{
			Verb: &agentcoordpb.ControlRunResult_Steer{Steer: &agentcoordpb.ControlSteerResult{Delivery: out.Delivery, MessageId: out.MessageID}},
		})
	case *agentcoordpb.ControlRun_Question:
		harp, text := v.Question.GetHarp(), v.Question.GetText()
		if st := controlArgs("agent_ask", harp, "text", &text); st != nil {
			return &agentcoordpb.CoordinatorResponse{Status: st}
		}
		ans, err := c.ControlQuestion(ctx, by, harp, text)
		if err != nil {
			return &agentcoordpb.CoordinatorResponse{Status: controlStatus("agent_ask", err)}
		}
		result, st := askResult(ans)
		if st != nil {
			return &agentcoordpb.CoordinatorResponse{Status: st}
		}
		return controlResponse(fmt.Sprintf("%s answered", ans.From), &agentcoordpb.ControlRunResult{
			Verb: &agentcoordpb.ControlRunResult_Question{Question: result},
		})
	case *agentcoordpb.ControlRun_Summarize:
		harp, focus := v.Summarize.GetHarp(), v.Summarize.GetFocus()
		if st := controlArgs("agent_summarize", harp, "focus", &focus); st != nil {
			return &agentcoordpb.CoordinatorResponse{Status: st}
		}
		ans, err := c.ControlSummarize(ctx, by, harp, focus)
		if err != nil {
			return &agentcoordpb.CoordinatorResponse{Status: controlStatus("agent_summarize", err)}
		}
		result, st := askResult(ans)
		if st != nil {
			return &agentcoordpb.CoordinatorResponse{Status: st}
		}
		return controlResponse(fmt.Sprintf("%s summarized", ans.From), &agentcoordpb.ControlRunResult{
			Verb: &agentcoordpb.ControlRunResult_Summarize{Summarize: result},
		})
	case *agentcoordpb.ControlRun_Pause:
		harp := v.Pause.GetHarp()
		if st := controlArgs("agent_pause", harp, "", nil); st != nil {
			return &agentcoordpb.CoordinatorResponse{Status: st}
		}
		newly, err := c.ControlPause(ctx, by, harp, v.Pause.GetReason())
		if err != nil {
			return &agentcoordpb.CoordinatorResponse{Status: controlStatus("agent_pause", err)}
		}
		msg := fmt.Sprintf("%s was already paused", harp)
		if newly {
			msg = fmt.Sprintf("paused %s: its current turn finishes, nothing new is handed to it until agent_resume", harp)
		}
		return controlResponse(msg, &agentcoordpb.ControlRunResult{
			Verb: &agentcoordpb.ControlRunResult_Pause{Pause: &agentcoordpb.ControlPauseResult{NewlyPaused: newly}},
		})
	case *agentcoordpb.ControlRun_Resume:
		harp := v.Resume.GetHarp()
		if st := controlArgs("agent_resume", harp, "", nil); st != nil {
			return &agentcoordpb.CoordinatorResponse{Status: st}
		}
		newly, err := c.ControlResume(ctx, by, harp)
		if err != nil {
			return &agentcoordpb.CoordinatorResponse{Status: controlStatus("agent_resume", err)}
		}
		msg := fmt.Sprintf("%s was not paused", harp)
		if newly {
			msg = fmt.Sprintf("resumed %s: turns held at its gate are handed to it in arrival order", harp)
		}
		return controlResponse(msg, &agentcoordpb.ControlRunResult{
			Verb: &agentcoordpb.ControlRunResult_Resume{Resume: &agentcoordpb.ControlResumeResult{NewlyResumed: newly}},
		})
	default:
		return &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.InvalidArgument, "control_run: no verb set — a ControlRun names exactly one of steer, question, summarize, pause, resume")}
	}
}

// controlArgs is the wire edge's argument check: the target harp is required
// by every verb, and the verb's body (named by bodyField; nil for a verb
// that carries none) by the ones that have one. Refused here as
// INVALID_ARGUMENT naming the field, because the verb's own guard would
// otherwise report an empty harp as "not a child of this coordinator" — true,
// and useless to the caller who left it blank.
func controlArgs(tool, harp, bodyField string, body *string) *rpcstatus.Status {
	if harp == "" {
		return statusErr(codes.InvalidArgument, tool+": harp is required (the child to control, from agent_run's harp or the roster)")
	}
	if body != nil && *body == "" {
		return statusErr(codes.InvalidArgument, fmt.Sprintf("%s: %s is required", tool, bodyField))
	}
	return nil
}

// controlStatus maps a control verb's error onto a plane-2 status, on the
// verb's TYPED causes and nothing else: refused ownership is PERMISSION_DENIED,
// an unknown target NOT_FOUND, a run that cannot take the verb
// FAILED_PRECONDITION, an unanswered ask DEADLINE_EXCEEDED. The message is
// the verb's own, which already says what the caller should do next.
func controlStatus(tool string, err error) *rpcstatus.Status {
	code := codes.Internal
	switch {
	case errors.Is(err, ErrControlRefused):
		code = codes.PermissionDenied
	case errors.Is(err, ErrNotInjectable):
		code = codes.NotFound
	case errors.Is(err, ErrCapabilityUnavailable), errors.Is(err, ErrAskUnavailable):
		code = codes.FailedPrecondition
	case errors.Is(err, ErrAskTimeout):
		code = codes.DeadlineExceeded
	}
	return statusErr(code, tool+": "+err.Error())
}

// askResult projects one cooperative answer onto the wire. A structured
// companion that does not decode is an error rather than a silently absent
// field: the child attached it, and the asker would otherwise read "the
// child sent nothing structured".
func askResult(ans AskAnswer) (*agentcoordpb.ControlAskResult, *rpcstatus.Status) {
	out := &agentcoordpb.ControlAskResult{AskId: ans.AskID, From: ans.From, Text: ans.Text}
	if len(ans.Structured) > 0 {
		st := &structpb.Struct{}
		if err := st.UnmarshalJSON(ans.Structured); err != nil {
			return nil, statusErr(codes.Internal, fmt.Sprintf("the answer from %s carried a structured companion that does not decode: %v", ans.From, err))
		}
		out.Structured = st
	}
	return out, nil
}

func controlResponse(msg string, result *agentcoordpb.ControlRunResult) *agentcoordpb.CoordinatorResponse {
	return &agentcoordpb.CoordinatorResponse{
		Status: okStatus(msg),
		Kind:   &agentcoordpb.CoordinatorResponse_ControlRun{ControlRun: result},
	}
}
