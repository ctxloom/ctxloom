package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/SophisticatedContextManager/scm/internal/remote"
)

var remoteReplaceCmd = &cobra.Command{
	Use:   "replace",
	Short: "Manage local overrides for remote items",
	Long: `Manage replace directives for local development.

Replace directives let you use a local file instead of fetching
from a remote. Useful for testing changes before publishing.

Examples:
  scm remote replace add alice/security ./local/security.yaml
  scm remote replace remove alice/security
  scm remote replace list`,
}

var remoteReplaceAddCmd = &cobra.Command{
	Use:   "add <reference> <local-path>",
	Short: "Add a replace directive",
	Long: `Add a replace directive to use a local file instead of a remote.

The local path must exist and point to a valid YAML file.

Examples:
  scm remote replace add alice/security ./local/security.yaml
  scm remote replace add corp/compliance ../corp-scm/compliance.yaml`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref := args[0]
		localPath := args[1]

		manager, err := remote.NewReplaceManager("")
		if err != nil {
			return err
		}

		if err := manager.Add(ref, localPath); err != nil {
			return err
		}

		fmt.Printf("Added replace: %s → %s\n", ref, localPath)
		return nil
	},
}

var remoteReplaceRemoveCmd = &cobra.Command{
	Use:   "remove <reference>",
	Short: "Remove a replace directive",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref := args[0]

		manager, err := remote.NewReplaceManager("")
		if err != nil {
			return err
		}

		if err := manager.Remove(ref); err != nil {
			return err
		}

		fmt.Printf("Removed replace: %s\n", ref)
		return nil
	},
}

var remoteReplaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all replace directives",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := remote.NewReplaceManager("")
		if err != nil {
			return err
		}

		replaces := manager.List()
		if len(replaces) == 0 {
			fmt.Println("No replace directives configured.")
			return nil
		}

		fmt.Println("Replace directives:")
		for ref, path := range replaces {
			fmt.Printf("  %s → %s\n", ref, path)
		}

		return nil
	},
}

func init() {
	remoteCmd.AddCommand(remoteReplaceCmd)
	remoteReplaceCmd.AddCommand(remoteReplaceAddCmd)
	remoteReplaceCmd.AddCommand(remoteReplaceRemoveCmd)
	remoteReplaceCmd.AddCommand(remoteReplaceListCmd)
}
