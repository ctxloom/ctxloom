package coordgrpc

import (
	"encoding/json"
	"fmt"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

var askKindToWire = map[engine.AskKind]agentcoordpb.ApprovalRequest_ApprovalKind{
	engine.AskTool:     agentcoordpb.ApprovalRequest_APPROVAL_KIND_TOOL,
	engine.AskQuestion: agentcoordpb.ApprovalRequest_APPROVAL_KIND_QUESTION,
	engine.AskPlan:     agentcoordpb.ApprovalRequest_APPROVAL_KIND_PLAN,
}

// ApprovalRequestToWire encodes a run's request for the root human.
func ApprovalRequestToWire(r coord.ApprovalRequest) *agentcoordpb.ApprovalRequest {
	ask := r.Ask
	out := &agentcoordpb.ApprovalRequest{
		Kind:            askKindToWire[ask.Kind],
		Tool:            ask.Tool,
		Input:           ask.Input,
		ToolUseId:       ask.ToolUseID,
		Suggestions:     ask.Suggestions,
		SuggestsSetMode: modeName(ask.SuggestsSetMode),
		Ceiling:         r.Ceiling.String(),
		Timeout:         durationToWire(r.Timeout),
	}
	if ask.Plan != nil {
		out.Plan = &agentcoordpb.PlanProposal{Markdown: ask.Plan.Markdown, Path: ask.Plan.Path}
	}
	for _, q := range ask.Questions {
		wq := &agentcoordpb.Question{Header: q.Header, Text: q.Text, MultiSelect: q.MultiSelect}
		for _, o := range q.Options {
			wq.Options = append(wq.Options, &agentcoordpb.QuestionOption{Label: o.Label, Description: o.Description})
		}
		out.Questions = append(out.Questions, wq)
	}
	return out
}

// ApprovalRequestFromWire decodes a run's request. A request the coordinator
// could not present as asked — no kind, no ceiling, a mode or input it cannot
// read — is refused rather than defaulted.
func ApprovalRequestFromWire(w *agentcoordpb.ApprovalRequest) (coord.ApprovalRequest, error) {
	var out coord.ApprovalRequest
	kind, ok := askKindFromWire(w.GetKind())
	if !ok {
		return out, fmt.Errorf("approval: kind %s is not a request this coordinator presents", w.GetKind())
	}
	ceiling, ok := engine.ParsePermissionMode(w.GetCeiling())
	if !ok {
		return out, fmt.Errorf("approval: ceiling %q is not a permission mode", w.GetCeiling())
	}
	suggests, err := modeFromWire("suggests_set_mode", w.GetSuggestsSetMode())
	if err != nil {
		return out, err
	}
	if in := w.GetInput(); len(in) > 0 && !json.Valid(in) {
		return out, fmt.Errorf("approval: input is not JSON")
	}
	out = coord.ApprovalRequest{
		Ask: engine.PermissionAsk{
			Kind:            kind,
			Tool:            w.GetTool(),
			Input:           w.GetInput(),
			ToolUseID:       w.GetToolUseId(),
			Suggestions:     w.GetSuggestions(),
			SuggestsSetMode: suggests,
		},
		Ceiling: ceiling,
		Timeout: durationFromWire(w.GetTimeout()),
	}
	if p := w.GetPlan(); p != nil {
		out.Ask.Plan = &engine.PlanProposal{Markdown: p.GetMarkdown(), Path: p.GetPath()}
	}
	for _, q := range w.GetQuestions() {
		eq := engine.Question{Header: q.GetHeader(), Text: q.GetText(), MultiSelect: q.GetMultiSelect()}
		for _, o := range q.GetOptions() {
			eq.Options = append(eq.Options, engine.QuestionOption{Label: o.GetLabel(), Description: o.GetDescription()})
		}
		out.Ask.Questions = append(out.Ask.Questions, eq)
	}
	return out, nil
}

func askKindFromWire(k agentcoordpb.ApprovalRequest_ApprovalKind) (engine.AskKind, bool) {
	for kind, wire := range askKindToWire {
		if wire == k {
			return kind, true
		}
	}
	return 0, false
}

// ApprovalDecisionToWire encodes the decision on a request; the decider
// travels by its vocabulary name.
func ApprovalDecisionToWire(d coord.ApprovalDecision) (*agentcoordpb.ApprovalDecision, error) {
	decider, err := d.Decider.MarshalText()
	if err != nil {
		return nil, fmt.Errorf("approval decision: %w", err)
	}
	out := &agentcoordpb.ApprovalDecision{
		Allow:        d.Allow,
		SessionRules: d.SessionRules,
		SetMode:      modeName(d.SetMode),
		Message:      d.Message,
		Decider:      string(decider),
	}
	for _, a := range d.Answers {
		out.Answers = append(out.Answers, &agentcoordpb.QuestionAnswer{Question: a.Question, Labels: a.Labels, Other: a.Other})
	}
	return out, nil
}

// ApprovalDecisionFromWire decodes the decision a run receives.
func ApprovalDecisionFromWire(w *agentcoordpb.ApprovalDecision) (coord.ApprovalDecision, error) {
	var out coord.ApprovalDecision
	if err := out.Decider.UnmarshalText([]byte(w.GetDecider())); err != nil {
		return coord.ApprovalDecision{}, fmt.Errorf("approval decision: %w", err)
	}
	setMode, err := modeFromWire("set_mode", w.GetSetMode())
	if err != nil {
		return coord.ApprovalDecision{}, err
	}
	out.Allow = w.GetAllow()
	out.SessionRules = w.GetSessionRules()
	out.SetMode = setMode
	out.Message = w.GetMessage()
	for _, a := range w.GetAnswers() {
		out.Answers = append(out.Answers, engine.QuestionAnswer{Question: a.GetQuestion(), Labels: a.GetLabels(), Other: a.GetOther()})
	}
	return out, nil
}

// modeName is a declared mode's wire spelling; "" when none is declared.
func modeName(d engine.Declared[engine.PermissionMode]) string {
	if m, ok := d.Get(); ok {
		return m.String()
	}
	return ""
}

// modeFromWire reads an optional mode: "" is none, anything else must name a
// mode.
func modeFromWire(field, name string) (engine.Declared[engine.PermissionMode], error) {
	if name == "" {
		return engine.Declared[engine.PermissionMode]{}, nil
	}
	m, ok := engine.ParsePermissionMode(name)
	if !ok {
		return engine.Declared[engine.PermissionMode]{}, fmt.Errorf("approval: %s %q is not a permission mode", field, name)
	}
	return engine.Provide(m), nil
}
