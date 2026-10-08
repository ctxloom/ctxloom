package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/textblocks"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// HookOutput is the JSON output format for Claude Code's SessionStart hook.
type HookOutput = claude.SessionStartOutput

// HookSpecificOutput contains hook-specific data to inject.
type HookSpecificOutput = claude.AdditionalContextOutput

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
	Long: `Reads the SessionStart payload on stdin and writes the hook's JSON to stdout:
the compacted essence of the session a compacted resume continues, as
additionalContext, and the session-start notices as systemMessage. It never
carries the project's context, which the session already has.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runHookSessionStart,
}

func runHookSessionStart(cmd *cobra.Command, _ []string) (err error) {
	// Always output valid JSON, even on errors, so the host never hangs
	// waiting for output — but a panic still exits NON-ZERO: exit 0 would
	// make a crash indistinguishable from "nothing to deliver".
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "ctxloom hook session-start: panic: %v\n", r)
			fmt.Println("{}")
			err = fmt.Errorf("session-start hook panicked: %v", r)
		}
	}()

	var hookInput claude.SessionStartPayload
	inputData, err := io.ReadAll(cmd.InOrStdin())
	if err == nil && len(inputData) > 0 {
		if unmarshalErr := json.Unmarshal(inputData, &hookInput); unmarshalErr != nil {
			clidiag.Warn("ctxloom hook session-start", "failed to parse hook input: %v", unmarshalErr)
		}
	}

	resumedFrom := os.Getenv("CTXLOOM_RESUMED_FROM")
	output := buildSessionStartOutput(
		resumedEssenceForInjection(hookInput.Source, resumedFrom, os.Getenv("CTXLOOM_RESUMED_PARTS")),
		essencePathOf(resumedFrom))

	// After a /clear, nudge the USER (not the model) toward /recover via the
	// systemMessage channel. claude-code's /clear starts a FRESH session UUID
	// and an empty transcript file, firing SessionStart again with the new id;
	// recovery reads the harp-lifetime canonical transcript, so recoverability
	// is a question about the harp's index entry (currentSessionRecoverable).
	clearRecoverable := currentSessionRecoverable(hookInput.Source, os.Getenv(agent.SessionHarpEnv), hookInput.SessionID)
	// The two notices can co-occur, so they are joined rather than one
	// clobbering the other.
	output.SystemMessage = textblocks.Join(
		clearRecoveryMessage(hookInput.Source, clearRecoverable),
		agentSetupNudge(),
	)

	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		clidiag.Warn("ctxloom hook session-start", "failed to encode output: %v", err)
		fmt.Println("{}")
	}
	return nil
}

// clearRecoveryMessage returns the user-facing nudge shown after a /clear when
// the current session's pre-clear transcript is recoverable, or "" otherwise.
func clearRecoveryMessage(source string, recoverable bool) string {
	if source != "clear" || !recoverable {
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
	if source != "clear" || harpName == "" {
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

// buildSessionStartOutput wraps the resumed essence in the envelope a
// SessionStart hook returns, or an empty HookOutput when there is none.
//
// The essence is held under claude.AdditionalContextMaxChars, because past
// claude's cap the model sees only a short preview of what a hook returned.
// An essence too long for that is CUT, not dropped: the model gets as much of
// it as fits, then a pointer to where the whole of it is (essencePath) and to
// /recover, which brings the prior session back through ctxloom's MCP
// tools rather than a hook. A cut with no path still names /recover.
func buildSessionStartOutput(essence, essencePath string) HookOutput {
	if essence == "" {
		return HookOutput{}
	}
	body := essenceHeader + essence + essenceFooter
	if len(body) > claude.AdditionalContextMaxChars {
		where := "Run /recover to bring the whole of it back."
		if essencePath != "" {
			where = fmt.Sprintf("The whole essence is at `%s`; read it, or run /recover to bring it back.", essencePath)
		}
		cut := fmt.Sprintf("\n\n_[The essence is cut here to fit the session-start limit. %s]_", where)
		room := claude.AdditionalContextMaxChars - len(essenceHeader) - len(essenceFooter) - len(cut)
		body = essenceHeader + strings.ToValidUTF8(essence[:max(room, 0)], "") + cut + essenceFooter
	}
	return HookOutput{
		HookSpecificOutput: &HookSpecificOutput{
			HookEventName:     claude.HookEventSessionStart,
			AdditionalContext: body,
		},
	}
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
	case "clear", "compact":
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
