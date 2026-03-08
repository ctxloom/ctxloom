package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/SophisticatedContextManager/scm/internal/operations"
)

var lockCmd = &cobra.Command{
	Use:    "lock",
	Short:  "Generate lockfile from installed items",
	Hidden: true, // Use 'scm remote lock' instead
	Long: `Generate a lockfile (.scm/lock.yaml) from currently installed remote items.

The lockfile records exact versions (SHA commits) of all installed bundles and
profiles, enabling reproducible installations across machines and CI environments.

After modifying your installed items, run 'scm lock' to update the lockfile.
Commit the lockfile to version control for reproducible builds.

Related commands:
  scm install              Install all items from lockfile
  scm update               Check for and apply updates

Examples:
  scm lock                 Generate/update lockfile
  scm install              Install all from lockfile`,
	RunE: runLock,
}

func runLock(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	result, err := operations.LockDependencies(cmd.Context(), cfg, operations.LockDependenciesRequest{})
	if err != nil {
		return err
	}

	if result.Status == "empty" {
		fmt.Println("No remote items with source metadata found.")
		fmt.Println("Install items with: scm install <remote>/<name>")
		return nil
	}

	fmt.Printf("Lockfile %s: %s\n", result.Status, result.Path)
	if result.ItemCount > 0 {
		fmt.Printf("  Items: %d\n", result.ItemCount)
	}
	if result.Message != "" {
		fmt.Println(result.Message)
	}

	return nil
}

func init() {
	rootCmd.AddCommand(lockCmd)
}
