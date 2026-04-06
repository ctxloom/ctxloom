package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

var remoteLockNoSync bool

var remoteLockCmd = &cobra.Command{
	Use:   "lock",
	Short: "Generate lockfile from installed remote items",
	Long: `Generate a lockfile (.ctxloom/lock.yaml) from currently installed remote items.

The lockfile pins exact versions (commit SHAs) of remote dependencies for
reproducible installations. Commit this file to your repository.

By default, sync runs first to ensure all dependencies are installed before
locking their versions. This prevents generating an incomplete lockfile if the
ephemeral directory was cleared. Use --no-sync to skip this behavior.

Examples:
  ctxloom remote lock              # Sync then generate lockfile
  ctxloom remote lock --no-sync    # Generate lockfile without syncing`,
	RunE: runRemoteLock,
}

func runRemoteLock(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if !remoteLockNoSync {
		fmt.Println("Syncing dependencies before generating lockfile...")
	}

	result, err := operations.LockDependencies(cmd.Context(), cfg, operations.LockDependenciesRequest{
		SkipSync: remoteLockNoSync,
	})
	if err != nil {
		return err
	}

	if result.Status == "empty" {
		fmt.Println("No remote items with source metadata found.")
		fmt.Println("Install items with: ctxloom install <remote>/<name>")
		return nil
	}

	fmt.Printf("Generated %s with %d entries\n", result.Path, result.ItemCount)
	fmt.Println("Commit this file to your repository for reproducible installations.")

	return nil
}

func init() {
	remoteCmd.AddCommand(remoteLockCmd)

	remoteLockCmd.Flags().BoolVar(&remoteLockNoSync, "no-sync", false,
		"Skip syncing dependencies before generating lockfile")
}
