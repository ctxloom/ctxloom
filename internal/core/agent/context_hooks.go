package agent

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/shell"

	"github.com/ctxloom/ctxloom/internal/core/wire"

	"github.com/ctxloom/ctxloom/internal/shared/exectoken"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// SessionStartTimeout is the timeout, in seconds, for the session-start
// hook. It reads the project's config; a slow one would sit between the
// launch and the session's first prompt.
const SessionStartTimeout = 15

// NewSessionStartHook creates ctxloom's SessionStart hook: the session-start
// notices (cli.hookSessionStartCmd). It carries NO project context and no
// argument naming one — the assembled context is the session's system
// prompt, and this hook delivering it too is the duplication it was cut down
// to remove.
//
// No arguments, for the reason ctxloomCallback gives: everything the hook
// needs is resolved from its payload and environment at fire time.
func NewSessionStartHook() wire.Hook {
	return ctxloomCallback(SessionStartTimeout, "session-start")
}

// ctxloomCallback is one of ctxloom's own hook callbacks: EXEC form, the bare
// ctxloom executable (CtxloomCommand) running `hook <verb> <args...>`, so no
// shell parses any of it and nothing in it is a fact about the machine that
// wrote it. Every NewXxxHook constructor builds through it, so the shape of a
// ctxloom callback is decided once.
//
// A callback takes no session argument: it resolves its harp from the
// environment at FIRE time (SessionHarpEnv) rather than having one
// interpolated here, because the installed command is written once — by
// apply-hooks, into settings that outlive the session that wrote them — and
// must serve every later session.
func ctxloomCallback(timeout int, verb string, args ...string) wire.Hook {
	return wire.Hook{
		Command: CtxloomCommand(),
		Args:    append([]string{callbackSubcommand, verb}, args...),
		Type:    "command",
		Timeout: timeout,
	}
}

// callbackSubcommand is the ctxloom subcommand every callback runs under
// (ctxloomCallback); the HUD statusline runs under it too (`hook hud`).
const callbackSubcommand = "hook"

// IsCallback reports whether a hook entry, as a settings file decodes it, is
// one of ctxloom's callbacks by construction: it runs ctxloom's executable,
// and its subcommand (CtxloomSubcommand) is `hook <verb>` — the shape
// ctxloomCallback builds and the HUD statusline shares. It is what a release
// with no record takes out of a tracked settings file, and what a status
// reports as wired: a user's own entry running ctxloom with any other
// subcommand (`ctxloom profile list`) is theirs, and neither.
func IsCallback(v any) bool {
	sub, ok := CtxloomSubcommand(v)
	words := strings.Fields(sub)
	return ok && len(words) >= 2 && words[0] == callbackSubcommand
}

// CtxloomSubcommand is the ctxloom subcommand a hook entry invokes — its
// leading argument words up to the first flag, joined by a space. ok is false
// for an entry that does not run ctxloom or names no subcommand.
func CtxloomSubcommand(v any) (string, bool) {
	args, ok := ctxloomArgs(v)
	if !ok {
		return "", false
	}
	end := slices.IndexFunc(args, func(w string) bool { return strings.HasPrefix(w, "-") })
	if end < 0 {
		end = len(args)
	}
	return strings.Join(args[:end], " "), end > 0
}

// ctxloomArgs is the argument words of a hook entry that runs ctxloom,
// whichever form it is spelled in: the exec form (the executable in
// "command", its arguments in "args") or a shell line in "command" alone.
func ctxloomArgs(v any) ([]string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	command, ok := m["command"].(string)
	if !ok || !exectoken.IsManaged(command, CtxloomBinary) {
		return nil, false
	}
	raw, exec := m["args"].([]any)
	if !exec {
		fields, err := shell.Fields(command, func(string) string { return "" })
		if err != nil || len(fields) == 0 {
			return nil, false
		}
		return fields[1:], true
	}
	args := make([]string, 0, len(raw))
	for _, a := range raw {
		w, ok := a.(string)
		if !ok {
			return nil, false
		}
		args = append(args, w)
	}
	return args, true
}

// SkillMatesTimeout is the timeout, in seconds, for the PostToolUse
// skill-mates hook. It READS THE TRANSCRIPT (that is where "already invoked
// this session" comes from), which grows with the session; unlike a hook
// that fires on every tool call it fires only on Skill calls, so the longer
// budget is paid rarely.
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
// No arguments, for the reason ctxloomCallback gives.
func NewSkillMatesHook() wire.Hook {
	h := ctxloomCallback(SkillMatesTimeout, "skill-mates")
	h.Tool = wire.ToolSkill
	return h
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
// No arguments, for the reason ctxloomCallback gives — and the owner's harp
// is resolved only under the session-owner marker
// (sessions.EnvSessionOwner) the owner's launch alone carries. Once written
// into a repository's own settings file the hook fires in every engine that
// loads it, a delegated child's included; the marker, not this declaration,
// is what keeps the child from claiming the spool its runner reads.
func NewMailDrainHook() wire.Hook {
	return ctxloomCallback(MailDrainTimeout, "mail-drain")
}

// MailPeekTimeout is the timeout, in seconds, for the post_tool mail-peek
// hook. It fires on every tool call; with no mail waiting it reads two
// directory listings, and with mail it makes at most two local requests to
// the run's own runner, each bounded well inside this.
const MailPeekTimeout = 10

// NewMailPeekHook creates the post_tool hook that hands the engine mail that
// arrived during its turn, at its next tool call (cli's hook mail-peek).
// Unnarrowed: any tool call is a moment the model can be handed context.
// No arguments, for the reason ctxloomCallback gives.
func NewMailPeekHook() wire.Hook {
	return ctxloomCallback(MailPeekTimeout, "mail-peek")
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
