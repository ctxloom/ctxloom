package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/textblocks"
)

// hookSessionStartCmd is ctxloom's own SessionStart callback. It NEVER
// delivers the project's assembled context: that is the session's system
// prompt (claude's context approach), and a second copy here is the
// duplication this hook was cut down to remove. What it delivers is what
// only a session-start moment can: the compacted essence of a resumed
// session, and the user-facing notices (the /clear recovery hint, the
// agent-setup nudge).
var hookSessionStartCmd = &cobra.Command{
	Use:    "session-start",
	Hidden: true, // Machine callback (SessionStart hook) - not for direct use
	Short:  "Deliver the resumed essence and session-start notices to an AI tool's SessionStart hook",
	Long: `Reads the session_start payload on stdin, through the codec of the engine
--engine names, and answers through the same codec: the compacted essence of
the session a compacted resume continues, as model context, and the
session-start notices for the user. It never carries the project's context,
which the session already has.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runHookSessionStart,
}

// runHookSessionStart answers one session_start. A payload it cannot read is
// an error and the process exits non-zero: an engine shows a failed hook,
// which is the truth, where an answer built from an empty payload would claim
// a startup that never happened. A panic exits non-zero for the same reason.
func runHookSessionStart(cmd *cobra.Command, _ []string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "ctxloom hook session-start: panic: %v\n", r)
			err = fmt.Errorf("session-start hook panicked: %v", r)
		}
	}()
	kind, err := firingEngine(cmd)
	if err != nil {
		return err
	}
	codec := kind.Hooks()
	ev, err := readHookEvent(cmd, codec, wire.HookEventSessionStart)
	if err != nil {
		return err
	}
	return writeHookResponse(cmd, codec, wire.HookEventSessionStart, sessionStartResponse(ev, codec.ContextLimit()))
}

// sessionStartResponse is what one session_start earns: the resumed essence
// (held under the engine's context limit) and the user-facing notices.
//
// After a /clear the USER (not the model) is nudged toward /recover: a clear
// starts a FRESH native session and an empty transcript, firing session_start
// again with the new id; recovery reads the harp-lifetime canonical
// transcript, so recoverability is a question about the harp's index entry
// (currentSessionRecoverable). The two notices can co-occur, so they are
// joined rather than one clobbering the other.
func sessionStartResponse(ev engine.HookEvent, contextLimit int) engine.HookResponse {
	resumedFrom := os.Getenv("CTXLOOM_RESUMED_FROM")
	clearRecoverable := currentSessionRecoverable(ev.Source, os.Getenv(agent.SessionHarpEnv), ev.NativeSession)
	return engine.HookResponse{
		Context: sessionStartContext(
			resumedEssenceForInjection(ev.Source, resumedFrom, os.Getenv("CTXLOOM_RESUMED_PARTS")),
			essencePathOf(resumedFrom), contextLimit),
		Notice: textblocks.Join(clearRecoveryMessage(ev.Source, clearRecoverable), agentSetupNudge()),
	}
}

// clearRecoveryMessage returns the user-facing nudge shown after a /clear when
// the current session's pre-clear transcript is recoverable, or "" otherwise.
func clearRecoveryMessage(source string, recoverable bool) string {
	if source != engine.SessionSourceClear || !recoverable {
		return ""
	}
	return "ctxloom: context cleared. Run /recover to bring your pre-clear context back."
}

// currentSessionRecoverable reports whether /recover (recover_session) has
// something to bring back for the current harp, for the one source that
// actually needs this check: source=="clear". The OLD transcript's content
// only survives if the store recorded it as a rotation (sessions.Entry.
// Rotations, appended by sessions.Manager.BindSession's displacing rebind).
//
// Recoverable iff harpName resolves to an index entry, AND EITHER:
//   - the entry already carries ≥1 recorded rotation, OR
//   - the entry's CURRENT SessionID is non-empty and differs from
//     payloadSessionID. claude runs a SessionStart event's hooks in parallel,
//     so this hook cannot rely on `hook session-bind` having recorded THIS
//     clear's displacement yet; a bound id that disagrees with the incoming
//     one is exactly that displacement.
func currentSessionRecoverable(source, harpName, payloadSessionID string) bool {
	if source != engine.SessionSourceClear || harpName == "" {
		return false
	}
	entry, err := operations.GetSession(harpName)
	if err != nil || entry == nil {
		return false
	}
	if len(entry.Rotations) > 0 {
		return true
	}
	return entry.SessionID != "" && entry.SessionID != payloadSessionID
}

// agentSetupNudge returns the "profiles but no agents" nudge for the project
// this hook process runs in, or "" when it should not fire. It reads this
// process's own generation and delegates the trigger decision to
// operations.AgentSetupNudge; a config refusal yields "" and never blocks
// startup.
func agentSetupNudge() string {
	cfg, err := GetConfig()
	if err != nil {
		return ""
	}
	return operations.AgentSetupNudge(cfg)
}

// essenceHeader frames the resumed essence for the model.
const essenceHeader = "# Resumed session (assembled by ctxloom)" +
	"\n\n_The summary below is the compacted essence of the session you resumed from. " +
	"Use it to pick up where that session left off. It is recovered memory, not project instructions._" +
	"\n\n<ctxloom-resumed-session>\n\n"

const essenceFooter = "\n\n</ctxloom-resumed-session>\n"

// sessionStartContext frames the resumed essence for the model, or "" when
// there is none.
//
// The essence is held under limit, the firing engine's context limit
// (engine.HookCodec.ContextLimit; 0 declares none), because past an engine's
// cap the model sees only a short preview of what a hook returned. An essence
// too long for that is CUT, not dropped: the model gets as much of it as fits,
// then a pointer to where the whole of it is (essencePath) and to /recover,
// which brings the prior session back through ctxloom's MCP tools rather than
// a hook. A cut with no path still names /recover.
func sessionStartContext(essence, essencePath string, limit int) string {
	if essence == "" {
		return ""
	}
	body := essenceHeader + essence + essenceFooter
	if limit > 0 && len(body) > limit {
		where := "Run /recover to bring the whole of it back."
		if essencePath != "" {
			where = fmt.Sprintf("The whole essence is at `%s`; read it, or run /recover to bring it back.", essencePath)
		}
		cut := fmt.Sprintf("\n\n_[The essence is cut here to fit the session-start limit. %s]_", where)
		room := limit - len(essenceHeader) - len(essenceFooter) - len(cut)
		body = essenceHeader + strings.ToValidUTF8(essence[:max(room, 0)], "") + cut + essenceFooter
	}
	return body
}

// essencePathOf is where harp's essence file is, "" when there is none.
func essencePathOf(harp string) string {
	if harp == "" {
		return ""
	}
	p, ok := operations.SessionEssenceInfo(harp, nil)
	if !ok {
		return ""
	}
	return p
}

// resumedEssenceForInjection returns the compacted essence to deliver for a
// resumed session, or "" when none should be. Essence rides only an initial
// launch (not /clear or /compact, see shouldInjectResumedEssence), only when
// a resume happened (resumedFrom set), and only when the resume carried the
// session part (CTXLOOM_RESUMED_PARTS).
func resumedEssenceForInjection(source, resumedFrom, resumedParts string) string {
	if resumedFrom == "" || !shouldInjectResumedEssence(source) {
		return ""
	}
	if !resumePartsIncludeSession(resumedParts) {
		return ""
	}
	data, err := operations.ReadHarpEssence(resumedFrom)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// shouldInjectResumedEssence reports whether a SessionStart with the given
// source should carry the resumed essence. It rides the initial launch
// (startup, resume, or an unknown/empty source) but not "clear" or "compact",
// which fire mid-session where /recover is the explicit path.
func shouldInjectResumedEssence(source string) bool {
	switch source {
	case engine.SessionSourceClear, engine.SessionSourceCompact:
		return false
	default:
		return true
	}
}

// resumePartsIncludeSession reports whether the resume carried the session
// essence rather than being a tasks-only resume. Empty parts default to true,
// matching the default "session,tasks".
func resumePartsIncludeSession(parts string) bool {
	if parts == "" {
		return true
	}
	for _, p := range strings.Split(parts, ",") {
		if strings.TrimSpace(p) == "session" {
			return true
		}
	}
	return false
}

func init() {
	hookCmd.AddCommand(hookSessionStartCmd)
}
