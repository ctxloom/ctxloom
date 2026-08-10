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
	if len(u.PreTool)+len(u.PostTool)+len(u.SessionStart)+len(u.SessionEnd)+len(u.PreShell)+len(u.PostFileEdit) > 0 {
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

// Append concatenates other onto h: each unified per-event slice, and each
// backend-native event list under its own backend key.
//
// The hooks half of this vocabulary owns its merge rule here, alongside the
// types it merges, for the same reason MergeMCPConfig does. A caller one layer
// up that re-spells the same appends by hand drifts in one direction only: a
// seventh unified event reaches Append and is silently dropped by the copy.
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
			h.Plugins[name][event] = append(h.Plugins[name][event], eventHooks...)
		}
	}
}

// Append concatenates each per-event slice from other onto u.
func (u *UnifiedHooks) Append(other UnifiedHooks) {
	u.PreTool = append(u.PreTool, other.PreTool...)
	u.PostTool = append(u.PostTool, other.PostTool...)
	u.SessionStart = append(u.SessionStart, other.SessionStart...)
	u.SessionEnd = append(u.SessionEnd, other.SessionEnd...)
	u.PreShell = append(u.PreShell, other.PreShell...)
	u.PostFileEdit = append(u.PostFileEdit, other.PostFileEdit...)
}
