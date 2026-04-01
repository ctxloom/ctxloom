package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
)

var remoteCmd = &cobra.Command{
	Use:   "remote",
	Short: "Manage remotes and discover content",
	Long: `Manage remote sources and discover bundles/profiles.

Remote sources are Git repositories (GitHub/GitLab) containing shared
bundles and profiles.

Registry:
  ctxloom remote list                    List configured remotes
  ctxloom remote add <name> <url>        Register a remote
  ctxloom remote rm <name>               Remove a remote
  ctxloom remote default [name]          Get/set default remote

Discovery:
  ctxloom remote search <query>          Search for bundles/profiles
  ctxloom remote browse <remote>         Browse a remote's contents
  ctxloom remote discover                Find ctxloom repositories

Examples:
  ctxloom remote add alice alice/ctxloom
  ctxloom remote search "golang testing"
  ctxloom remote browse ctxloom-default`,
}

var remoteAddCmd = &cobra.Command{
	Use:   "add <name> <url>",
	Short: "Register a remote source",
	Long: `Register a remote repository as a source for fragments and prompts.

URL formats:
  alice/ctxloom                      GitHub shorthand (expands to https://github.com/alice/ctxloom)
  https://github.com/alice/ctxloom   Full GitHub URL
  https://gitlab.com/corp/ctxloom   Full GitLab URL
  git@github.com:alice/ctxloom.git   SSH URL (converted to HTTPS)

Examples:
  ctxloom remote add alice alice/ctxloom
  ctxloom remote add corp https://gitlab.com/corp/ctxloom`,
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
			fmt.Println("Use 'ctxloom remote add <name> <url>' to add a remote.")
			fmt.Println("Use 'ctxloom remote discover' to find public repositories.")
			return nil
		}

		fmt.Println("Configured remotes:")
		for _, r := range result.Remotes {
			defaultMark := ""
			if r.Name == result.Default {
				defaultMark = " (default)"
			}
			fmt.Printf("  %-15s %s (version: %s)%s\n", r.Name, r.URL, r.Version, defaultMark)
		}

		return nil
	},
}

var remoteDefaultCmd = &cobra.Command{
	Use:   "default [name]",
	Short: "Get or set the default remote",
	Long: `Get or set the default remote for push operations.

Without arguments, displays the current default remote.
With a name, sets that remote as the default.
Use --clear to remove the default.

Examples:
  ctxloom remote default              # Show current default
  ctxloom remote default ctxloom-default   # Set default to ctxloom-default
  ctxloom remote default --clear      # Clear the default`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRemoteDefault,
}

var remoteDefaultClear bool

func runRemoteDefault(cmd *cobra.Command, args []string) error {
	registry, err := remote.NewRegistry("")
	if err != nil {
		return err
	}

	// Clear the default
	if remoteDefaultClear {
		if err := registry.SetDefault(""); err != nil {
			return err
		}
		fmt.Println("Cleared default remote.")
		return nil
	}

	// Get current default
	if len(args) == 0 {
		defaultRemote := registry.GetDefault()
		if defaultRemote == "" {
			fmt.Println("No default remote set.")
			fmt.Println("Set one with: ctxloom remote default <name>")
		} else {
			fmt.Printf("Default remote: %s\n", defaultRemote)
		}
		return nil
	}

	// Set new default
	name := args[0]
	if err := registry.SetDefault(name); err != nil {
		return err
	}

	fmt.Printf("Set default remote to: %s\n", name)
	return nil
}

var (
	remoteSyncForce bool
	remoteSyncLock  bool
)

var remoteSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync dependencies from profiles",
	Long: `Sync remote bundles and profiles referenced in your configuration.

This fetches all remote dependencies declared in your profiles, updates
the lockfile, and applies hooks.

Examples:
  ctxloom remote sync                    # Sync all dependencies
  ctxloom remote sync --force            # Re-pull even if already installed
  ctxloom remote sync --no-lock          # Skip lockfile update`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		fmt.Println("Syncing dependencies...")

		result, err := operations.SyncDependencies(cmd.Context(), cfg, operations.SyncDependenciesRequest{
			Force:      remoteSyncForce,
			Lock:       remoteSyncLock,
			ApplyHooks: true,
		})
		if err != nil {
			return err
		}

		if result.Total == 0 {
			fmt.Println("No remote dependencies to sync.")
			return nil
		}

		fmt.Printf("\nSynced %d items:\n", result.Total)
		if result.Installed > 0 {
			fmt.Printf("  Installed: %d\n", result.Installed)
		}
		if result.Updated > 0 {
			fmt.Printf("  Updated: %d\n", result.Updated)
		}
		if len(result.Skipped) > 0 {
			fmt.Printf("  Skipped (already installed): %d\n", len(result.Skipped))
		}
		if result.Errors > 0 {
			fmt.Printf("  Failed: %d\n", result.Errors)
			for _, item := range result.Failed {
				fmt.Printf("    - %s: %s\n", item.Reference, item.Error)
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(remoteCmd)

	remoteCmd.AddCommand(remoteAddCmd)
	remoteCmd.AddCommand(remoteRemoveCmd)
	remoteCmd.AddCommand(remoteListCmd)
	remoteCmd.AddCommand(remoteDefaultCmd)
	remoteCmd.AddCommand(remoteSyncCmd)

	remoteDefaultCmd.Flags().BoolVar(&remoteDefaultClear, "clear", false,
		"Clear the default remote")

	remoteSyncCmd.Flags().BoolVarP(&remoteSyncForce, "force", "f", false,
		"Re-pull even if already installed")
	remoteSyncLock = true // default
	remoteSyncCmd.Flags().BoolVar(&remoteSyncLock, "lock", true,
		"Update lockfile after sync")
}
