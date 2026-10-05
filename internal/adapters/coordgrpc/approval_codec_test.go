package coordgrpc

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
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
		Transitions: []engine.PostureTransition{
			{Posture: "default", Label: "default"},
			{Posture: "acceptEdits", Label: "accept edits", Default: true},
		},
		Timeout: 20 * time.Minute,
	}
}

// TestApprovalRequest_RoundTrips: every field the runner sends survives the
// wire unchanged.
func TestApprovalRequest_RoundTrips(t *testing.T) {
	want := fullApprovalRequest()
	wire := ApprovalRequestToWire(want)
	assert.Equal(t, agentcoordpb.ApprovalRequest_APPROVAL_KIND_QUESTION, wire.GetKind())
	require.Len(t, wire.GetTransitions(), 2)
	assert.False(t, wire.GetTransitions()[0].GetDefault(), "the default is a flag, not the first offer")
	assert.True(t, wire.GetTransitions()[1].GetDefault())
	assert.Equal(t, "accept edits", wire.GetTransitions()[1].GetLabel())
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

// approvalWireRules walks every message and method in the coordination
// package once, for the clauses of TestNoWirePathAnswersAnApproval.
type approvalWireRules struct {
	decisionCarriers []string            // messages with an ApprovalDecision field
	methods          map[string][]string // service name → its method names
	decisionMethods  []string            // methods taking or returning an ApprovalDecision
}

func walkApprovalWire() approvalWireRules {
	decision := (&agentcoordpb.ApprovalDecision{}).ProtoReflect().Descriptor().FullName()
	out := approvalWireRules{methods: map[string][]string{}}
	protoregistry.GlobalFiles.RangeFilesByPackage(agentcoordpb.File_coordination_proto.Package(), func(fd protoreflect.FileDescriptor) bool {
		var walk func(protoreflect.MessageDescriptors)
		walk = func(msgs protoreflect.MessageDescriptors) {
			for i := 0; i < msgs.Len(); i++ {
				md := msgs.Get(i)
				fields := md.Fields()
				for j := 0; j < fields.Len(); j++ {
					if m := fields.Get(j).Message(); m != nil && m.FullName() == decision {
						out.decisionCarriers = append(out.decisionCarriers, string(md.Name()))
					}
				}
				walk(md.Messages())
			}
		}
		walk(fd.Messages())
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			svc := svcs.Get(i)
			ms := svc.Methods()
			for j := 0; j < ms.Len(); j++ {
				m := ms.Get(j)
				out.methods[string(svc.Name())] = append(out.methods[string(svc.Name())], string(m.Name()))
				if m.Input().FullName() == decision || m.Output().FullName() == decision {
					out.decisionMethods = append(out.decisionMethods, string(m.FullName()))
				}
			}
		}
		return true
	})
	return out
}

// TestNoWirePathAnswersAnApproval is the absence the root-human rule rests
// on: nothing out of the root's process can answer a parked request. Each
// clause fails on its own:
//
//	(i)   ApprovalDecision travels only coordinator → run, as a reply field;
//	(ii)  ConsumerService is exactly its reviewed methods, so a new one —
//	      a read that grows a write — fails until reviewed;
//	(iii) no CoordinatorService method names approvals, and no method of
//	      any service takes or returns a decision;
//	(iv)  nothing reachable from PendingApprovalsResult names a request
//	      (an id), carries the raw request, or carries a decision — a
//	      reader holds nothing an answer could address.
func TestNoWirePathAnswersAnApproval(t *testing.T) {
	w := walkApprovalWire()
	assert.Equal(t, []string{"CoordinatorResponse"}, w.decisionCarriers, "(i)")
	assert.ElementsMatch(t, []string{"WatchRuns", "ListRuns", "SpoolStats", "PendingApprovals"}, w.methods["ConsumerService"], "(ii)")
	for _, m := range w.methods["CoordinatorService"] {
		assert.NotContains(t, strings.ToLower(m), "approv", "(iii) CoordinatorService.%s", m)
	}
	assert.Empty(t, w.decisionMethods, "(iii)")
	assertNamesNoRequest(t, (&agentcoordpb.PendingApprovalsResult{}).ProtoReflect().Descriptor())
}

// assertNamesNoRequest is clause (iv): md and every message reachable from it
// has no id field and no field holding a request or a decision.
func assertNamesNoRequest(t *testing.T, md protoreflect.MessageDescriptor) {
	t.Helper()
	forbidden := map[protoreflect.FullName]bool{
		(&agentcoordpb.ApprovalRequest{}).ProtoReflect().Descriptor().FullName():  true,
		(&agentcoordpb.ApprovalDecision{}).ProtoReflect().Descriptor().FullName(): true,
	}
	seen := map[protoreflect.FullName]bool{}
	var walk func(protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		if seen[md.FullName()] {
			return
		}
		seen[md.FullName()] = true
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			f := fields.Get(i)
			name := string(f.Name())
			assert.False(t, name == "id" || strings.HasSuffix(name, "_id"), "(iv) %s.%s names a request", md.FullName(), name)
			if m := f.Message(); m != nil {
				assert.False(t, forbidden[m.FullName()], "(iv) %s.%s carries %s", md.FullName(), name, m.FullName())
				walk(m)
			}
		}
	}
	walk(md)
}

// TestPendingApprovalsToWire: the projection keeps the queue's order and
// carries who asked, through which lineage, the summary and the times — and
// nothing an answer could address: no id, no raw input, no workdir.
func TestPendingApprovalsToWire(t *testing.T) {
	since := time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC)
	ps := []coord.PendingApproval{
		{
			ID: "secret-id", Kind: coord.ApprovalTool, From: coord.Identity{Harp: "child-a", RunID: "run-a"}, Agent: "worker",
			Lineage: []string{"root", "child-a"}, WorkDir: "/w",
			Ask:   engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash", Input: json.RawMessage(`{"command":"make"}`)},
			Since: since, Deadline: since.Add(15 * time.Minute),
		},
		{
			ID: "other", Kind: coord.ApprovalQuestion, From: coord.Identity{Harp: "child-b"},
			Ask:   engine.PermissionAsk{Kind: engine.AskQuestion, Questions: []engine.Question{{Header: "Pick"}}},
			Since: since, Deadline: since.Add(20 * time.Minute),
		},
		{ID: "plan", Kind: coord.ApprovalPlan, From: coord.Identity{Harp: "child-c"}, Since: since, Deadline: since.Add(30 * time.Minute)},
	}
	out := PendingApprovalsToWire(ps, "/proj")
	assert.Equal(t, "/proj", out.GetProjectDir())
	require.Len(t, out.GetPending(), 3)
	a := out.GetPending()[0]
	assert.Equal(t, agentcoordpb.ApprovalRequest_APPROVAL_KIND_TOOL, a.GetKind())
	assert.Equal(t, "child-a", a.GetHarp())
	assert.Equal(t, "worker", a.GetAgent())
	assert.Equal(t, []string{"root", "child-a"}, a.GetLineage())
	assert.Equal(t, ps[0].Summary(), a.GetSummary())
	assert.True(t, since.Equal(a.GetSince().AsTime()))
	assert.True(t, ps[0].Deadline.Equal(a.GetDeadline().AsTime()))
	assert.Equal(t, agentcoordpb.ApprovalRequest_APPROVAL_KIND_QUESTION, out.GetPending()[1].GetKind())
	assert.Equal(t, "Pick", out.GetPending()[1].GetSummary())
	assert.Equal(t, agentcoordpb.ApprovalRequest_APPROVAL_KIND_PLAN, out.GetPending()[2].GetKind())

	raw, err := protojson.Marshal(out)
	require.NoError(t, err)
	for _, leak := range []string{"secret-id", "run-a", "/w", `"command"`} {
		assert.NotContains(t, string(raw), leak, "the wire form must not carry %q", leak)
	}
}

// TestPendingApprovalsToWire_SummaryIsRaw: the wire carries the child's own
// characters — a bidi override arrives as itself; making it safe is each
// viewer's job, for the place it shows it.
func TestPendingApprovalsToWire_SummaryIsRaw(t *testing.T) {
	p := coord.PendingApproval{Kind: coord.ApprovalTool, Ask: engine.PermissionAsk{Kind: engine.AskTool, Tool: "Bash", Input: json.RawMessage(`{"command":"ls \u202egnp.exe"}`)}}
	out := PendingApprovalsToWire([]coord.PendingApproval{p}, "/proj")
	assert.Equal(t, "Bash: ls \u202egnp.exe", out.GetPending()[0].GetSummary())
}

// TestPendingApprovalsToWire_Empty: nothing pending is an empty list, still
// naming the project.
func TestPendingApprovalsToWire_Empty(t *testing.T) {
	out := PendingApprovalsToWire(nil, "/proj")
	assert.Empty(t, out.GetPending())
	assert.Equal(t, "/proj", out.GetProjectDir())
}
