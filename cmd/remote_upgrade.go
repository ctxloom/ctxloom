package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// remoteUpgradeCmd is the apt-style "upgrade" verb: it advances every pinned
// dependency to the latest available. Mechanically this is a relock — it
// re-pins each reference at the remote's current HEAD — hence it calls
// operations.Relock. The verb is "upgrade" (not "relock") to mirror apt's
// `update` (refresh the index) / `upgrade` (advance installed) pairing and to
// stay distinct from the schema "upgrade" handled elsewhere.
var remoteUpgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade pinned dependencies to the latest available",
	Long: `Re-pin every dependency in .ctxloom/lock.yaml at the remote's current HEAD.

Mirrors apt: where 'remote update' refreshes the local clones (the index),
'remote upgrade' advances your pins to the newest commit on each referenced
profile, parent, and bundle by walking the live dependency graph.

Unlike 'remote lock', which records the install-time SHA of each installed
file, 'upgrade' re-resolves every entry against the current default-branch HEAD.
It works even when lock.yaml does not exist yet, so it also rebuilds a deleted
or missing lockfile.

Examples:
  ctxloom remote upgrade                 # Advance all pins to current HEAD`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := loadConfigOrFallback(GetConfig, os.Stderr)

		fmt.Println("Upgrading pinned dependencies to current HEAD...")

		result, err := operations.Relock(cmd.Context(), cfg, operations.RelockRequest{})
		if err != nil {
			return err
		}

		switch result.Status {
		case "empty":
			fmt.Println(result.Message)
		default:
			fmt.Printf("\nRegenerated %s with %d entries.\n", result.Path, result.ItemCount)
		}

		if result.Failed > 0 {
			fmt.Printf("Skipped %d unresolvable entr%s:\n", result.Failed, plural(result.Failed, "y", "ies"))
			for _, e := range result.Errors {
				fmt.Printf("  - %s\n", e)
			}
		}

		return nil
	},
}

func init() {
	remoteCmd.AddCommand(remoteUpgradeCmd)
}
