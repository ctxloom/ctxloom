package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
)

var remoteRelockCmd = &cobra.Command{
	Use:   "relock",
	Short: "Regenerate the lockfile from configured profiles",
	Long: `Regenerate .ctxloom/lock.yaml from the project's configured profiles.

Unlike 'remote lock', which records the install-time SHA of each installed
file, 'relock' walks the live dependency graph (every configured profile, its
parents, and their bundles) and re-pins each entry at the current default-branch
HEAD.

This works even when lock.yaml does not exist yet, so it is the way to rebuild
a deleted or missing lockfile.

Examples:
  ctxloom remote relock                  # Rebuild lock.yaml from profiles`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := loadConfigOrFallback(GetConfig, os.Stderr)

		fmt.Println("Regenerating lockfile from configured profiles...")

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
	remoteCmd.AddCommand(remoteRelockCmd)
}
