package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// ContextInjectionTimeout is the timeout for the context injection hook in seconds.
const ContextInjectionTimeout = 60

// NewContextInjectionHook creates the SessionStart hook that injects
// assembled context into the agent. The Command names the self-exec
// absolute path (CtxloomCommand) — see its doc for the staged-vs-installed
// invariant this upholds — shell-quoted like the project path, since both
// are interpolated into one /bin/sh command string.
//
// workDir is the project directory where the context file lives.
// Resolved to an absolute path because Claude Code can launch the
// hook from a different cwd.
func NewContextInjectionHook(hash, workDir string) wire.Hook {
	return wire.Hook{
		Command:     fmt.Sprintf("%s hook inject-context --project %s %s", shellSingleQuote(CtxloomCommand()), shellSingleQuote(absOrSelf(workDir)), hash),
		Type:        "command",
		Timeout:     ContextInjectionTimeout,
		ContextHash: hash,
	}
}

// NewContextInjectionChunkHook builds one of N ordered context-injection hooks.
// Each invocation emits a single sub-cap chunk (part k of total) and uses the
// flock rendezvous (AwaitTurn) to complete in order, so the harness — which
// injects parallel hook output in completion order — sees the chunks in
// sequence. See NewContextInjectionHooks for when chunking kicks in.
func NewContextInjectionChunkHook(hash, workDir string, part, total int) wire.Hook {
	return wire.Hook{
		Command: fmt.Sprintf("%s hook inject-context --project %s --part %d --of %d %s",
			shellSingleQuote(CtxloomCommand()), shellSingleQuote(absOrSelf(workDir)), part, total, hash),
		Type:        "command",
		Timeout:     ContextInjectionTimeout,
		ContextHash: hash,
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
	content, err := ReadContextFile(workDir, hash)
	if err != nil {
		// This was a bare `_`, so a read failure right after the
		// content-addressed file was written (or a reaped/corrupted cache)
		// silently collapsed N chunk hooks to one, reintroducing the exact
		// truncation ContextChunkMaxChars exists to prevent. The best-effort
		// single-hook fallback is still correct (the runtime hook re-reads the
		// file itself when it fires) — only the silence was the defect.
		Warn("context injection hook for %s: %v — falling back to a single whole-content hook", hash, err)
	}
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

// MergeHooksConfig merges source hooks into dest hooks. It is the nil-handling
// wrapper the host assembly (backends.AssembleManagedHooks) and the agent-side
// BaseLifecycle.MergeManaged both build on; the merge rule itself lives with
// the types, as wire.HooksConfig.Append.
// A nil src is a legitimate no-op (nothing to merge). A nil DEST is not: it is
// the caller's own hook set, so with none there is nowhere for src to go and this
// signature has no way to refuse. The drop still happens — it cannot not — but a
// non-empty set going missing is named on the diagnostic channel rather than
// leaving the session running with none of its configured hooks and nothing said.
// The same hook never runs twice in one event, whatever declared it:
// wire.HooksConfig.Append dedupes on the hook's whole executable content scoped
// to the event. Do not reintroduce a plain concatenation here or beside it — a
// diamond (child parents [b, c], both parenting d) applied d's hook once per
// path before that landed.
func MergeHooksConfig(dest *wire.HooksConfig, src *wire.HooksConfig) {
	if src == nil {
		return
	}
	if dest == nil {
		if n := countHooks(src); n > 0 {
			Warn("hook merge has no destination hook set: dropping %d configured hook(s); this is a caller error, not a configuration one", n)
		}
		return
	}

	dest.Append(*src)
}

// countHooks totals every hook a HooksConfig carries — the seven unified
// lifecycles plus every plugin-specific list — so a merge that cannot happen can
// report the SIZE of what it dropped rather than a bare "some hooks".
func countHooks(h *wire.HooksConfig) int {
	if h == nil {
		return 0
	}
	n := len(h.Unified.PreTool) + len(h.Unified.PostTool) +
		len(h.Unified.SessionStart) + len(h.Unified.SessionEnd) +
		len(h.Unified.PreShell) + len(h.Unified.PostFileEdit) + len(h.Unified.TurnEnd)
	for _, backend := range h.Plugins {
		for _, hooks := range backend {
			n += len(hooks)
		}
	}
	return n
}
