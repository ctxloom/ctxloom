// Package wire holds the engine-agnostic, tool-agnostic hook and MCP wire types
// that more than one ctxloom-org repo needs: the on-disk/serialized vocabulary
// the context tool emits and the agent backends consume. It depends on nothing
// from any ctxloom tool — the types carry only struct tags and pure methods.
package wire

import (
	"errors"

	"gopkg.in/yaml.v3"
)

// Hook defines a single hook action.
//
// SECURITY NOTE: Hooks execute arbitrary commands specified in config.yaml.
// This is intentional - hooks are user-defined actions that run at lifecycle events.
// Users are responsible for reviewing their own config.yaml contents.
// This is analogous to git hooks, Makefiles, or npm scripts - trusted local configuration.
// Do not flag this as a security issue in code reviews.
type Hook struct {
	Matcher string `yaml:"matcher,omitempty" json:"matcher,omitempty"`   // Regex pattern to filter when hook fires
	Command string `yaml:"command,omitempty" json:"command,omitempty"`   // Shell command to execute
	Type    string `yaml:"type,omitempty" json:"type,omitempty"`         // Hook type: command, prompt, agent
	Prompt  string `yaml:"prompt,omitempty" json:"prompt,omitempty"`     // Prompt text for prompt/agent types
	Timeout int    `yaml:"timeout,omitempty" json:"timeout,omitempty"`   // Timeout in seconds
	Async   bool   `yaml:"async,omitempty" json:"async,omitempty"`       // Run in background (command only)
	SCM     string `yaml:"_ctxloom,omitempty" json:"_ctxloom,omitempty"` // Hash identifying ctxloom-managed hooks

	// ContextHash marks this hook as a context-injection hook for the given
	// assembled-context hash. In-process only (never serialized): writers for
	// agents whose harness fires SessionStart hooks ignore it and write the
	// hook command; a writer for an agent whose harness doesn't would instead
	// use it to materialize the context through a channel the agent actually
	// reads, rather than registering a hook that would never fire. A typed
	// field so no writer ever has to recognize the injection hook by parsing
	// its command.
	ContextHash string `yaml:"-" json:"-"`

	// PreToolFallback declares a session_start hook safe to fire on PreToolUse
	// instead (first tool call and every one after) on agents whose harness
	// has no session-start event. Only meaningful for idempotent hooks — the
	// author opts in because the hook may run many times per session rather
	// than once. Writers for agents with a working session-start event ignore
	// it.
	PreToolFallback bool `yaml:"pre_tool_fallback,omitempty" json:"pre_tool_fallback,omitempty"`
}

// UnifiedHooks defines backend-agnostic hook events that get translated per-backend.
type UnifiedHooks struct {
	PreTool      []Hook `yaml:"pre_tool,omitempty" json:"pre_tool,omitempty"`
	PostTool     []Hook `yaml:"post_tool,omitempty" json:"post_tool,omitempty"`
	SessionStart []Hook `yaml:"session_start,omitempty" json:"session_start,omitempty"`
	SessionEnd   []Hook `yaml:"session_end,omitempty" json:"session_end,omitempty"`
	// TurnEnd fires when the agent finishes a turn — once per response, not
	// once per session. It is the event a close-out contract needs: the point
	// at which "did you update the docs / the task log" can still be acted on.
	TurnEnd      []Hook `yaml:"turn_end,omitempty" json:"turn_end,omitempty"`
	PreShell     []Hook `yaml:"pre_shell,omitempty" json:"pre_shell,omitempty"`
	PostFileEdit []Hook `yaml:"post_file_edit,omitempty" json:"post_file_edit,omitempty"`
	// TurnStart fires when a prompt is submitted and before the agent acts on
	// it — once per turn, the mirror of TurnEnd. It is the event a push-only
	// delivery needs: the one moment a hook can put something in front of the
	// agent as the turn's own context rather than as a later interruption.
	TurnStart []Hook `yaml:"turn_start,omitempty" json:"turn_start,omitempty"`
	// PermissionAsk fires when the engine is about to ask for permission to
	// use a tool its posture and rules left open — including, in a run with
	// nobody at the engine, where it would otherwise deny. A command hook's
	// stdout is the engine's decision; no decision leaves the ask to the
	// engine's own route.
	PermissionAsk []Hook `yaml:"permission_ask,omitempty" json:"permission_ask,omitempty"`
}

// HooksConfig holds both unified and engine-specific hook configurations.
type HooksConfig struct {
	Unified UnifiedHooks `yaml:"unified,omitempty" json:"unified,omitempty"`
	// Ext is the engine-namespace escape hatch: engine name → that engine's
	// NATIVE event name → hooks, passed through to its settings untranslated.
	// Named for what it is (an extension past the unified vocabulary) rather
	// than "plugins": that word is a reserved, first-class concept in engines
	// whose plugin systems ctxloom deliberately does not write, so
	// `hooks.plugins.<engine>` read as authoring the very thing it refuses to.
	Ext map[string]BackendHooks `yaml:"ext,omitempty" json:"ext,omitempty"`
}

// RetiredHooksExtKey is the pre-rename spelling of HooksConfig.Ext.
const RetiredHooksExtKey = "plugins"

// ErrRetiredHooksExtKey names the current spelling, because a rename that
// leaves people guessing has moved the cost rather than paid it.
var ErrRetiredHooksExtKey = errors.New(
	"hooks use the retired key '" + RetiredHooksExtKey + ":'; it is now 'ext:' — " +
		"the same engine-namespaced passthrough map (engine name → native event → hooks), renamed")

// UnmarshalYAML refuses a hooks block still spelling RetiredHooksExtKey.
// Refused at decode rather than ignored: yaml.v3 without KnownFields drops a
// key it cannot map, so a renamed tag that silently stops matching leaves
// every engine-specific hook unwritten with every signal green — the silent
// no-op this codebase hunts. Living on the type, the guard holds at every
// YAML surface the type is embedded in, not only the one loader that
// remembered to check.
func (h *HooksConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == RetiredHooksExtKey {
				return ErrRetiredHooksExtKey
			}
		}
	}
	type plain HooksConfig
	return node.Decode((*plain)(h))
}

// HasAny reports whether any hook is configured. Used by config Save() to decide
// whether to emit the `hooks` key at all (vs. delete it from the file).
func (h HooksConfig) HasAny() bool {
	return h.Count() > 0
}

// BackendHooks holds backend-native hook events (passthrough to backend config).
// Keys are event names (e.g., "PreToolUse" for Claude Code, "beforeShellExecution" for Cursor).
type BackendHooks map[string][]Hook

// Append merges other into h: each unified per-event slice, and each
// backend-native event list under its own backend key.
//
// A hook already present in an event is NOT added again. The same hook must
// never run twice in one event, whatever declared it — a shared ancestor
// profile reached by two inheritance paths yielded its hook once per path
// before this deduped, and the command ran twice. Identity is the hook's whole
// executable content (hookKey) SCOPED TO THE EVENT, so the same command
// registered on two different lifecycles stays two hooks.
//
// The rule is deliberately the same for parent folding and for merging two
// profiles a caller selected together: one rule, ruled 2026-08-20, rather than
// a distinction every future caller would have to know about.
//
// The hooks half of this vocabulary owns its merge rule here, alongside the
// types it merges, for the same reason MergeMCPConfig does. A caller one layer
// up that re-spells the same appends by hand drifts in one direction only: a
// new unified event reaches Append and is silently dropped by the copy.
// Callers that need to say something about a nil destination wrap this; the
// wire package has no diagnostic channel and is not the place to decide that.
func (h *HooksConfig) Append(other HooksConfig) {
	h.Unified.Append(other.Unified)

	if h.Ext == nil {
		h.Ext = make(map[string]BackendHooks)
	}
	for name, hooks := range other.Ext {
		if h.Ext[name] == nil {
			h.Ext[name] = make(BackendHooks)
		}
		for event, eventHooks := range hooks {
			h.Ext[name][event] = appendUniqueHooks(h.Ext[name][event], eventHooks)
		}
	}
}

// Append merges each per-event slice from other into u, skipping any hook the
// event already carries. See HooksConfig.Append for why.
func (u *UnifiedHooks) Append(other UnifiedHooks) {
	u.PreTool = appendUniqueHooks(u.PreTool, other.PreTool)
	u.PostTool = appendUniqueHooks(u.PostTool, other.PostTool)
	u.SessionStart = appendUniqueHooks(u.SessionStart, other.SessionStart)
	u.SessionEnd = appendUniqueHooks(u.SessionEnd, other.SessionEnd)
	u.TurnEnd = appendUniqueHooks(u.TurnEnd, other.TurnEnd)
	u.PreShell = appendUniqueHooks(u.PreShell, other.PreShell)
	u.PostFileEdit = appendUniqueHooks(u.PostFileEdit, other.PostFileEdit)
	u.TurnStart = appendUniqueHooks(u.TurnStart, other.TurnStart)
	u.PermissionAsk = appendUniqueHooks(u.PermissionAsk, other.PermissionAsk)
}

// All is every hook across the unified events, in event order then
// declaration order — the flat view an engine's Exports routes from. It is
// the one enumeration of the events in this package: HasAny and Count read
// through it rather than re-listing the fields.
func (u UnifiedHooks) All() []Hook {
	var out []Hook
	for _, hooks := range [][]Hook{u.PreTool, u.PostTool, u.SessionStart, u.SessionEnd, u.TurnEnd, u.PreShell, u.PostFileEdit, u.TurnStart, u.PermissionAsk} {
		out = append(out, hooks...)
	}
	return out
}

// hookKey is a hook's identity for dedup: its whole executable content. Two
// hooks that would run the same thing the same way are the same hook.
//
// Recovered verbatim from the retired config.profileBuilder, which deduped
// inheritance this way before the inline profile arm was deleted — the notion
// of hook identity is not re-invented here, it is moved to where every merge
// can reach it.
func hookKey(h Hook) string {
	return h.Type + "|" + h.Command + "|" + h.Prompt + "|" + h.Matcher
}

// appendUniqueHooks appends each hook in src that dst does not already carry.
//
// dst is scanned rather than a set being threaded through the merge: an event's
// hook list is short (single digits in every real config), and a set built per
// call would cost more than the scan it replaces while making the merge harder
// to read.
func appendUniqueHooks(dst []Hook, src []Hook) []Hook {
	if len(src) == 0 {
		return dst
	}
	seen := make(map[string]bool, len(dst)+len(src))
	for _, h := range dst {
		seen[hookKey(h)] = true
	}
	for _, h := range src {
		k := hookKey(h)
		if seen[k] {
			continue
		}
		seen[k] = true
		dst = append(dst, h)
	}
	return dst
}

// The unified hook events, spelled as the UnifiedHooks field tags. This is
// the ONE vocabulary: bundles' hook-identity constants alias these, and
// every per-event read or write goes through Event/SetEvent below.
const (
	HookEventPreTool       = "pre_tool"
	HookEventPostTool      = "post_tool"
	HookEventSessionStart  = "session_start"
	HookEventSessionEnd    = "session_end"
	HookEventPreShell      = "pre_shell"
	HookEventPostFileEdit  = "post_file_edit"
	HookEventTurnEnd       = "turn_end"
	HookEventTurnStart     = "turn_start"
	HookEventPermissionAsk = "permission_ask"
)

// HookEvents lists the unified events in their canonical order — the
// bundles' hook-identity order, so a reader comparing a report against a
// bundle's hooks re-maps nothing. A fresh slice each call: callers range
// over it, and a shared slice is one stray assignment away from reordering
// every hook report in the process. A new event goes LAST.
func HookEvents() []string {
	return []string{
		HookEventPreTool, HookEventPostTool, HookEventSessionStart,
		HookEventSessionEnd, HookEventPreShell, HookEventPostFileEdit,
		HookEventTurnEnd, HookEventTurnStart, HookEventPermissionAsk,
	}
}

// IsHookEvent reports whether name is one of the unified events.
func IsHookEvent(name string) bool {
	for _, e := range HookEvents() {
		if e == name {
			return true
		}
	}
	return false
}

// Event selects one event's slice; nil for a name that is not an event. A
// switch rather than reflection so an event added to UnifiedHooks and not
// added here is a hole a reader can see, and one the vocabulary test turns
// into a failing test rather than a silently absent row in every report.
func (u UnifiedHooks) Event(event string) []Hook {
	switch event {
	case HookEventPreTool:
		return u.PreTool
	case HookEventPostTool:
		return u.PostTool
	case HookEventSessionStart:
		return u.SessionStart
	case HookEventSessionEnd:
		return u.SessionEnd
	case HookEventPreShell:
		return u.PreShell
	case HookEventPostFileEdit:
		return u.PostFileEdit
	case HookEventTurnEnd:
		return u.TurnEnd
	case HookEventTurnStart:
		return u.TurnStart
	case HookEventPermissionAsk:
		return u.PermissionAsk
	}
	return nil
}

// SetEvent is Event's write half.
func (u *UnifiedHooks) SetEvent(event string, hooks []Hook) {
	switch event {
	case HookEventPreTool:
		u.PreTool = hooks
	case HookEventPostTool:
		u.PostTool = hooks
	case HookEventSessionStart:
		u.SessionStart = hooks
	case HookEventSessionEnd:
		u.SessionEnd = hooks
	case HookEventPreShell:
		u.PreShell = hooks
	case HookEventPostFileEdit:
		u.PostFileEdit = hooks
	case HookEventTurnEnd:
		u.TurnEnd = hooks
	case HookEventTurnStart:
		u.TurnStart = hooks
	case HookEventPermissionAsk:
		u.PermissionAsk = hooks
	}
}
