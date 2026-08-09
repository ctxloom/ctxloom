package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// Bundle hold/unhold — dependency management over the active lockfile. A hold
// freezes a bundle at its currently-locked SHA so `remote upgrade` won't advance
// it; unhold releases that freeze. (Per-item content review lives in the
// separate `ctxloom review` porcelain.)

var bundleHoldCmd = &cobra.Command{
	Use:   "hold <name>",
	Short: "Hold an item at its locked SHA so `upgrade` won't advance it",
	Long: `Set the hold flag on a bundle's active lockfile entry so 'remote upgrade'
leaves it frozen at its currently-locked commit — even when its version
constraint would otherwise allow a newer one. The hold is policy only: it does
not edit the manifest, and the held SHA still satisfies the constraint. Unhold
to let it move again. Called 'hold' rather than 'pin' to keep clear of an exact
version pin in the manifest, a different concept.`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleHold,
}

func runBundleHold(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	return holdItem(cfg, args[0], cmd.OutOrStdout(), cmd.ErrOrStderr())
}

// holdItem sets the hold flag on name's active lockfile entry and reports the
// outcome to out (or, when there was nothing to flip, to errOut via
// reportNothingToHold).
func holdItem(cfg *config.Config, name string, out, errOut io.Writer) error {
	found, err := operations.SetItemPin(cfg, name, true)
	if err != nil {
		return err
	}
	if !found {
		return reportNothingToHold(cfg, name, "hold", errOut)
	}
	fmt.Fprintf(out, "Held %q at its locked SHA.\n", name)
	return nil
}

// reportNothingToHold handles the branch where SetItemPin flipped nothing: the
// name has no entry in the active lockfile. That covers two situations the
// lockfile cannot tell apart, and they are not the same kind of event.
//
//   - The name IS a bundle this project carries locally. A local bundle has no
//     upstream and no lockfile entry, so `remote upgrade` can never advance it —
//     the guarantee "hold" exists to give is already unconditionally true, and
//     "unhold" has nothing to release either. The user's requested postcondition
//     holds. That is a benign no-op: say so and exit 0. This is the contract the
//     acceptance suite has specified since the command was `bundle pin`
//     (tests/acceptance/features/bundle.feature, "Holding a local bundle reports
//     it is not lockfile-tracked" — both hold AND unhold asserted to succeed).
//
//   - The name resolves to NOTHING — a typo, the wrong project, or a `remote
//     pull` that never ran. Here the user asked to freeze a dependency, nothing
//     was frozen, and exit 0 tells them it was: the exit-0-on-failure family.
//     Refuse, and name the recovery.
//
// The notice is a diagnostic about work NOT done, so it rides stderr even on the
// exit-0 path — `ctxloom bundle hold x > pins.txt` should not collect it. (The
// old code sent it to stdout on this exit-0 path, which was the wrong stream.)
func reportNothingToHold(cfg *config.Config, name, verb string, errOut io.Writer) error {
	if _, err := operations.GetBundle(cfg, name); err != nil {
		return fmt.Errorf("%q is neither a local bundle nor an entry in the active lockfile, so there was nothing to %s — run `ctxloom remote pull` to lock it, or `ctxloom bundle list` to check the name", name, verb)
	}
	fmt.Fprintf(errOut, "%q is a local bundle, not tracked in the active lockfile; nothing to %s. `remote upgrade` never advances a local bundle, so it is already frozen.\n", name, verb)
	return nil
}

var bundleUnholdCmd = &cobra.Command{
	Use:   "unhold <name>",
	Short: "Release a hold so `upgrade` can advance the item again",
	Args:  cobra.ExactArgs(1),
	RunE:  runBundleUnhold,
}

func runBundleUnhold(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	found, err := operations.SetItemPin(cfg, args[0], false)
	if err != nil {
		return err
	}
	// See reportNothingToHold: a local bundle has nothing to release and
	// succeeds; a name that resolves to nothing is refused.
	if !found {
		return reportNothingToHold(cfg, args[0], "unhold", cmd.ErrOrStderr())
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Released hold on %q; the next 'remote upgrade' may advance it.\n", args[0])
	return nil
}
