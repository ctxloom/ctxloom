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
	Long: `Re-pin each local profile's direct remote references to current HEAD and
stage the resulting dependency changes for review.

Mirrors apt: where 'remote update' refreshes the local clones (the index),
'remote upgrade' advances your pins to the newest commit. The changes are NOT
applied directly — they are staged in the pending lockfile so you can review
them ('ctxloom bundle review') and approve or decline. Passive 'remote sync'
installs exactly what is already pinned and never stages a change.

Examples:
  ctxloom remote upgrade                 # Stage HEAD pins for review`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := loadConfigOrFallback(GetConfig, os.Stderr)

		fmt.Println("Resolving current HEAD for pinned dependencies...")

		res, err := operations.StageUpgrade(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		if res.TrustedApplied > 0 {
			fmt.Printf("Applied %d change(s) from trusted remote(s).\n", res.TrustedApplied)
		}
		if res.Staged == 0 {
			if res.TrustedApplied == 0 {
				fmt.Println("Everything is up to date.")
			}
			return nil
		}

		fmt.Printf("Staged %d dependency change(s) for review.\n", res.Staged)
		fmt.Println("Review with: ctxloom bundle review")
		fmt.Println("Apply with:  ctxloom bundle approve   (or decline)")
		return nil
	},
}

func init() {
	remoteCmd.AddCommand(remoteUpgradeCmd)
}
