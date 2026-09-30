package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// This file is claude's approval codec: the one place claude's
// PermissionRequest / PreToolUse hook payloads and answers, and its
// --permission-prompt-tool call and result, are read and written. Shapes
// are the live ones (claude 2.1.283, hookcheck cells A and D).

// hookEventPermissionRequest is the hook claude runs when a call would
// prompt; it carries the call's tool and input and claude's own
// suggestions, but no tool_use_id.
const hookEventPermissionRequest = "PermissionRequest"

// The tools a PreToolUse answer must satisfy with updatedInput: claude
// offers them to a -p run only when something can answer them, and an
// allow without updatedInput does not answer them.
const (
	toolAskUserQuestion = "AskUserQuestion"
	toolExitPlanMode    = "ExitPlanMode"
)

// settingsDestinationSession is the ONLY destination an answer writes
// permissions to: any other one persists into a settings file.
const settingsDestinationSession = "session"

// Approvals is claude's approval codec.
func (c Claude) Approvals() engine.Declared[engine.ApprovalCodec] {
	return engine.Provide[engine.ApprovalCodec](approvalCodec{})
}

type approvalCodec struct{}

// hookAsk is the part of a PermissionRequest / PreToolUse payload the codec
// reads.
type hookAsk struct {
	ToolName    string          `json:"tool_name"`
	ToolInput   json.RawMessage `json:"tool_input"`
	ToolUseID   string          `json:"tool_use_id"`
	Suggestions []suggestion    `json:"permission_suggestions"`
}

// suggestion is one of claude's permission_suggestions (and, written, one
// updatedPermissions entry).
type suggestion struct {
	Type        string       `json:"type"`
	Rules       []nativeRule `json:"rules,omitempty"`
	Behavior    string       `json:"behavior,omitempty"`
	Mode        string       `json:"mode,omitempty"`
	Destination string       `json:"destination"`
}

// nativeRule is claude's rule as its suggestions spell it: the rule string
// "Tool(content)" split in two.
type nativeRule struct {
	ToolName    string `json:"toolName"`
	RuleContent string `json:"ruleContent,omitempty"`
}

// String renders the rule in claude's settings syntax.
func (r nativeRule) String() string {
	if r.RuleContent == "" {
		return r.ToolName
	}
	return r.ToolName + "(" + r.RuleContent + ")"
}

var (
	errNoToolName  = errors.New("claude approval: the payload names no tool")
	errNoToolUseID = errors.New("claude approval: a PreToolUse payload carries no tool_use_id")
)

func errUnknownEvent(event string) error {
	return fmt.Errorf("claude approval: %q is not an approval hook event (%s, %s)", event, hookEventPermissionRequest, hookEventPreToolUse)
}

// DecodeAsk reads a PermissionRequest or PreToolUse payload. The kind is
// the tool's: AskUserQuestion is a question, ExitPlanMode a plan, anything
// else a tool call.
func (approvalCodec) DecodeAsk(event string, payload []byte) (engine.PermissionAsk, error) {
	p, input, err := readAsk(event, payload)
	if err != nil {
		return engine.PermissionAsk{}, err
	}
	ask := engine.PermissionAsk{Kind: engine.AskTool, Tool: p.ToolName, Input: input, ToolUseID: p.ToolUseID}
	ask.Suggestions, ask.SuggestsSetMode = decodeSuggestions(p.Suggestions)
	switch p.ToolName {
	case toolAskUserQuestion:
		ask.Kind = engine.AskQuestion
		ask.Questions, err = decodeQuestions(input)
	case toolExitPlanMode:
		ask.Kind = engine.AskPlan
		ask.Plan, err = decodePlan(input)
	}
	if err != nil {
		return engine.PermissionAsk{}, err
	}
	return ask, nil
}

// readAsk parses an approval hook payload for event and canonicalises its
// tool input.
func readAsk(event string, payload []byte) (hookAsk, json.RawMessage, error) {
	var p hookAsk
	if event != hookEventPermissionRequest && event != hookEventPreToolUse {
		return p, nil, errUnknownEvent(event)
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return p, nil, fmt.Errorf("claude approval payload: %w", err)
	}
	if err := p.validate(event); err != nil {
		return p, nil, err
	}
	input, err := canonicalJSON(p.ToolInput)
	if err != nil {
		return p, nil, fmt.Errorf("claude approval tool_input: %w", err)
	}
	return p, input, nil
}

// validate refuses a payload naming no tool, and a PreToolUse payload with
// no call id (PreToolUse always carries one).
func (p hookAsk) validate(event string) error {
	if p.ToolName == "" {
		return errNoToolName
	}
	if event == hookEventPreToolUse && p.ToolUseID == "" {
		return errNoToolUseID
	}
	return nil
}

// decodeSuggestions keeps what a human may grant: the rules of claude's
// allow-rule suggestions (their destination is the encoder's to set, and it
// is always session), and an accept-edits or default mode change. Directory
// additions and deny rules are not grants; a bypass change is never offered.
func decodeSuggestions(in []suggestion) ([]string, engine.Declared[engine.PermissionMode]) {
	var rules []string
	var mode engine.Declared[engine.PermissionMode]
	for _, s := range in {
		switch {
		case s.Type == "addRules" && s.Behavior == "allow":
			for _, r := range s.Rules {
				if r.ToolName != "" {
					rules = append(rules, r.String())
				}
			}
		case s.Type == "setMode":
			if m, ok := settableMode(s.Mode); ok {
				mode = engine.Provide(m)
			}
		}
	}
	return rules, mode
}

// settableMode is a mode an answer may switch the engine to.
func settableMode(s string) (engine.PermissionMode, bool) {
	m, ok := engine.ParsePermissionMode(s)
	if !ok || (m != engine.PermissionAcceptEdits && m != engine.PermissionDefault) {
		return 0, false
	}
	return m, true
}

// nativeQuestion is one AskUserQuestion question.
type nativeQuestion struct {
	Question string `json:"question"`
	Header   string `json:"header"`
	Options  []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
	MultiSelect bool `json:"multiSelect"`
}

func decodeQuestions(input json.RawMessage) ([]engine.Question, error) {
	var in struct {
		Questions []nativeQuestion `json:"questions"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("claude approval: AskUserQuestion input: %w", err)
	}
	out := make([]engine.Question, 0, len(in.Questions))
	for _, q := range in.Questions {
		eq := engine.Question{Header: q.Header, Text: q.Question, MultiSelect: q.MultiSelect}
		for _, o := range q.Options {
			eq.Options = append(eq.Options, engine.QuestionOption{Label: o.Label, Description: o.Description})
		}
		out = append(out, eq)
	}
	return out, nil
}

func decodePlan(input json.RawMessage) (*engine.PlanProposal, error) {
	var in struct {
		Plan         string `json:"plan"`
		PlanFilePath string `json:"planFilePath"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("claude approval: ExitPlanMode input: %w", err)
	}
	return &engine.PlanProposal{Markdown: in.Plan, Path: in.PlanFilePath}, nil
}

// EncodeAnswer writes the hook's stdout for the decision.
func (approvalCodec) EncodeAnswer(event string, ask engine.PermissionAsk, a engine.PermissionAnswer) ([]byte, error) {
	switch event {
	case hookEventPermissionRequest:
		return encodePermissionRequest(a)
	case hookEventPreToolUse:
		return encodePreToolUse(ask, a)
	default:
		return nil, errUnknownEvent(event)
	}
}

var (
	errGrantOnDeny   = errors.New("claude approval: a deny carries no session rules and no mode change")
	errGrantOnPre    = errors.New("claude approval: a PreToolUse answer cannot grant rules or change mode; that rides a PermissionRequest answer")
	errAnswersNotAsk = errors.New("claude approval: answers answer a question, and this ask is not one")
)

// encodePermissionRequest is `decision: {behavior, updatedPermissions |
// message}`. Every permission it writes is session-scoped.
func encodePermissionRequest(a engine.PermissionAnswer) ([]byte, error) {
	type decision struct {
		Behavior           string       `json:"behavior"`
		Message            string       `json:"message,omitempty"`
		UpdatedPermissions []suggestion `json:"updatedPermissions,omitempty"`
	}
	_, setMode := a.SetMode.Get()
	if !a.Allow {
		if len(a.SessionRules) > 0 || setMode {
			return nil, errGrantOnDeny
		}
		return hookAnswer(hookEventPermissionRequest, map[string]any{"decision": decision{Behavior: "deny", Message: a.Message}})
	}
	perms, err := sessionPermissions(a)
	if err != nil {
		return nil, err
	}
	return hookAnswer(hookEventPermissionRequest, map[string]any{"decision": decision{Behavior: "allow", UpdatedPermissions: perms}})
}

// sessionPermissions renders an allow's grants: one session addRules entry
// for its rules, one session setMode entry for its mode change.
func sessionPermissions(a engine.PermissionAnswer) ([]suggestion, error) {
	var out []suggestion
	if len(a.SessionRules) > 0 {
		add := suggestion{Type: "addRules", Behavior: "allow", Destination: settingsDestinationSession}
		for _, r := range a.SessionRules {
			nr, err := parseRule(r)
			if err != nil {
				return nil, err
			}
			add.Rules = append(add.Rules, nr)
		}
		out = append(out, add)
	}
	if m, ok := a.SetMode.Get(); ok {
		if _, settable := settableMode(m.String()); !settable {
			return nil, fmt.Errorf("claude approval: an answer may change mode only to %s or %s, not %s", engine.PermissionAcceptEdits, engine.PermissionDefault, m)
		}
		out = append(out, suggestion{Type: "setMode", Mode: m.String(), Destination: settingsDestinationSession})
	}
	return out, nil
}

// encodePreToolUse is `permissionDecision` (+ `updatedInput` on an allow of
// a question or plan, which claude requires; + the reason on a deny).
func encodePreToolUse(ask engine.PermissionAsk, a engine.PermissionAnswer) ([]byte, error) {
	if _, setMode := a.SetMode.Get(); setMode || len(a.SessionRules) > 0 {
		return nil, errGrantOnPre
	}
	if len(a.Answers) > 0 && ask.Kind != engine.AskQuestion {
		return nil, errAnswersNotAsk
	}
	if !a.Allow {
		return hookAnswer(hookEventPreToolUse, map[string]any{"permissionDecision": "deny", "permissionDecisionReason": a.Message})
	}
	out := map[string]any{"permissionDecision": "allow"}
	switch ask.Kind {
	case engine.AskQuestion:
		in, err := answeredInput(ask.Input, a.Answers)
		if err != nil {
			return nil, err
		}
		out["updatedInput"] = in
	case engine.AskPlan:
		out["updatedInput"] = ask.Input
	}
	return hookAnswer(hookEventPreToolUse, out)
}

// answeredInput is the question's input with `answers` set: each answer
// keyed by its question text (LIVE-D1), the chosen labels and any free text
// joined into one string.
func answeredInput(input json.RawMessage, answers []engine.QuestionAnswer) (json.RawMessage, error) {
	var in map[string]any
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("claude approval: AskUserQuestion input: %w", err)
	}
	keyed := make(map[string]string, len(answers))
	for _, a := range answers {
		parts := append([]string(nil), a.Labels...)
		if a.Other != "" {
			parts = append(parts, a.Other)
		}
		keyed[a.Question] = strings.Join(parts, ", ")
	}
	in["answers"] = keyed
	return json.Marshal(in)
}

func hookAnswer(event string, fields map[string]any) ([]byte, error) {
	fields["hookEventName"] = event
	return json.Marshal(map[string]any{"hookSpecificOutput": fields})
}

// HostCall reads a --permission-prompt-tool call's arguments.
func (approvalCodec) HostCall(args json.RawMessage) (engine.HostCall, error) {
	var in struct {
		ToolName  string          `json:"tool_name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return engine.HostCall{}, fmt.Errorf("claude permission host call: %w", err)
	}
	if in.ToolName == "" {
		return engine.HostCall{}, errNoToolName
	}
	input, err := canonicalJSON(in.Input)
	if err != nil {
		return engine.HostCall{}, fmt.Errorf("claude permission host call input: %w", err)
	}
	return engine.HostCall{Tool: in.ToolName, ToolUseID: in.ToolUseID, Input: input}, nil
}

// HostDeny is the permission host's deny: the JSON text claude reads from
// the tool's result.
func (approvalCodec) HostDeny(message string) (string, error) {
	b, err := json.Marshal(map[string]string{"behavior": "deny", "message": message})
	return string(b), err
}

// RepoSurfaces are the repository files claude loads that can run code or
// decide permissions: settings (hooks, rules), MCP servers, and the skill,
// agent and command trees.
func (approvalCodec) RepoSurfaces() []string {
	return []string{
		".claude/settings.json", ".claude/settings.local.json", ".mcp.json",
		".claude/skills/**", ".claude/agents/**", ".claude/commands/**",
	}
}

// ruleTool is a rule's tool: a built-in tool name, or an MCP server or
// server tool (mcp__<server> / mcp__<server>__<tool>).
var ruleTool = regexp.MustCompile(`^(?:[A-Za-z][A-Za-z0-9_]*|mcp__[A-Za-z0-9_-]+(?:__[A-Za-z0-9_-]+)?)$`)

// ValidateRule accepts claude's rule syntax: Tool, or Tool(content) with
// non-blank content on one line.
func (approvalCodec) ValidateRule(rule string) error {
	_, err := parseRule(rule)
	return err
}

// parseRule splits a rule into claude's toolName / ruleContent.
func parseRule(rule string) (nativeRule, error) {
	bad := func(why string) (nativeRule, error) {
		return nativeRule{}, fmt.Errorf("claude permission rule %q: %s (the syntax is Tool or Tool(content), e.g. Bash(npm test) or mcp__server__tool)", rule, why)
	}
	if strings.ContainsAny(rule, "\r\n") {
		return bad("a rule is one line")
	}
	tool, content, hasContent := strings.Cut(rule, "(")
	if hasContent {
		if !strings.HasSuffix(content, ")") {
			return bad("the content must close with )")
		}
		content = strings.TrimSuffix(content, ")")
		if strings.TrimSpace(content) == "" {
			return bad("the content is empty")
		}
	}
	if !ruleTool.MatchString(tool) || strings.TrimRight(tool, "_") == "mcp" {
		return bad("the tool name is not a tool")
	}
	return nativeRule{ToolName: tool, RuleContent: content}, nil
}

// canonicalJSON re-encodes raw with sorted keys, so two spellings of one
// input compare equal; absent input is the empty object.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
