package coordgrpc

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/durationpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

func fullApprovalRequest() coord.ApprovalRequest {
	return coord.ApprovalRequest{
		Ask: engine.PermissionAsk{
			Kind:            engine.AskQuestion,
			Tool:            "AskUserQuestion",
			Input:           json.RawMessage(`{"questions":[{"question":"which?"}]}`),
			ToolUseID:       "toolu_9",
			Suggestions:     []string{"Bash(ls:*)"},
			SuggestsSetMode: engine.Provide("acceptEdits"),
			Plan:            &engine.PlanProposal{Markdown: "# p", Path: "/p.md"},
			Questions: []engine.Question{{
				Header: "Pick", Text: "which?", MultiSelect: true,
				Options: []engine.QuestionOption{{Label: "a", Description: "first"}, {Label: "b"}},
			}},
		},
		Transitions: []string{"default", "acceptEdits"},
		Timeout:     20 * time.Minute,
	}
}

// TestApprovalRequest_RoundTrips: every field the runner sends survives the
// wire unchanged.
func TestApprovalRequest_RoundTrips(t *testing.T) {
	want := fullApprovalRequest()
	wire := ApprovalRequestToWire(want)
	assert.Equal(t, agentcoordpb.ApprovalRequest_APPROVAL_KIND_QUESTION, wire.GetKind())
	assert.Equal(t, []string{"default", "acceptEdits"}, wire.GetTransitions())
	assert.Equal(t, "acceptEdits", wire.GetSuggestsSetMode())
	got, err := ApprovalRequestFromWire(wire)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	for _, kind := range []engine.AskKind{engine.AskTool, engine.AskQuestion, engine.AskPlan} {
		req := coord.ApprovalRequest{Ask: engine.PermissionAsk{Kind: kind}}
		back, err := ApprovalRequestFromWire(ApprovalRequestToWire(req))
		require.NoError(t, err)
		assert.Equal(t, kind, back.Ask.Kind)
		_, set := back.Ask.SuggestsSetMode.Get()
		assert.False(t, set, "no suggestion on the wire decodes as none")
	}
}

// TestApprovalRequestFromWire_Refuses: a request the coordinator cannot
// present faithfully is refused at decode, never defaulted.
func TestApprovalRequestFromWire_Refuses(t *testing.T) {
	valid := func() *agentcoordpb.ApprovalRequest { return ApprovalRequestToWire(fullApprovalRequest()) }
	for _, tc := range []struct {
		name  string
		mut   func(*agentcoordpb.ApprovalRequest)
		wants string
	}{
		{"unspecified-kind", func(r *agentcoordpb.ApprovalRequest) { r.Kind = agentcoordpb.ApprovalRequest_APPROVAL_KIND_UNSPECIFIED }, "kind"},
		{"unknown-kind", func(r *agentcoordpb.ApprovalRequest) { r.Kind = 99 }, "kind"},
		{"input-not-json", func(r *agentcoordpb.ApprovalRequest) { r.Input = []byte("{nope") }, "input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := valid()
			tc.mut(r)
			_, err := ApprovalRequestFromWire(r)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wants)
		})
	}
}

// TestApprovalDecision_RoundTrips: the decision reaches the runner whole, the
// decider by its vocabulary name.
func TestApprovalDecision_RoundTrips(t *testing.T) {
	want := coord.ApprovalDecision{
		Allow:        true,
		SessionRules: []string{"Bash(ls:*)"},
		SetMode:      engine.Provide("acceptEdits"),
		Answers:      []engine.QuestionAnswer{{Question: "which?", Labels: []string{"a", "b"}, Other: "and c"}},
		Message:      "go",
		Decider:      agent.DeciderHuman,
	}
	wire, err := ApprovalDecisionToWire(want)
	require.NoError(t, err)
	assert.Equal(t, "human", wire.GetDecider())
	assert.Equal(t, "acceptEdits", wire.GetSetMode())
	got, err := ApprovalDecisionFromWire(wire)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	deny, err := ApprovalDecisionToWire(coord.ApprovalDecision{Decider: agent.DeciderTimeout, Message: "no decision"})
	require.NoError(t, err)
	assert.Empty(t, deny.GetSetMode())
	back, err := ApprovalDecisionFromWire(deny)
	require.NoError(t, err)
	assert.Equal(t, agent.DeciderTimeout, back.Decider)
	_, set := back.SetMode.Get()
	assert.False(t, set)

	_, err = ApprovalDecisionFromWire(&agentcoordpb.ApprovalDecision{Decider: "a presenter"})
	assert.ErrorIs(t, err, agent.ErrUnknownDecider)
}

// TestAgentRequestFromWire_Approval: the approval arm reaches the
// coordinator as its typed request rather than as an unsupported kind.
func TestAgentRequestFromWire_Approval(t *testing.T) {
	req, err := AgentRequestFromWire(&agentcoordpb.AgentRequest{
		RequestId: "r-1",
		Timeout:   durationpb.New(time.Hour),
		Kind:      &agentcoordpb.AgentRequest_Approval{Approval: ApprovalRequestToWire(fullApprovalRequest())},
	})
	require.NoError(t, err)
	assert.Equal(t, fullApprovalRequest(), req.Kind)

	_, err = AgentRequestFromWire(&agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_Approval{Approval: &agentcoordpb.ApprovalRequest{}}})
	assert.Error(t, err, "a malformed approval is a decode refusal")
}

// TestAgentReplyToWire_Approval: the queue's decision is the reply's approval
// arm.
func TestAgentReplyToWire_Approval(t *testing.T) {
	resp := AgentReplyToWire(coord.AgentReply{RequestID: "r-1", Result: coord.ApprovalDecision{Allow: true, Decider: agent.DeciderHuman}})
	assert.Equal(t, "r-1", resp.GetRequestId())
	assert.True(t, resp.GetApproval().GetAllow())
	assert.Equal(t, "human", resp.GetApproval().GetDecider())

	bad := AgentReplyToWire(coord.AgentReply{Result: coord.ApprovalDecision{Decider: agent.Decider(99)}})
	assert.NotZero(t, bad.GetStatus().GetCode(), "an unencodable decision is an error status, not a silent allow")
	assert.Nil(t, bad.GetApproval())
}

// TestRunnerRequestToWire_SetGrants: a revoke reaches the runner as the full
// remaining grant set.
func TestRunnerRequestToWire_SetGrants(t *testing.T) {
	out := RunnerRequestToWire(coord.RunnerRequest{Kind: coord.SetGrants{RunID: "run-1", Rules: []string{"Bash(ls:*)"}}}, nil)
	assert.Equal(t, "run-1", out.GetSetGrants().GetRunId())
	assert.Equal(t, []string{"Bash(ls:*)"}, out.GetSetGrants().GetRules())
}

// TestNoWirePathAnswersAnApproval is the absence the root-human rule rests
// on: ApprovalDecision travels only coordinator → run, as a reply field, and
// no RPC names approvals — so nothing out of the root's process can answer
// one. A new field or method carrying a decision toward the coordinator fails
// here.
func TestNoWirePathAnswersAnApproval(t *testing.T) {
	decision := (&agentcoordpb.ApprovalDecision{}).ProtoReflect().Descriptor().FullName()
	var carriers []string
	var methods []string
	protoregistry.GlobalFiles.RangeFilesByPackage(agentcoordpb.File_coordination_proto.Package(), func(fd protoreflect.FileDescriptor) bool {
		var walk func(protoreflect.MessageDescriptors)
		walk = func(msgs protoreflect.MessageDescriptors) {
			for i := 0; i < msgs.Len(); i++ {
				md := msgs.Get(i)
				fields := md.Fields()
				for j := 0; j < fields.Len(); j++ {
					if m := fields.Get(j).Message(); m != nil && m.FullName() == decision {
						carriers = append(carriers, string(md.Name()))
					}
				}
				walk(md.Messages())
			}
		}
		walk(fd.Messages())
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			ms := svcs.Get(i).Methods()
			for j := 0; j < ms.Len(); j++ {
				m := ms.Get(j)
				if strings.Contains(strings.ToLower(string(m.Name())), "approv") ||
					m.Input().FullName() == decision || m.Output().FullName() == decision {
					methods = append(methods, string(m.FullName()))
				}
			}
		}
		return true
	})
	assert.Equal(t, []string{"CoordinatorResponse"}, carriers)
	assert.Empty(t, methods)
}
