package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ctxloom/shared/wire"
)

// ContextInjectionTimeout is the timeout for the context injection hook in seconds.
const ContextInjectionTimeout = 60

// NewContextInjectionHook creates the SessionStart hook that injects
// assembled context into the agent. The Command is emitted as the bare
// name `ctxloom` so it re-resolves via PATH at fire time and never goes
// stale when the binary moves.
//
// workDir is the project directory where the context file lives.
// Resolved to an absolute path because Claude Code can launch the
// hook from a different cwd.
func NewContextInjectionHook(hash, workDir string) wire.Hook {
	return wire.Hook{
		Command: fmt.Sprintf("ctxloom hook inject-context --project %s %s", shellSingleQuote(absOrSelf(workDir)), hash),
		Type:    "command",
		Timeout: ContextInjectionTimeout,
	}
}

// NewContextInjectionChunkHook builds one of N ordered context-injection hooks.
// Each invocation emits a single sub-cap chunk (part k of total) and uses the
// flock rendezvous (AwaitTurn) to complete in order, so the harness — which
// injects parallel hook output in completion order — sees the chunks in
// sequence. See NewContextInjectionHooks for when chunking kicks in.
func NewContextInjectionChunkHook(hash, workDir string, part, total int) wire.Hook {
	return wire.Hook{
		Command: fmt.Sprintf("ctxloom hook inject-context --project %s --part %d --of %d %s",
			shellSingleQuote(absOrSelf(workDir)), part, total, hash),
		Type:    "command",
		Timeout: ContextInjectionTimeout,
	}
}

// absOrSelf resolves workDir to an absolute path (Claude Code may launch the
// hook from a different cwd), falling back to the input on error.
func absOrSelf(workDir string) string {
	if abs, err := filepath.Abs(workDir); err == nil {
		return abs
	}
	return workDir
}

// NewContextInjectionHooks returns the SessionStart context-injection hook(s)
// for the given content hash. It reads the (content-addressed, immutable)
// context file to decide the split: content that fits in one sub-cap chunk —
// or a missing/unreadable file — yields a single legacy whole-content hook;
// larger content yields N ordered chunk hooks. Reading the file here and in the
// hook with the same ChunkContext guarantees write-time and run-time agree on
// N. Best-effort by design: any read error falls back to the single hook (the
// runtime hook then emits nothing if the file is truly empty).
func NewContextInjectionHooks(hash, workDir string) []wire.Hook {
	content, _ := ReadContextFile(workDir, hash)
	chunks := ChunkContext(content)
	if len(chunks) <= 1 {
		return []wire.Hook{NewContextInjectionHook(hash, workDir)}
	}
	hooks := make([]wire.Hook, 0, len(chunks))
	for k := 1; k <= len(chunks); k++ {
		hooks = append(hooks, NewContextInjectionChunkHook(hash, workDir, k, len(chunks)))
	}
	return hooks
}

// shellSingleQuote wraps s in single quotes for safe interpolation into a
// /bin/sh command string, escaping embedded single quotes as the standard
// '\” idiom. Unlike double-quoting, single quotes neutralize spaces, $,
// backticks, and backslashes — so a project path containing any of those
// can't break the command split or inject shell behavior.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// MergeHooksConfig merges source hooks into dest hooks. It is the wire-only
// merge primitive the host assembly (backends.AssembleManagedHooks) and the
// agent-side BaseLifecycle.MergeManaged both build on, so it stays in the
// substrate even though config-coupled assembly moved host-side.
func MergeHooksConfig(dest *wire.HooksConfig, src *wire.HooksConfig) {
	if src == nil || dest == nil {
		return
	}

	// Merge unified hooks
	dest.Unified.PreTool = append(dest.Unified.PreTool, src.Unified.PreTool...)
	dest.Unified.PostTool = append(dest.Unified.PostTool, src.Unified.PostTool...)
	dest.Unified.SessionStart = append(dest.Unified.SessionStart, src.Unified.SessionStart...)
	dest.Unified.SessionEnd = append(dest.Unified.SessionEnd, src.Unified.SessionEnd...)
	dest.Unified.PreShell = append(dest.Unified.PreShell, src.Unified.PreShell...)
	dest.Unified.PostFileEdit = append(dest.Unified.PostFileEdit, src.Unified.PostFileEdit...)

	// Merge plugin-specific hooks
	if dest.Plugins == nil {
		dest.Plugins = make(map[string]wire.BackendHooks)
	}
	for name, hooks := range src.Plugins {
		if dest.Plugins[name] == nil {
			dest.Plugins[name] = make(wire.BackendHooks)
		}
		for event, eventHooks := range hooks {
			dest.Plugins[name][event] = append(dest.Plugins[name][event], eventHooks...)
		}
	}
}
