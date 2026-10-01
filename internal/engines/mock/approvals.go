package mock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// Approvals is the mock's approval codec: a plain JSON vocabulary of its
// own (an ask names its tool and input; an answer its decision),
// so the approval route can be driven end to end without a real engine.
func (m Mock) Approvals() engine.Declared[engine.ApprovalCodec] {
	return engine.Provide[engine.ApprovalCodec](approvalCodec{})
}

type approvalCodec struct{}

// mockCall is the mock's ask payload.
type mockCall struct {
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input"`
}

var errMockNoTool = errors.New("mock approval: the payload names no tool")

func (c mockCall) decode() (string, json.RawMessage, error) {
	if c.Tool == "" {
		return "", nil, errMockNoTool
	}
	in, err := canonical(c.Input)
	return c.Tool, in, err
}

// DecodeAsk reads a mock ask: every mock ask is a tool call.
func (approvalCodec) DecodeAsk(_ string, payload []byte) (engine.PermissionAsk, error) {
	var c mockCall
	if err := json.Unmarshal(payload, &c); err != nil {
		return engine.PermissionAsk{}, fmt.Errorf("mock approval payload: %w", err)
	}
	tool, in, err := c.decode()
	if err != nil {
		return engine.PermissionAsk{}, err
	}
	return engine.PermissionAsk{Kind: engine.AskTool, Tool: tool, Input: in}, nil
}

// EncodeAnswer writes {allow, session_rules, set_mode, message}; a mode
// change the mock's model offers no transition to is refused.
func (approvalCodec) EncodeAnswer(_ string, _ engine.PermissionAsk, a engine.PermissionAnswer) ([]byte, error) {
	out := struct {
		Allow        bool     `json:"allow"`
		SessionRules []string `json:"session_rules,omitempty"`
		SetMode      string   `json:"set_mode,omitempty"`
		Message      string   `json:"message,omitempty"`
	}{Allow: a.Allow, SessionRules: a.SessionRules, Message: a.Message}
	if m, ok := a.SetMode.Get(); ok {
		var settable []string
		for _, tr := range (permissionModel{}).Transitions(nil) {
			settable = append(settable, tr.Posture)
		}
		if !slices.Contains(settable, m) {
			return nil, fmt.Errorf("mock approval: an answer may change mode only to %s, not %s", strings.Join(settable, "|"), m)
		}
		out.SetMode = m
	}
	return json.Marshal(out)
}

// Hooks are the mock's approval hooks: one permission_ask hook (the mock's
// native events are the unified ones) for every tool.
func (approvalCodec) Hooks(timeout time.Duration) wire.UnifiedHooks {
	return wire.UnifiedHooks{PermissionAsk: []wire.Hook{agent.ApprovalHook(wire.HookEventPermissionAsk, "", timeout)}}
}

// RepoSurfaces are the repository files the mock loads that can run code:
// its settings and its hook registrations.
func (approvalCodec) RepoSurfaces() []string { return []string{settingsRel, hooksRel} }

// ValidateRule accepts any one-line, non-blank rule: the mock has no rule
// syntax of its own to enforce.
func (approvalCodec) ValidateRule(rule string) error {
	if strings.TrimSpace(rule) == "" || strings.ContainsAny(rule, "\r\n") {
		return fmt.Errorf("mock permission rule %q: a rule is one non-blank line", rule)
	}
	return nil
}

// canonical re-encodes raw with sorted keys; absent input is {}.
func canonical(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
