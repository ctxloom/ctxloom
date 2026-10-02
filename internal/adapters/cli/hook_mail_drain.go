package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// mailDrainProg names this hook on the clidiag warning channel.
const mailDrainProg = "ctxloom hook mail-drain"

var hookMailDrainCmd = &cobra.Command{
	Use:    "mail-drain",
	Hidden: true, // Machine callback (turn_start hook) — not for direct use
	Short:  "Hand the session owner its pending mail as the starting turn's context (internal — used by the turn_start hook)",
	Long: `Reads the session owner's spool directly and delivers every pending message
as additional context of the turn that is starting.

The session owner is the one recipient with no runner of its own, and this
hook is its ONLY spool reader: it claims what waits in in/, writes each
message — under the coordinator's provenance header, exactly as a hosted
engine sees delivered mail — to stdout as a UserPromptSubmit envelope, and
acknowledges what it wrote. A message is acknowledged only once it has been
written; a hook that dies in between leaves it claimed, and the next turn's
hook delivers it again.

With no mail waiting it writes nothing at all: a prompt is the ordinary
reason for a turn to start, and an empty envelope would still be an event the
model sees.

A prompt that IS a ctxloom wake (engine.WakeText) redeems the wake's nonce —
that is the wake's acknowledgement — and, when the wake finds no mail, blocks
the prompt so a stale wake costs no model turn. A human's prompt is never
blocked. A turn that delivers mail answers every outstanding wake, since it
delivered what each of them announced.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runHookMailDrain,
}

// runHookMailDrain never reports a nonzero exit: a turn_start hook that fails
// blocks the human's own prompt, which no delivery problem is worth. Every
// reason mail was not (fully) delivered is NAMED on the diagnostic channel
// instead, as one line, because the alternative is this project's
// characteristic bug: exit 0, and zero bytes written, with nothing said.
func runHookMailDrain(cmd *cobra.Command, args []string) error {
	if err := drainMail(cmd, os.Getenv(agent.SessionHarpEnv)); err != nil {
		clidiag.Warn(mailDrainProg, "%v", err)
	}
	return nil
}

// drainMail does the work and RETURNS its failure rather than warning itself,
// so a test can assert which reason fired without parsing stderr. It
// delivers what it can before reporting what it could not.
//
// CONTAINER AXIS: this hook is inert for a coordinator hosted in a container
// tonight — a containerized claude never receives ctxloom's hooks at all
// (pulmonary-eternity), so nothing there ever invokes it. The exclusion is a
// property of hook carriage, not of this code: the day carriage is fixed,
// the same binary reads the same spool through the same home-relative mapper
// (spool.HomeMapper's mount contract), and nothing here changes.
func drainMail(cmd *cobra.Command, harp string) error {
	// Read to EOF before anything can return: closing stdin early would be
	// reported by some engines as a failed hook.
	raw, _ := io.ReadAll(cmd.InOrStdin())
	if harp == "" {
		// A plain engine session ctxloom did not launch: no harp, no spool,
		// nothing truthful to say. Silence is the contract, not a shortcut.
		return nil
	}
	mapper := spool.NewHomeMapper()
	// An unreadable payload is a turn with no prompt we can read: never a
	// wake, so never blocked.
	var payload claude.UserPromptSubmitPayload
	_ = json.Unmarshal(raw, &payload)
	isWake, problems := redeemWakeNonce(mapper, harp, payload.Prompt)
	res, err := spool.Claim(mapper, harp)
	if err != nil {
		return fmt.Errorf("no mail delivered: %w", err)
	}
	for _, p := range res.Problems {
		problems = append(problems, p.Error())
	}
	if len(res.Entries) == 0 {
		if isWake {
			problems = append(problems, blockStaleWake(cmd)...)
		}
		return joinProblems(problems)
	}
	frames := make([]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		frames = append(frames, runner.FrameCoordinatorDelivery(e.Message.FromHarp, e.Message.Kind, e.Message.Body))
	}
	if err := writeHookOutput(cmd, claude.UserPromptSubmitOutput{HookSpecificOutput: &claude.AdditionalContextOutput{
		HookEventName:     claude.HookEventUserPromptSubmit,
		AdditionalContext: strings.Join(frames, "\n\n"),
	}}); err != nil {
		// Delivery is the write. What was not written stays in in/claimed/,
		// where the next turn's Claim hands it out again.
		return fmt.Errorf("%d message(s) left claimed, not delivered: %w", len(res.Entries), err)
	}
	problems = append(problems, ackDelivered(mapper, harp, res.Entries)...)
	// This turn delivered what every armed wake announced: answer them all,
	// or a wake held or lost upstream refuses every later one.
	if _, err := spool.ClearWakes(mapper, harp); err != nil {
		problems = append(problems, fmt.Sprintf("the wakes this delivery answered could not be cleared, and may refuse the next wake: %v", err))
	}
	return joinProblems(problems)
}

// redeemWakeNonce consumes the wake nonce a prompt carries, reporting whether
// the prompt was a wake and, when the nonce could not be redeemed, why.
func redeemWakeNonce(mapper spool.PathMapper, harp, prompt string) (bool, []string) {
	nonce, isWake := engine.WakeNonce(prompt)
	if !isWake {
		return false, nil
	}
	if _, err := spool.ConsumeWake(mapper, harp, nonce); err != nil {
		return true, []string{fmt.Sprintf("wake %s was not redeemed: %v", nonce, err)}
	}
	return true, nil
}

// blockStaleWake blocks a wake whose mail was already delivered, reporting
// when the block could not be written.
func blockStaleWake(cmd *cobra.Command) []string {
	if err := writeHookOutput(cmd, claude.UserPromptSubmitOutput{
		Decision: claude.DecisionBlock,
		Reason:   "ctxloom: the mail this wake announced was already delivered",
	}); err != nil {
		return []string{fmt.Sprintf("a stale wake could not be blocked: %v", err)}
	}
	return nil
}

// ackDelivered acknowledges each delivered entry, reporting the ones that
// could not be (and so will be delivered again); one already gone is fine.
func ackDelivered(mapper spool.PathMapper, harp string, entries []spool.Entry) []string {
	var problems []string
	for _, e := range entries {
		if err := spool.Deliver(mapper, e.Ref, e.Identity(), time.Now()); err != nil && !errors.Is(err, spool.ErrAlreadyGone) {
			problems = append(problems, fmt.Sprintf("%s was delivered but could not be acknowledged and will be delivered again: %v", e.Ref, err))
		}
	}
	return problems
}

// writeHookOutput writes out as one JSON line. It is encoded to bytes first so
// a failure cannot leave a partial envelope on the engine's input channel.
func writeHookOutput(cmd *cobra.Command, out claude.UserPromptSubmitOutput) error {
	body, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(append(body, '\n'))
	return err
}

// joinProblems renders every non-fatal problem as ONE failure, so the hook's
// single stderr line names them all.
func joinProblems(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%d problem(s) in the owner's spool: %s", len(problems), strings.Join(problems, "; "))
}

func init() {
	hookCmd.AddCommand(hookMailDrainCmd)
}
