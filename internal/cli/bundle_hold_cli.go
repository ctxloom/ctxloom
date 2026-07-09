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
	Use:     "hold <name>",
	Aliases: []string{"pin"},
	Short:   "Hold an item at its locked SHA so `upgrade` won't advance it",
	Long: `Set the hold flag on a bundle's active lockfile entry so 'remote upgrade'
leaves it frozen at its currently-locked commit — even when its version
constraint would otherwise allow a newer one. The hold is policy only: it does
not edit the manifest, and the held SHA still satisfies the constraint. Unhold to
let it move again. ('hold' was formerly 'pin', which now risks confusion with an
exact version pin in the manifest.)`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		_, err = holdItem(cfg, args[0], cmd.OutOrStdout())
		return err
	},
}

// holdItem sets the hold flag on name's active lockfile entry and reports the
// outcome to out. Returns whether a hold was placed.
func holdItem(cfg *config.Config, name string, out io.Writer) (bool, error) {
	found, err := operations.SetItemPin(cfg, name, true)
	if err != nil {
		return false, err
	}
	if !found {
		fmt.Fprintf(out, "%q is not in the active lockfile; nothing to hold.\n", name)
		return false, nil
	}
	fmt.Fprintf(out, "Held %q at its locked SHA.\n", name)
	return true, nil
}

var bundleUnholdCmd = &cobra.Command{
	Use:     "unhold <name>",
	Aliases: []string{"unpin"},
	Short:   "Release a hold so `upgrade` can advance the item again",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		found, err := operations.SetItemPin(cfg, args[0], false)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if !found {
			fmt.Fprintf(out, "%q is not in the active lockfile; nothing to unhold.\n", args[0])
			return nil
		}
		fmt.Fprintf(out, "Released hold on %q; the next 'remote upgrade' may advance it.\n", args[0])
		return nil
	},
}
