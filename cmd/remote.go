package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/operations"
)

var remoteCmd = &cobra.Command{
	Use:   "remote",
	Short: "Manage remote fragment/prompt sources",
	Long: `Manage remote sources for fragments and prompts.

Remote sources are Git repositories (GitHub/GitLab) containing shared
fragments, prompts, and profiles. Use 'scm remote discover' to find
public repositories, and 'scm remote add' to register them.

Examples:
  scm remote add alice alice/scm              # Add GitHub remote (shorthand)
  scm remote add corp https://gitlab.com/corp/scm  # Add GitLab remote
  scm remote list                             # List configured remotes
  scm remote remove alice                     # Remove a remote`,
}

var remoteAddCmd = &cobra.Command{
	Use:   "add <name> <url>",
	Short: "Register a remote source",
	Long: `Register a remote repository as a source for fragments and prompts.

URL formats:
  alice/scm                      GitHub shorthand (expands to https://github.com/alice/scm)
  https://github.com/alice/scm   Full GitHub URL
  https://gitlab.com/corp/scm   Full GitLab URL
  git@github.com:alice/scm.git   SSH URL (converted to HTTPS)

Examples:
  scm remote add alice alice/scm
  scm remote add corp https://gitlab.com/corp/scm`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}

		result, err := operations.AddRemote(cmd.Context(), cfg, operations.AddRemoteRequest{
			Name: args[0],
			URL:  args[1],
		})
		if err != nil {
			return err
		}

		if result.Warning != "" {
			fmt.Printf("Warning: %s\n", result.Warning)
		}

		fmt.Printf("Added remote '%s' → %s\n", result.Name, result.URL)
		return nil
	},
}

var remoteRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Remove a remote source",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}

		result, err := operations.RemoveRemote(cmd.Context(), cfg, operations.RemoveRemoteRequest{
			Name: args[0],
		})
		if err != nil {
			return err
		}

		fmt.Printf("Removed remote '%s'\n", result.Name)
		return nil
	},
}

var remoteListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List configured remotes",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}

		result, err := operations.ListRemotes(cmd.Context(), cfg, operations.ListRemotesRequest{})
		if err != nil {
			return err
		}

		if result.Count == 0 {
			fmt.Println("No remotes configured.")
			fmt.Println("Use 'scm remote add <name> <url>' to add a remote.")
			fmt.Println("Use 'scm remote discover' to find public repositories.")
			return nil
		}

		fmt.Println("Configured remotes:")
		for _, r := range result.Remotes {
			fmt.Printf("  %-15s %s (version: %s)\n", r.Name, r.URL, r.Version)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(remoteCmd)

	remoteCmd.AddCommand(remoteAddCmd)
	remoteCmd.AddCommand(remoteRemoveCmd)
	remoteCmd.AddCommand(remoteListCmd)
}
