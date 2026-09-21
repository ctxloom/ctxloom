package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
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
model sees.`,
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
	// The payload is drained, not decoded: the hook keys on the harp in its
	// environment (sessions.HookEnv) and takes nothing from the event body.
	// Closing stdin early would be reported by some engines as a failed hook.
	_, _ = io.Copy(io.Discard, cmd.InOrStdin())
	if harp == "" {
		// A plain engine session ctxloom did not launch: no harp, no spool,
		// nothing truthful to say. Silence is the contract, not a shortcut.
		return nil
	}
	mapper := spool.NewHomeMapper()
	res, err := spool.Claim(mapper, harp)
	if err != nil {
		return fmt.Errorf("no mail delivered: %w", err)
	}
	var problems []string
	for _, p := range res.Problems {
		problems = append(problems, p.Error())
	}
	if len(res.Entries) == 0 {
		return joinProblems(problems)
	}
	frames := make([]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		frames = append(frames, coord.FrameCoordinatorDelivery(e.Message.FromHarp, e.Message.Kind, e.Message.Body))
	}
	out := claude.UserPromptSubmitOutput{HookSpecificOutput: &claude.AdditionalContextOutput{
		HookEventName:     claude.HookEventUserPromptSubmit,
		AdditionalContext: strings.Join(frames, "\n\n"),
	}}
	// Encoded to bytes first so a failure cannot leave a partial envelope
	// on the engine's input channel.
	body, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("%d message(s) left claimed, not delivered: %w", len(res.Entries), err)
	}
	if _, err := cmd.OutOrStdout().Write(append(body, '\n')); err != nil {
		// Delivery is the write. What was not written stays in in/claimed/,
		// where the next turn's Claim hands it out again.
		return fmt.Errorf("%d message(s) left claimed, not delivered: %w", len(res.Entries), err)
	}
	for _, e := range res.Entries {
		if err := spool.Ack(mapper, harp, e.Ref.Name); err != nil && !errors.Is(err, spool.ErrAlreadyGone) {
			problems = append(problems, fmt.Sprintf("%s was delivered but could not be acknowledged and will be delivered again: %v", e.Ref, err))
		}
	}
	return joinProblems(problems)
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
