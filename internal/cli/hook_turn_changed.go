package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/turnchange"
)

// The two words this command puts on stdout. Nothing consumes them
// automatically: no hook configuration invokes this command, so the verdict is
// read only by whoever runs it directly. The fail-safe direction is therefore
// a property of this command itself rather than a guarantee some caller
// provides.
const (
	verdictChanged   = "changed"
	verdictUnchanged = "unchanged"
)

// turnChangedProg names this hook on the clidiag warning channel.
const turnChangedProg = "ctxloom hook turn-changed"

// hookTurnChangedCmd answers "did the turn now ending change anything?" for
// the Stop hook, replacing a git-status-dirtiness guard that measured the
// wrong thing. Dirtiness IN THIS CHECKOUT goes quiet on exactly the sessions
// that most need a close-out checklist: a coordinator dispatches every edit
// into a separate worktree and leaves its own tree clean. The turn is the
// unit, so the answer comes from the session transcript instead.
//
// WHY IT IS KEPT, given nothing calls it: this command is a member of the hook
// vocabulary and is kept for that vocabulary's COMPLETENESS. It does NOT serve
// the close-out contract and must not be wired to one to satisfy a claim found
// in prose. This file is the home of that ruling; do not re-derive it
// elsewhere.
var hookTurnChangedCmd = &cobra.Command{
	Use:    "turn-changed",
	Short:  "Report whether the ending turn changed anything (internal — used by the Stop hook)",
	Hidden: true, // Machine callback (Stop hook) — not for direct use
	Long: `Reads a Claude Code Stop hook payload on stdin and prints one word on
stdout: "changed" if the turn now ending performed a change-making action
(a file write, a mutating shell command, or work dispatched to another
agent — including a subagent editing a different worktree), or "unchanged"
if it only read, searched or answered.

Anything that cannot be measured — no transcript_path, an unreadable file, a
format this build cannot parse — prints "changed" and says why on stderr.
Silence is the failure this guard exists to prevent, so a spurious checklist
is the cheaper error. Exit status is always 0; the WORD is the result.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runTurnChanged,
}

func runTurnChanged(cmd *cobra.Command, args []string) error {
	verdict := turnChangedVerdict(cmd)
	fmt.Fprintln(cmd.OutOrStdout(), verdict)
	// Never non-zero: a Stop hook that fails is a Stop hook that can stall a
	// turn, and the verdict already rode out on stdout.
	return nil
}

// turnChangedVerdict resolves the word to print. Every failure path returns
// verdictChanged and warns, so the contract keeps firing when this command
// cannot answer.
func turnChangedVerdict(cmd *cobra.Command) string {
	raw, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		clidiag.Warn(turnChangedProg, "read stdin: %v — reporting %q", err, verdictChanged)
		return verdictChanged
	}
	if len(raw) == 0 {
		clidiag.Warn(turnChangedProg, "empty hook payload — reporting %q", verdictChanged)
		return verdictChanged
	}
	payload, err := claude.DecodeStopPayload(raw)
	if err != nil {
		clidiag.Warn(turnChangedProg, "parse hook payload: %v — reporting %q", err, verdictChanged)
		return verdictChanged
	}
	// Routed by the session's own engine: this guard is installed on every
	// hooking backend, and a reader chosen by assumption rather than by
	// engine reports "changed" forever on the ones it cannot parse.
	adapter, src, err := operations.ResolveTurnTranscript(cmd.Context(), os.Getenv(agent.SessionHarpEnv), payload.TranscriptPath)
	if err != nil {
		clidiag.Warn(turnChangedProg, "%v — reporting %q", err, verdictChanged)
		return verdictChanged
	}

	decision, err := turnchange.ClassifyTranscript(cmd.Context(), adapter, src)
	if err != nil {
		clidiag.Warn(turnChangedProg, "%v — reporting %q", err, verdictChanged)
		return verdictChanged
	}
	if decision.Changed {
		return verdictChanged
	}
	return verdictUnchanged
}

func init() {
	// turn-changed is a machine callback (Stop hook target), so it lives
	// under the hidden `hook` namespace beside its siblings.
	hookCmd.AddCommand(hookTurnChangedCmd)
}
