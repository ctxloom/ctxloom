package cli

import (
	"errors"
	"os"

	"github.com/spf13/afero"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/memory"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/turnchange"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// nextStepProg names this hook on the clidiag warning channel.
const nextStepProg = "ctxloom hook next-step"

var hookNextStepCmd = &cobra.Command{
	Use:    "next-step",
	Hidden: true, // Machine callback (TurnEnd hook) — not for direct use
	Short:  "Capture what the agent was about to do next (internal — used by the TurnEnd hook)",
	Long: `Reads a turn_end hook payload on stdin, through the codec of the engine
--engine names, and stores the last assistant message
of the turn as this harp's next step, replacing the one stored a turn earlier.

There is no LLM call and no model output: the last thing the agent said is
taken verbatim off the transcript the engine already wrote.

Compaction reads what this captures as a task hint. Compressing a transcript
without knowing what the resuming session means to do is the weaker regime —
the material the next step needs is discarded as readily as anything else — and
the only moment that intention can be had cheaply is while the agent that holds
it is still live. That is why the capture rides the end of every TURN and
overwrites: whatever the final turn said is what survives the session.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runHookNextStep,
}

// runHookNextStep never reports a nonzero exit. A TurnEnd hook that fails is a
// hook that can stall the turn it fires on, and a missed next step costs a
// less-steered compaction later — not a broken session now. Every reason the
// capture did not happen is NAMED on the diagnostic channel instead, because
// the alternative is this project's characteristic bug: exit 0, and zero bytes
// written, with nothing said about which.
func runHookNextStep(cmd *cobra.Command, args []string) error {
	kind, err := firingEngine(cmd)
	if err == nil {
		err = captureNextStep(cmd, kind.Hooks())
	}
	if err != nil {
		clidiag.Warn(nextStepProg, "no next step captured: %v", err)
	}
	return nil
}

// captureNextStep does the work and RETURNS its failure rather than warning
// itself, so a test can assert which reason fired without parsing stderr.
func captureNextStep(cmd *cobra.Command, codec engine.HookCodec) error {
	harp := os.Getenv(agent.SessionHarpEnv)
	if harp == "" {
		return errors.New("no " + agent.SessionHarpEnv + " in the environment: there is no harp to store a next step under")
	}
	ev, err := readHookEvent(cmd, codec, wire.HookEventTurnEnd)
	if err != nil {
		return err
	}
	// The transcript is read through the ACTIVE engine's own adapter. This
	// hook is installed on every hooking backend, so assuming one engine's
	// format here is how the capture fires every turn on the others and
	// stores nothing.
	adapter, src, err := operations.ResolveTurnTranscript(cmd.Context(), afero.NewOsFs(), App().Engines(), harp, ev.Transcript)
	if err != nil {
		return err
	}
	evs, err := turnchange.ReadTranscript(cmd.Context(), afero.NewOsFs(), adapter, src)
	if err != nil {
		return err
	}
	text := turnchange.LastAssistantText(evs)
	if text == "" {
		return errors.New("the ending turn produced no assistant message")
	}
	// WriteNextStep refuses an empty write, so the previous turn's capture
	// stands rather than being erased by a turn that had nothing to say.
	return memory.WriteNextStep(afero.NewOsFs(), harp, text)
}

func init() {
	hookCmd.AddCommand(hookNextStepCmd)
}
