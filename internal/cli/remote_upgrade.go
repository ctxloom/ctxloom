package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// remoteUpgradeCmd is the apt-style "upgrade" verb: it advances every unheld
// pinned dependency to the newest commit its version constraint allows and
// writes the result straight to the active lock. Where 'remote update' refreshes
// the local clones (the index), 'remote upgrade' advances your pins.
var remoteUpgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade pinned dependencies to the latest available",
	Long: `Re-resolve each local profile's dependency closure to the newest commit each
version constraint allows and write the advances straight to the active lock —
your profile YAML is never rewritten. A held entry ('ctxloom bundle hold') stays
frozen.

The lockfile is pure dependency pinning: upgrading a pin does not expose new
content to the agent. Any changed content from an untrusted source is withheld
until you accept it with 'ctxloom review'.

Mirrors apt: where 'remote update' refreshes the local clones (the index),
'remote upgrade' advances your pins to the newest commit. Passive 'remote pull'
installs exactly what is already pinned and never advances.

Examples:
  ctxloom remote upgrade                 # Advance pins to the latest available`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := loadConfigOrFallback(GetConfig, os.Stderr)

		fmt.Println("Resolving latest commits for pinned dependencies...")

		advanced, err := operations.UpgradeDependencies(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		if advanced == 0 {
			fmt.Println("Everything is up to date.")
			return nil
		}

		fmt.Printf("Advanced %d dependency pin(s).\n", advanced)
		fmt.Println("Changed content from untrusted sources is withheld until reviewed: ctxloom review")
		return nil
	},
}

func init() {
	remoteCmd.AddCommand(remoteUpgradeCmd)
}
