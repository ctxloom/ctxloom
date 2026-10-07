package agent

import (
	"strconv"

	"github.com/ctxloom/ctxloom/internal/core/wire"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// SessionStartTimeout is the timeout, in seconds, for the session-start
// hook. It reads the session index, one essence file and the project's
// config; a slow one would sit between the launch and the session's first
// prompt.
const SessionStartTimeout = 15

// NewSessionStartHook creates ctxloom's SessionStart hook: the resumed
// session's essence and the session-start notices (cli.hookSessionStartCmd).
// It carries NO project context and no argument naming one — the assembled
// context is the session's system prompt, and this hook delivering it too is
// the duplication it was cut down to remove.
//
// No arguments, for the reason NewNextStepHook gives: the installed command
// outlives the session that wrote it, so everything the hook needs is
// resolved from its payload and environment at fire time.
func NewSessionStartHook() wire.Hook {
	return ctxloomCallback(SessionStartTimeout, "session-start")
}

// ctxloomCallback is one of ctxloom's own hook callbacks: EXEC form, the bare
// ctxloom executable (CtxloomCommand) running `hook <verb> <args...>`, so no
// shell parses any of it and nothing in it is a fact about the machine that
// wrote it. Every NewXxxHook constructor builds through it, so the shape of a
// ctxloom callback is decided once.
func ctxloomCallback(timeout int, verb string, args ...string) wire.Hook {
	return wire.Hook{
		Command: CtxloomCommand(),
		Args:    append([]string{"hook", verb}, args...),
		Type:    "command",
		Timeout: timeout,
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
	return ctxloomCallback(ToolReflectTimeout, "tool-reflect", "--min-output-bytes", strconv.Itoa(minBytes))
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
// Narrowed to the skill tool class alone (wire.ToolSkill, which each engine's
// hooks approach maps to its own skill tool): for any other tool the answer is
// silence, and a process spawn plus a transcript read is too much to pay to
// hear it.
// No arguments, for the reason NewNextStepHook gives: the installed command
// outlives the session that wrote it, so the session is resolved from the
// environment at fire time.
func NewSkillMatesHook() wire.Hook {
	h := ctxloomCallback(SkillMatesTimeout, "skill-mates")
	h.Tool = wire.ToolSkill
	return h
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
	return ctxloomCallback(NextStepTimeout, "next-step")
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
// the environment at fire time — and only under the session-owner marker
// (sessions.EnvSessionOwner) the owner's launch alone carries. Once written
// into a repository's own settings file the hook fires in every engine that
// loads it, a delegated child's included; the marker, not this declaration,
// is what keeps the child from claiming the spool its runner reads.
func NewMailDrainHook() wire.Hook {
	return ctxloomCallback(MailDrainTimeout, "mail-drain")
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
