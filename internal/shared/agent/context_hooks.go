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

// DefaultToolReflectBytes is the tool-result size at or above which a result
// is considered to carry information worth stating. It lives here because TWO
// mechanisms enforce the same policy and must not drift: the PostToolUse hook
// (which asks the agent to reflect, where the engine supports that event) and
// distillation (which decides whether a result's body can be reduced to its
// shape). Two copies of this number would be two policies.
//
// Measured, not chosen: across four real transcripts the median tool result is
// 447 bytes and 13% exceed 2KB, but those 13% carry roughly 1MB of body that
// distillation would otherwise discard.
const DefaultToolReflectBytes = 2048

// DefaultEssenceChars is the target size, in characters, of a finished
// session essence. It lives here for the same reason DefaultToolReflectBytes
// does: config resolves it and the compactor consumes it, and two copies of a
// number that must agree is how they stop agreeing.
//
// It is ABSOLUTE rather than a proportion of the transcript, and that is the
// whole point. The essence is re-injected into a FRESH context window on
// resume, and that window does not grow because the session was longer -- so a
// longer session needs MORE compression, not a longer essence. The proportional
// target this replaced ("30-50% of original size") was indexed to the wrong
// quantity and produced essences of 115KB, 170KB and 377KB against a 100,000
// char hard refusal ceiling.
//
// 10,000 codifies observed healthy behaviour rather than inventing a number:
// across 66 essences on disk the MEDIAN is 8,977 bytes. It is roughly 2,500
// tokens, about 1% of a 200k window.
const DefaultEssenceChars = 10_000

// ToolReflectTimeout is the timeout, in seconds, for the PostToolUse reflect
// hook. It is short because the hook does no I/O beyond reading its own stdin:
// a slow one would stall every tool call in the session.
const ToolReflectTimeout = 5

// NewToolReflectHook creates the PostToolUse hook that asks the agent to state
// what it learned from a large tool result. minBytes is resolved from config by
// the caller and interpolated here, so the threshold lives in one place rather
// than being re-decided inside the hook.
func NewToolReflectHook(minBytes int) wire.Hook {
	return wire.Hook{
		Command: fmt.Sprintf("%s hook tool-reflect --min-output-bytes %d",
			shellSingleQuote(CtxloomCommand()), minBytes),
		Type:    "command",
		Timeout: ToolReflectTimeout,
	}
}

// NextStepTimeout is the timeout, in seconds, for the TurnEnd next-step hook.
// Longer than ToolReflectTimeout because this hook READS THE TRANSCRIPT, which
// grows with the session; short enough that a stalled read cannot hold a turn
// open indefinitely.
const NextStepTimeout = 15

// NewNextStepHook creates the TurnEnd hook that captures what the agent was
// about to do next, so a later distillation can be task-aware instead of
// task-agnostic.
//
// TurnEnd is the seam and the choice is load-bearing. The capture has to
// happen while a live agent still holds the context; by session_end there is
// nobody left to ask, and session_end is additionally not the same event on
// every engine (kiro maps it to its per-TURN stop). Firing every turn and
// OVERWRITING is what makes that survivable: whatever the final turn said is
// what remains when the session ends, without anything having to detect that
// the session was ending.
//
// The hook takes no arguments. It resolves its harp from the environment at
// FIRE time (SessionHarpEnv) rather than having one interpolated here, because
// the installed command is written once — by apply-hooks, into settings that
// outlive the session that wrote them — and must serve every later session.
func NewNextStepHook() wire.Hook {
	return wire.Hook{
		Command: fmt.Sprintf("%s hook next-step", shellSingleQuote(CtxloomCommand())),
		Type:    "command",
		Timeout: NextStepTimeout,
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
