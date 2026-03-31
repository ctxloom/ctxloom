package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

var remoteInstallForce bool

var remoteLockCmd = &cobra.Command{
	Use:   "lock",
	Short: "Generate lockfile from installed remote items",
	Long: `Generate a lockfile (.ctxloom/lock.yaml) from currently installed remote items.

The lockfile pins exact versions (commit SHAs) of remote dependencies for
reproducible installations. Commit this file to your repository.

Examples:
  ctxloom remote lock              # Generate lockfile from installed items`,
	RunE: runRemoteLock,
}

var remoteInstallCmd = &cobra.Command{
	Use:    "install",
	Short:  "Install items from lockfile",
	Hidden: true, // Use top-level 'ctxloom install' instead
	Long: `Install all items specified in the lockfile (.ctxloom/lock.yaml).

This is useful for CI/CD pipelines and setting up new development environments
with exact versions of remote dependencies.

Examples:
  ctxloom remote install           # Install from lockfile
  ctxloom remote install --force   # Skip confirmation prompts`,
	RunE: runRemoteInstall,
}

var remoteOutdatedCmd = &cobra.Command{
	Use:    "outdated",
	Short:  "Show items with newer versions available",
	Hidden: true, // Use 'ctxloom update' to check for outdated items
	Long: `Check if any locked items have newer versions available.

Compares locked SHAs against the latest commits on the default branch
of each remote.

Examples:
  ctxloom remote outdated`,
	RunE: runRemoteOutdated,
}

func runRemoteLock(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	result, err := operations.LockDependencies(cmd.Context(), cfg, operations.LockDependenciesRequest{})
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

func runRemoteInstall(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	result, err := operations.InstallDependencies(cmd.Context(), cfg, operations.InstallDependenciesRequest{
		Force: remoteInstallForce,
	})
	if err != nil {
		return err
	}

	if result.Status == "empty" {
		fmt.Println("No entries in lockfile.")
		fmt.Println("Generate one with: ctxloom remote lock")
		return nil
	}

	fmt.Printf("Installing %d items from lockfile...\n\n", result.Total)

	// Show errors if any
	for _, e := range result.Errors {
		fmt.Printf("Error: %s\n", e)
	}

	fmt.Println()
	fmt.Printf("Installed: %d, Failed: %d\n", result.Installed, result.Failed)

	return nil
}

func runRemoteOutdated(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	fmt.Printf("Checking items for updates...\n\n")

	result, err := operations.CheckOutdated(cmd.Context(), cfg, operations.CheckOutdatedRequest{})
	if err != nil {
		return err
	}

	if result.Status == "empty" {
		fmt.Println("No entries in lockfile.")
		return nil
	}

	if result.Status == "up_to_date" {
		fmt.Println("All items are up to date!")
		return nil
	}

	fmt.Printf("Found %d outdated items:\n\n", result.Count)
	fmt.Printf("  %-10s │ %-25s │ %-10s │ %-10s\n", "Type", "Reference", "Locked", "Latest")
	fmt.Printf("────────────┼───────────────────────────┼────────────┼────────────\n")

	for _, o := range result.Items {
		ref := o.Reference
		if len(ref) > 23 {
			ref = ref[:20] + "..."
		}

		fmt.Printf("  %-10s │ %-25s │ %-10s │ %-10s\n",
			o.Type, ref, o.LockedSHA, o.LatestSHA)
	}

	fmt.Println()
	fmt.Println("Update with: ctxloom install <reference> --force")

	return nil
}

func init() {
	remoteCmd.AddCommand(remoteLockCmd)
	remoteCmd.AddCommand(remoteInstallCmd)
	remoteCmd.AddCommand(remoteOutdatedCmd)

	remoteInstallCmd.Flags().BoolVarP(&remoteInstallForce, "force", "f", false,
		"Skip confirmation prompts")
}
