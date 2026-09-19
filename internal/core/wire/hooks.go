// Package wire holds the engine-agnostic, tool-agnostic hook and MCP wire types
// that more than one ctxloom-org repo needs: the on-disk/serialized vocabulary
// the context tool emits and the agent backends consume. It depends on nothing
// from any ctxloom tool — the types carry only struct tags and pure methods.
package wire

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
	// it. No currently-registered backend lacks a session-start event (the
	// one that did, antigravity, was removed in 0.7.0); the field stays wired
	// for whichever future engine needs it next.
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
}

// HooksConfig holds both unified and backend-specific hook configurations.
type HooksConfig struct {
	Unified UnifiedHooks            `yaml:"unified,omitempty" json:"unified,omitempty"`
	Plugins map[string]BackendHooks `yaml:"plugins,omitempty" json:"plugins,omitempty"`
}

// HasAny reports whether any hook is configured. Used by config Save() to decide
// whether to emit the `hooks` key at all (vs. delete it from the file).
func (h HooksConfig) HasAny() bool {
	u := h.Unified
	if len(u.PreTool)+len(u.PostTool)+len(u.SessionStart)+len(u.SessionEnd)+len(u.TurnEnd)+len(u.PreShell)+len(u.PostFileEdit) > 0 {
		return true
	}
	for _, backend := range h.Plugins {
		for _, hooks := range backend {
			if len(hooks) > 0 {
				return true
			}
		}
	}
	return false
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
// eighth unified event reaches Append and is silently dropped by the copy.
// Callers that need to say something about a nil destination wrap this; the
// wire package has no diagnostic channel and is not the place to decide that.
func (h *HooksConfig) Append(other HooksConfig) {
	h.Unified.Append(other.Unified)

	if h.Plugins == nil {
		h.Plugins = make(map[string]BackendHooks)
	}
	for name, hooks := range other.Plugins {
		if h.Plugins[name] == nil {
			h.Plugins[name] = make(BackendHooks)
		}
		for event, eventHooks := range hooks {
			h.Plugins[name][event] = appendUniqueHooks(h.Plugins[name][event], eventHooks)
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
