package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// HookOutput is the JSON output format for AI tool SessionStart hooks,
// compatible with Claude Code and Codex.
type HookOutput = claude.SessionStartOutput

// HookSpecificOutput contains hook-specific data to inject.
type HookSpecificOutput = claude.SessionStartSpecificOutput

var injectContextProject string
var injectContextPart int
var injectContextTotal int

var hookInjectContextCmd = &cobra.Command{
	Use:    "inject-context <hash>",
	Hidden: true, // Machine callback (SessionStart hook) - not for direct use
	Short:  "Inject session context for AI tool hooks",
	Long: `Reads the context file (.ctxloom/cache/context/<hash>.md) and outputs JSON suitable for
AI tool SessionStart hooks.

This command is invoked automatically by AI tools (Claude Code, Codex) during
their SessionStart event to inject fresh context on startup, resume, or /clear.

Arguments:
  hash    The context file hash (filename without .md extension)

Output format (JSON to stdout):
{
  "hookSpecificOutput": {
    "additionalContext": "<context content>"
  }
}`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runHookInjectContext,
}

func runHookInjectContext(cmd *cobra.Command, args []string) (err error) {
	// Always output valid JSON, even on errors, so the host never hangs
	// waiting for output — but a panic still exits NON-ZERO: a panicking
	// hook has delivered zero context, and exit 0 would make that
	// indistinguishable from "ctxloom had nothing to inject". Registered
	// before the first statement that can panic (the args read below), so
	// the whole body is covered.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "ctxloom hook inject-context: panic: %v\n", r)
			// Output empty JSON on panic
			fmt.Println("{}")
			err = fmt.Errorf("inject-context hook panicked: %v", r)
		}
	}()

	hash := args[0]

	// Read hook input from stdin (Claude passes session context here)
	var hookInput claude.SessionStartPayload
	inputData, err := io.ReadAll(os.Stdin)
	if err == nil && len(inputData) > 0 {
		if unmarshalErr := json.Unmarshal(inputData, &hookInput); unmarshalErr != nil {
			clidiag.Warn("ctxloom hook inject-context", "failed to parse hook input: %v", unmarshalErr)
		}
	}

	// Determine work directory from --project flag, git root, or current directory
	workDir := resolveInjectContextWorkDir(injectContextProject, gitutil.FindRoot)

	// Read context file by hash
	content, err := agent.ReadContextFile(workDir, hash)
	if err != nil {
		// Log to stderr, output empty JSON to stdout
		clidiag.Warn("ctxloom hook inject-context", "failed to read context file: %v", err)
		content = ""
	}

	// Select this part's chunk. With no --of (the legacy/manual single-shot
	// form), the whole content is emitted as one block. With --part/--of,
	// the content is split deterministically (see backends.ChunkContext)
	// and we wait our turn so the N parallel chunk hooks complete — and
	// thus inject — in order (see backends.AwaitTurn).
	content, part, total := selectChunk(content, injectContextPart, injectContextTotal)
	// Ordering only matters for a part that HAS a chunk to emit. An
	// out-of-range part (missing context file, or a file that shrank since
	// the hooks were written) emits nothing, so joining the rendezvous
	// would spend up to ContextRendezvousTimeout of session-startup
	// latency ordering nothing at all. Skipping cannot break the chain for
	// the parts that do have content: chunks are contiguous from part 1,
	// so an empty part is never followed by a content-bearing one, and the
	// rendezvous only ever waits on the immediate predecessor.
	if total > 1 && content != "" {
		agent.AwaitTurn(hookInput.SessionID, part, total)
	}

	// On the initial launch of a resumed session, inject the resumed
	// session's distilled essence alongside the project context. Skipped on
	// /clear and /compact (by source), where re-injecting would also mean a
	// redistill on the resume hot path — there the user pulls prior context
	// back explicitly with /recover.
	resumedEssence := resumedEssenceForInjection(part, hookInput.Source,
		os.Getenv("CTXLOOM_RESUMED_FROM"), os.Getenv("CTXLOOM_RESUMED_PARTS"))

	// Build output via the extracted helper so the wrapping logic
	// (header/footer, empty-content handling, SessionStart event
	// name) is unit-testable without the surrounding hook plumbing.
	output := buildInjectContextOutput(content, resumedEssence, part, total)

	// After a /clear, nudge the USER (not the model) toward /recover via the
	// systemMessage channel, independent of the injected context. claude-code's
	// /clear does NOT keep the pre-clear transcript alive under the same
	// file — measured false: it starts a FRESH session UUID and a fresh,
	// empty transcript file, firing SessionStart again with the new id (the
	// /clear record is the first message of the successor file; no backward
	// pointer exists in the vendor files). What makes recovery possible now is
	// the harp-LIFETIME canonical transcript (RefreshVendorTranscript
	// concatenating sessions.Entry.Rotations' cached segments with the live
	// conversion) — so recoverability is a question about the harp's index
	// entry, NOT whether the CURRENT (post-clear, necessarily still-empty-at-
	// this-moment) transcript file happens to be non-empty.
	//
	// It is NOT simply "does the entry already carry a recorded rotation",
	// though: claude.go's ctxloomMachineCallbacks runs inject-context BEFORE
	// session-bind on a real SessionStart, so at THIS moment the bind that
	// would append hookInput.SessionID's predecessor to Rotations has not run
	// yet — the index still holds the PRE-clear binding. currentSessionRecoverable
	// therefore also treats a bound entry whose CURRENT SessionID differs from
	// the incoming hookInput.SessionID as recoverable: that displacement is
	// about to be recorded by the session-bind hook that fires right after
	// this one. See its own doc comment.
	//
	// Only the first chunk checks (the message also guards part>1); the
	// lookup is local, and on an unbound harp or one whose current binding
	// already matches the incoming id we stay silent rather than promise a
	// recovery that would come back empty.
	//
	// WHY the source is not re-checked here: clearRecoveryMessage owns that
	// decision, and encoding it in both places made each copy individually
	// unkillable — removing either one alone changed no observable behaviour,
	// so no test could tell whether the rule was still enforced. This computes
	// only the fact (is there anything to recover) and lets one function decide
	// what to do with it.
	clearRecoverable := false
	if part <= 1 {
		clearRecoverable = currentSessionRecoverable(hookInput.Source, os.Getenv(agent.SessionHarpEnv), hookInput.SessionID)
	}
	// Compose the user-facing SessionStart nudges: the clear-recovery hint
	// (when a /clear left a recoverable prior session) and the agent-setup
	// nudge (when this project has profiles but no agents). Both ride the
	// systemMessage channel and can co-occur, so they are joined rather than
	// one clobbering the other.
	output.SystemMessage = operations.JoinLeadBlocks(
		clearRecoveryMessage(hookInput.Source, part, clearRecoverable),
		agentSetupNudge(workDir, part),
	)

	// Output JSON to stdout
	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(output); err != nil {
		// If encoding fails, output empty JSON
		clidiag.Warn("ctxloom hook inject-context", "failed to encode output: %v", err)
		fmt.Println("{}")
	}
	return nil
}

// clearRecoveryMessage returns the user-facing nudge shown after a /clear when
// the current session's pre-clear transcript is recoverable, or "" otherwise. It
// rides SessionStartOutput's systemMessage channel (surfaced to the user, not the
// model), and fires once per clear: only on the first chunk (part<=1) and only
// for source=="clear".
func clearRecoveryMessage(source string, part int, recoverable bool) string {
	if part > 1 || source != "clear" || !recoverable {
		return ""
	}
	return "ctxloom: context cleared. Run /recover to bring your pre-clear context back."
}

// currentSessionRecoverable reports whether /recover (recover_session) has
// something to bring back for the current harp, for the one source that
// actually needs this check: source=="clear". claude-code's /clear starts a
// FRESH session UUID and an empty transcript file (measured; see the call
// site's doc comment) — the OLD transcript's content only survives if the
// store recorded it as a rotation (sessions.Entry.Rotations, appended by
// sessions.Manager.BindSession's displacing rebind).
//
// Recoverable iff harpName resolves to an index entry, AND EITHER:
//   - the entry already carries ≥1 recorded rotation (a prior clear in THIS
//     session already displaced a binding), OR
//   - the entry's CURRENT SessionID is non-empty and differs from
//     payloadSessionID (hookInput.SessionID) — the FIRST clear in a session.
//     inject-context runs before session-bind (claude.go's
//     ctxloomMachineCallbacks), so at this moment the index still holds the
//     PRE-clear binding and no rotation has been appended yet; a bound id
//     that disagrees with the incoming one is exactly the displacement
//     session-bind is about to record. Without this disjunct, the FIRST
//     /clear in every session was reported not-recoverable — the rotation
//     that would make it true hadn't been written yet.
//
// Any other source, an unresolvable harp, an unbound entry, or an entry
// already bound to payloadSessionID (nothing displaced, nothing to recover)
// yields false, so the nudge never promises a recovery that would come back
// empty.
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

// agentSetupNudge returns the Phase F "profiles but no agents" nudge for
// the project rooted at workDir, or "" when it should not fire. It loads the
// project config (rooted at workDir/.ctxloom) and delegates the trigger decision
// to operations.AgentSetupNudge (profiles present AND no agents). It fires
// once per SessionStart (part<=1, so a multi-chunk inject doesn't repeat it) and
// is fully fault-tolerant: a config-load failure yields "" and never blocks
// startup (CLAUDE.md). The condition self-resolves the moment any agent is
// configured.
func agentSetupNudge(workDir string, part int) string {
	if part > 1 {
		return ""
	}
	cfg, err := config.Load(config.WithAppDir(filepath.Join(workDir, config.AppDirName)))
	if err != nil {
		return ""
	}
	return operations.AgentSetupNudge(cfg)
}

// selectChunk resolves which slice of the assembled context this hook
// invocation emits. total < 1 is the single-shot form: the whole content is
// returned as part 1 of 1. Otherwise the content is split deterministically
// and the part-th chunk (1-based) is returned; an out-of-range part yields
// empty content (buildInjectContextOutput then emits nothing), which keeps the
// hook safe even if part/total ever disagree with the on-disk content.
func selectChunk(content string, part, total int) (chunk string, outPart, outTotal int) {
	if total < 1 {
		return content, 1, 1
	}
	chunks := agent.ChunkContext(content)
	if part >= 1 && part <= len(chunks) {
		return chunks[part-1], part, total
	}
	return "", part, total
}

// buildInjectContextOutput wraps a context chunk in the ctxloom-attributed
// envelope that SessionStart hooks receive. Empty content produces an empty
// HookOutput (no AdditionalContext field) so the LLM doesn't see a misleading
// "ctxloom content loaded" header when ctxloom had nothing to inject.
//
// Each chunk is independently framed in its own <ctxloom-context> block so an
// unrelated SessionStart hook (e.g. `session bind`) interleaving between chunks
// can't corrupt the framing. The full attribution paragraph rides on the first
// segment (delivered first by the rendezvous); later segments carry only a
// compact header.
//
// Extracted as a pure function so the wrapping format is testable without
// spinning up the full hook plumbing.
func buildInjectContextOutput(content, resumedEssence string, part, total int) HookOutput {
	includeEssence := part <= 1 && resumedEssence != ""
	if content == "" && !includeEssence {
		return HookOutput{}
	}

	var body string
	if includeEssence {
		body = "# Resumed session (assembled by ctxloom)" +
			"\n\n_The summary below is the distilled essence of the session you resumed from. " +
			"Use it to pick up where that session left off. It is recovered memory, not project instructions._" +
			"\n\n<ctxloom-resumed-session>\n\n" + resumedEssence + "\n\n</ctxloom-resumed-session>\n"
	}

	if content != "" {
		// Header/preamble text is shared with claude's native
		// --append-system-prompt-file delivery (agent.FrameProjectContext) so the
		// two paths can't drift; here it's composed per-segment for the chunked
		// SessionStart hook. A single-shot call (total<=1) reproduces
		// agent.FrameProjectContext(content) exactly.
		header := agent.ProjectContextHeader
		if total > 1 {
			header = fmt.Sprintf("%s — segment %d of %d", agent.ProjectContextHeader, part, total)
		}
		var preamble string
		if part <= 1 {
			preamble = agent.ProjectContextPreamble
			if total > 1 {
				preamble += fmt.Sprintf("\n\n_This context is delivered in %d ordered segments._", total)
			}
		}
		ctxBlock := header + preamble + "\n\n<ctxloom-context>\n\n" + content + "\n\n</ctxloom-context>\n"
		if body != "" {
			body += "\n" + ctxBlock
		} else {
			body = ctxBlock
		}
	}

	return HookOutput{
		HookSpecificOutput: &HookSpecificOutput{
			HookEventName:     claude.HookEventSessionStart,
			AdditionalContext: body,
		},
	}
}

// resumedEssenceForInjection returns the distilled essence to inject for a
// resumed session, or "" when none should be injected. Essence rides only the
// first chunk, only on an initial launch (not /clear or /compact, see
// shouldInjectResumedEssence), only when a resume happened (resumedFrom set),
// and only when the resume carried the session part (CTXLOOM_RESUMED_PARTS).
func resumedEssenceForInjection(part int, source, resumedFrom, resumedParts string) string {
	if part > 1 || resumedFrom == "" || !shouldInjectResumedEssence(source) {
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

// resolveInjectContextWorkDir picks the directory ctxloom should treat
// as the project root for an inject-context call. Precedence:
//
//  1. Explicit --project flag value, if non-empty.
//  2. CTXLOOM_ROOT override, when set and a valid directory.
//  3. Git repository root containing the current directory.
//  4. Fall back to ".".
//
// findRoot is the injectable git-root finder; production uses
// gitutil.FindRoot, tests pass a stub that returns a known value or
// an error to exercise each branch.
func resolveInjectContextWorkDir(flagVal string, findRoot func(string) (string, error)) string {
	if flagVal != "" {
		return flagVal
	}
	if root, ok := projectroot.FromEnv(afero.NewOsFs()); ok {
		return root
	}
	if findRoot != nil {
		if root, err := findRoot("."); err == nil {
			return root
		}
	}
	return "."
}

func init() {
	hookInjectContextCmd.Flags().StringVar(&injectContextProject, "project", "", "Project directory (defaults to git root or current directory)")
	hookInjectContextCmd.Flags().IntVar(&injectContextPart, "part", 0, "1-based chunk index when context is split across multiple ordered hooks")
	hookInjectContextCmd.Flags().IntVar(&injectContextTotal, "of", 0, "total number of context chunks (omit or 0 for single-shot whole-content injection)")
	hookCmd.AddCommand(hookInjectContextCmd)
}
