package agent

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/wire"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// ContextInjectionTimeout is the timeout for the context injection hook in seconds.
const ContextInjectionTimeout = 60

// NewContextInjectionHook creates the SessionStart hook that injects
// assembled context into the agent. The Command names the bare ctxloom
// executable (CtxloomCommand) and carries NO project path.
//
// INVARIANT: neither half of this command is a fact about the machine that
// wrote it. The generated settings file is tracked, so an absolute path in it
// is one developer's path that no other clone can satisfy — their hooks then
// succeed at doing nothing. The project is resolved at FIRE time instead, by
// cli.resolveInjectContextWorkDir: CTXLOOM_ROOT, else the git root containing
// cwd. The hook's own --project flag stays available for a human invoking it
// by hand; it is simply never emitted here.
func NewContextInjectionHook(hash string) wire.Hook {
	return wire.Hook{
		Command:     fmt.Sprintf("%s hook inject-context %s", shellSingleQuote(CtxloomCommand()), hash),
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
func NewContextInjectionChunkHook(hash string, part, total int) wire.Hook {
	return wire.Hook{
		Command: fmt.Sprintf("%s hook inject-context --part %d --of %d %s",
			shellSingleQuote(CtxloomCommand()), part, total, hash),
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

// SkillMatesTimeout is the timeout, in seconds, for the PostToolUse
// skill-mates hook. Like NextStepTimeout it READS THE TRANSCRIPT (that is
// where "already invoked this session" comes from); unlike the reflect hook it
// fires only on Skill calls, so the longer budget is paid rarely.
const SkillMatesTimeout = 15

// NewSkillMatesHook creates the PostToolUse hook that, when a link-group skill
// completes, names its group-mates the session has not invoked yet. It is the
// owner-session binding of that one ctxloom-owned step: the engine's tool
// hook is the completion signal, and the hook's additionalContext is the
// channel -- the same vehicle the skill listing rides.
//
// Matched to the Skill tool alone: for any other tool the answer is silence,
// and a process spawn plus a transcript read is too much to pay to hear it.
// No arguments, for the reason NewNextStepHook gives: the installed command
// outlives the session that wrote it, so the session is resolved from the
// environment at fire time.
func NewSkillMatesHook() wire.Hook {
	return wire.Hook{
		Command: fmt.Sprintf("%s hook skill-mates", shellSingleQuote(CtxloomCommand())),
		Type:    "command",
		Matcher: "Skill",
		Timeout: SkillMatesTimeout,
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
// nobody left to ask, and session_end need not be the same event on every
// engine. Firing every turn and
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

// MailDrainTimeout is the timeout, in seconds, for the turn_start mail-drain
// hook. It reads and renames a handful of files and nothing else; a slow one
// would sit between the human's Enter and the model's turn.
const MailDrainTimeout = 5

// NewMailDrainHook creates the turn_start hook that hands the session owner
// its pending mail as the starting turn's context.
//
// turn_start is the seam because the owner is prompt-driven: the only moment
// anything can be put in front of it is the moment a turn starts, and the
// human's own Enter is that moment as much as any wake is. The hook is the
// owner's only spool reader, so it is declared HERE — ctxloom's own hook
// management, unconditionally, on every engine that carries the event — and
// not in any bundle a profile may or may not select: mail delivery is not
// optional content.
//
// No arguments, for the reason NewNextStepHook gives: the installed command
// outlives the session that wrote it, so the owner's harp is resolved from
// the environment at fire time.
func NewMailDrainHook() wire.Hook {
	return wire.Hook{
		Command: fmt.Sprintf("%s hook mail-drain", shellSingleQuote(CtxloomCommand())),
		Type:    "command",
		Timeout: MailDrainTimeout,
	}
}

// NewContextInjectionHooks returns the SessionStart context-injection hook(s)
// for the given content hash. It reads the (content-addressed, immutable)
// context file to decide the split: content that fits in one sub-cap chunk —
// or a missing/unreadable file — yields a single legacy whole-content hook;
// larger content yields N ordered chunk hooks. Reading the file here and in the
// hook with the same ChunkContext guarantees write-time and run-time agree on
// N. Best-effort by design: any read error falls back to the single hook (the
// runtime hook then emits nothing if the file is truly empty).
func NewContextInjectionHooks(rep report.Reporter, hash, workDir string) []wire.Hook {
	content, err := ReadContextFile(workDir, hash)
	if err != nil {
		// This was a bare `_`, so a read failure right after the
		// content-addressed file was written (or a reaped/corrupted cache)
		// silently collapsed N chunk hooks to one, reintroducing the exact
		// truncation ContextChunkMaxChars exists to prevent. The best-effort
		// single-hook fallback is still correct (the runtime hook re-reads the
		// file itself when it fires) — only the silence was the defect.
		rep.Warnf("context injection hook for %s: %v — falling back to a single whole-content hook", hash, err)
	}
	chunks := ChunkContext(rep, content)
	if len(chunks) <= 1 {
		return []wire.Hook{NewContextInjectionHook(hash)}
	}
	hooks := make([]wire.Hook, 0, len(chunks))
	for k := 1; k <= len(chunks); k++ {
		hooks = append(hooks, NewContextInjectionChunkHook(hash, k, len(chunks)))
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

// MergeHooksConfig is wire.MergeHooksConfig with the drop named on this
// package's diagnostic channel: a nil DEST is a caller error, and a non-empty
// set going missing is warned rather than leaving the session running with
// none of its configured hooks and nothing said.
func MergeHooksConfig(rep report.Reporter, dest *wire.HooksConfig, src *wire.HooksConfig) {
	if n := wire.MergeHooksConfig(dest, src); n > 0 {
		rep.Warnf("hook merge has no destination hook set: dropping %d configured hook(s); this is a caller error, not a configuration one", n)
	}
}
