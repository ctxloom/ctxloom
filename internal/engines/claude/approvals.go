package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// This file is claude's approval codec: the one place claude's
// PermissionRequest hook payload and answer are read and written. Shapes
// are the live ones (claude 2.1.283, hookcheck cells A and D; the no-host
// route is pinned by the P12 probe rung).

// hookEventPermissionRequest is the hook claude runs when a call would
// prompt; it carries the call's tool and input and claude's own
// suggestions, but no tool_use_id.
const hookEventPermissionRequest = "PermissionRequest"

// settingsDestinationSession is the ONLY destination an answer writes
// permissions to: any other one persists into a settings file.
const settingsDestinationSession = "session"

// Approvals is claude's approval codec.
func (c Claude) Approvals() engine.Declared[engine.ApprovalCodec] {
	return engine.Provide[engine.ApprovalCodec](approvalCodec{})
}

type approvalCodec struct{}

// hookAsk is the part of a PermissionRequest payload the codec reads.
type hookAsk struct {
	ToolName    string          `json:"tool_name"`
	ToolInput   json.RawMessage `json:"tool_input"`
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

var errNoToolName = errors.New("claude approval: the payload names no tool")

func errUnknownEvent(event string) error {
	return fmt.Errorf("claude approval: %q is not the approval hook event (%s)", event, hookEventPermissionRequest)
}

// DecodeAsk reads a PermissionRequest payload. Every ask is a tool call:
// with no permission prompt tool, claude -p offers neither AskUserQuestion
// nor ExitPlanMode, so a question or a plan never reaches this hook.
func (approvalCodec) DecodeAsk(event string, payload []byte) (engine.PermissionAsk, error) {
	if event != hookEventPermissionRequest {
		return engine.PermissionAsk{}, errUnknownEvent(event)
	}
	var p hookAsk
	if err := json.Unmarshal(payload, &p); err != nil {
		return engine.PermissionAsk{}, fmt.Errorf("claude approval payload: %w", err)
	}
	if p.ToolName == "" {
		return engine.PermissionAsk{}, errNoToolName
	}
	input, err := canonicalJSON(p.ToolInput)
	if err != nil {
		return engine.PermissionAsk{}, fmt.Errorf("claude approval tool_input: %w", err)
	}
	ask := engine.PermissionAsk{Kind: engine.AskTool, Tool: p.ToolName, Input: input}
	ask.Suggestions, ask.SuggestsSetMode = decodeSuggestions(p.Suggestions)
	return ask, nil
}

// decodeSuggestions keeps what a human may grant: the rules of claude's
// allow-rule suggestions (their destination is the encoder's to set, and it
// is always session), and an accept-edits or default mode change. Directory
// additions and deny rules are not grants; a bypass change is never offered.
func decodeSuggestions(in []suggestion) ([]string, engine.Declared[string]) {
	var rules []string
	var mode engine.Declared[string]
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

// settableMode is a mode an answer may switch the engine to: one an
// approved plan may continue at, in claude's spelling.
func settableMode(s string) (string, bool) {
	for _, m := range afterPlanModes() {
		if strings.EqualFold(strings.TrimSpace(s), m) {
			return m, true
		}
	}
	return "", false
}

// EncodeAnswer writes the hook's stdout for the decision.
func (approvalCodec) EncodeAnswer(event string, _ engine.PermissionAsk, a engine.PermissionAnswer) ([]byte, error) {
	if event != hookEventPermissionRequest {
		return nil, errUnknownEvent(event)
	}
	return encodePermissionRequest(a)
}

var errGrantOnDeny = errors.New("claude approval: a deny carries no session rules and no mode change")

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
		mode, settable := settableMode(m)
		if !settable {
			return nil, fmt.Errorf("claude approval: an answer may change mode only to %s, not %s", strings.Join(afterPlanModes(), " or "), m)
		}
		out = append(out, suggestion{Type: "setMode", Mode: mode, Destination: settingsDestinationSession})
	}
	return out, nil
}

func hookAnswer(event string, fields map[string]any) ([]byte, error) {
	fields["hookEventName"] = event
	return json.Marshal(map[string]any{"hookSpecificOutput": fields})
}

// Hooks are claude's approval hooks: the permission ask for every tool its
// posture and rules leave open (PermissionRequest, no matcher).
func (approvalCodec) Hooks(timeout time.Duration) wire.UnifiedHooks {
	return wire.UnifiedHooks{
		PermissionAsk: []wire.Hook{agent.ApprovalHook(hookEventPermissionRequest, "", timeout)},
	}
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
