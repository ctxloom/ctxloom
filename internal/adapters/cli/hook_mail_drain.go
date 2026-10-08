package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
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
engine sees delivered mail — to stdout as turn context, encoded by the codec
of the engine --engine names, and
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
//
// It drains only for the session OWNER's engine, the one the launch marks
// with sessions.EnvSessionOwner (sessionOwnerEnv). Any other engine that
// fires this hook — a delegated child loading a trusted repository's own
// settings file, which names it — carries its own harp, and claiming that
// spool would race the runner that delivers the child's mail: such an
// engine is handed no harp, so it claims nothing and says nothing.
func runHookMailDrain(cmd *cobra.Command, args []string) error {
	harp := ""
	if sessionOwnerEnv() {
		harp = os.Getenv(agent.SessionHarpEnv)
	}
	if err := drainMailFor(cmd, harp); err != nil {
		clidiag.Warn(mailDrainProg, "%v", err)
	}
	return nil
}

// drainMailFor resolves the firing engine and drains for harp; with no harp
// it reads stdin to EOF and claims nothing (see drainMail).
func drainMailFor(cmd *cobra.Command, harp string) error {
	if harp == "" {
		_, _ = io.Copy(io.Discard, cmd.InOrStdin())
		return nil
	}
	kind, err := firingEngine(cmd)
	if err != nil {
		_, _ = io.Copy(io.Discard, cmd.InOrStdin())
		return fmt.Errorf("no mail delivered: %w", err)
	}
	return drainMail(afero.NewOsFs(), cmd, kind.Hooks(), harp)
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
func drainMail(fs afero.Fs, cmd *cobra.Command, codec engine.HookCodec, harp string) error {
	// Read to EOF before anything can return: closing stdin early would be
	// reported by some engines as a failed hook.
	raw, _ := io.ReadAll(cmd.InOrStdin())
	if harp == "" {
		// A plain engine session ctxloom did not launch, or an engine that is
		// not the session owner: no spool this hook may read, nothing
		// truthful to say. Silence is the contract, not a shortcut.
		return nil
	}
	mapper := spool.NewHomeMapper()
	// An undecodable payload is a turn with no prompt we can read: never a
	// wake, so never blocked — and NAMED, because a wake that is never
	// redeemed refuses every later one, and the cause must be findable.
	var problems []string
	ev, err := codec.Decode(wire.HookEventTurnStart, raw)
	if err != nil {
		problems = append(problems, fmt.Sprintf("the turn_start payload did not decode, so it was not checked for a wake: %v", err))
	}
	isWake, wakeProblems := redeemWakeNonce(fs, mapper, harp, ev.Prompt)
	problems = append(problems, wakeProblems...)
	res, err := spool.Claim(fs, mapper, harp)
	if err != nil {
		return fmt.Errorf("no mail delivered: %w", err)
	}
	for _, p := range res.Problems {
		problems = append(problems, p.Error())
	}
	if len(res.Entries) == 0 {
		if isWake {
			problems = append(problems, blockStaleWake(cmd, codec)...)
		}
		return joinProblems(problems)
	}
	frames := make([]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		frames = append(frames, runner.FrameCoordinatorDelivery(coord.Message{
			From: e.Message.FromHarp, Kind: e.Message.Kind, ID: e.Identity(), InReplyTo: e.Message.InReplyTo, Body: e.Message.Body,
		}))
	}
	if err := writeHookResponse(cmd, codec, wire.HookEventTurnStart, engine.HookResponse{Context: strings.Join(frames, "\n\n")}); err != nil {
		// Delivery is the write. What was not written stays in in/claimed/,
		// where the next turn's Claim hands it out again.
		return fmt.Errorf("%d message(s) left claimed, not delivered: %w", len(res.Entries), err)
	}
	problems = append(problems, ackDelivered(fs, mapper, harp, res.Entries)...)
	// This turn delivered what every armed wake announced: answer them all,
	// or a wake held or lost upstream refuses every later one.
	if _, err := spool.ClearWakes(fs, mapper, harp); err != nil {
		problems = append(problems, fmt.Sprintf("the wakes this delivery answered could not be cleared, and may refuse the next wake: %v", err))
	}
	return joinProblems(problems)
}

// redeemWakeNonce consumes the wake nonce a prompt carries, reporting whether
// the prompt was a wake and, when the nonce could not be redeemed, why.
func redeemWakeNonce(fs afero.Fs, mapper spool.PathMapper, harp, prompt string) (bool, []string) {
	nonce, isWake := engine.WakeNonce(prompt)
	if !isWake {
		return false, nil
	}
	if _, err := spool.ConsumeWake(fs, mapper, harp, nonce); err != nil {
		return true, []string{fmt.Sprintf("wake %s was not redeemed: %v", nonce, err)}
	}
	return true, nil
}

// blockStaleWake blocks a wake whose mail was already delivered, reporting
// when the block could not be written.
func blockStaleWake(cmd *cobra.Command, codec engine.HookCodec) []string {
	if err := writeHookResponse(cmd, codec, wire.HookEventTurnStart, engine.HookResponse{
		Block:  true,
		Reason: "ctxloom: the mail this wake announced was already delivered",
	}); err != nil {
		return []string{fmt.Sprintf("a stale wake could not be blocked: %v", err)}
	}
	return nil
}

// ackDelivered acknowledges each delivered entry, reporting the ones that
// could not be (and so will be delivered again); one already gone is fine.
func ackDelivered(fs afero.Fs, mapper spool.PathMapper, harp string, entries []spool.Entry) []string {
	var problems []string
	for _, e := range entries {
		if err := spool.Deliver(fs, mapper, e.Ref, e.Identity(), time.Now()); err != nil && !errors.Is(err, spool.ErrAlreadyGone) {
			problems = append(problems, fmt.Sprintf("%s was delivered but could not be acknowledged and will be delivered again: %v", e.Ref, err))
		}
	}
	return problems
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
