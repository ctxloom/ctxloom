package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

var remoteCmd = &cobra.Command{
	Use:   "remote",
	Short: "Manage remotes and discover content",
	Long: `Manage remote sources and discover bundles/profiles.

Remote sources are Git repositories (GitHub or generic git) containing shared
bundles and profiles.

Registry:
  ctxloom remote list                    List configured remotes
  ctxloom remote add <name> <url>        Register a remote
  ctxloom remote rm <name>               Remove a remote
  ctxloom remote default <name>          Set the default remote
  ctxloom remote trust <name>            Auto-apply this remote's bundle changes
  ctxloom remote untrust <name>          Require review for this remote again

Discovery:
  ctxloom search <query>                 Search local and remote content
  ctxloom remote browse <remote>         Browse a remote's contents
  ctxloom remote discover                Find ctxloom repositories

Examples:
  ctxloom remote add alice alice/ctxloom
  ctxloom search "golang testing"
  ctxloom remote browse ctxloom-default`,
}

var remoteAddForge string

var remoteAddCmd = &cobra.Command{
	Use:   "add <name> <url>",
	Short: "Register a remote source",
	Long: `Register a remote repository as a source for fragments and prompts.

URL formats:
  alice/ctxloom                      GitHub shorthand (expands to https://github.com/alice/ctxloom)
  https://github.com/alice/ctxloom   Full GitHub URL
  https://git.example.com/corp/ctxloom   Generic git host URL
  git@github.com:alice/ctxloom.git   SSH URL (converted to HTTPS)

Forge selection:
  Without --forge, the forge resolves from the URL host: github.com (and the
  owner/repo shorthand) use the rich GitHub adapter; every other host uses the
  generic git adapter (clone + local read, ambient git auth). Pass --forge to
  override — "github", "git", or the label of a forges: entry (e.g. a GitHub
  Enterprise instance).

Examples:
  ctxloom remote add alice alice/ctxloom
  ctxloom remote add corp https://git.example.com/corp/ctxloom
  ctxloom remote add corp https://git.example.com/corp/ctxloom --forge git
  ctxloom remote add work https://github.mycorp.com/me/ctxloom --forge work-ghe`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}

		result, err := operations.AddRemote(cmd.Context(), cfg, operations.AddRemoteRequest{
			Name:  args[0],
			URL:   args[1],
			Forge: remoteAddForge,
		})
		if err != nil {
			return err
		}

		if result.Warning != "" {
			clidiag.Warn("ctxloom", "%s", result.Warning)
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

		return emit(cmd, result, func() error {
			out := cmd.OutOrStdout()
			if result.Count == 0 {
				fmt.Fprintln(out, "No remotes configured.")
				fmt.Fprintln(out, "Use 'ctxloom remote add <name> <url>' to add a remote.")
				fmt.Fprintln(out, "Use 'ctxloom remote discover' to find public repositories.")
				return nil
			}

			fmt.Fprintln(out, "Configured remotes:")
			for _, r := range result.Remotes {
				var marks string
				if r.Name == result.Default {
					marks += " (default)"
				}
				if r.Trusted {
					marks += " (trusted)"
				}
				fmt.Fprintf(out, "  %-15s %s%s\n", r.Name, r.URL, marks)
			}
			return nil
		})
	},
}

var remoteTrustCmd = &cobra.Command{
	Use:   "trust <name>",
	Short: "Trust a remote so its bundle changes auto-apply without review",
	Long: `Mark a remote as trusted. Bundle changes from a trusted remote are applied
automatically during sync, without surfacing for review. Any changes currently
pending from this remote are approved.

Revoke with 'ctxloom remote untrust <name>'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setRemoteTrust(cmd, args[0], true)
	},
}

var remoteUntrustCmd = &cobra.Command{
	Use:   "untrust <name>",
	Short: "Revoke trust from a remote so its changes require review again",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setRemoteTrust(cmd, args[0], false)
	},
}

// setRemoteTrust toggles a remote's trust flag and reports the outcome.
func setRemoteTrust(cmd *cobra.Command, name string, trust bool) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	result, err := operations.SetRemoteTrust(cmd.Context(), cfg, operations.SetRemoteTrustRequest{
		Name:  name,
		Trust: trust,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Remote '%s' is now %s.\n", result.Name, result.Status)
	return nil
}

var remoteDefaultCmd = &cobra.Command{
	Use:   "default <name>",
	Short: "Set the default remote",
	Long: `Set the default remote for push operations.

The current default is shown by 'ctxloom remote list' (marked "(default)").
Use --clear to remove the default.

Examples:
  ctxloom remote default ctxloom-default   # Set default to ctxloom-default
  ctxloom remote default --clear           # Clear the default`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRemoteDefault,
}

var remoteDefaultClear bool

func runRemoteDefault(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Clear the default.
	if remoteDefaultClear {
		if _, err := operations.SetDefaultRemote(cmd.Context(), cfg, operations.DefaultRemoteRequest{Name: ""}); err != nil {
			return err
		}
		fmt.Println("Cleared default remote.")
		return nil
	}

	// A name is required to set the default; the current default is visible via
	// `ctxloom remote list`.
	if len(args) == 0 {
		return fmt.Errorf("remote name required (or use --clear); see the current default in 'ctxloom remote list'")
	}

	// Set a new default.
	name := args[0]
	if _, err := operations.SetDefaultRemote(cmd.Context(), cfg, operations.DefaultRemoteRequest{Name: name}); err != nil {
		return err
	}
	fmt.Printf("Set default remote to: %s\n", name)
	return nil
}

var (
	remotePullForce bool
	remotePullLock  bool
)

var remotePullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull dependencies from profiles",
	Long: `Pull remote bundles and profiles referenced in your configuration.

This fetches all remote dependencies declared in your profiles, updates
the lockfile, and applies hooks.

Examples:
  ctxloom remote pull                    # Pull all dependencies
  ctxloom remote pull --force            # Re-pull even if already installed
  ctxloom remote pull --lock=false       # Skip lockfile update`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		fmt.Println("Pulling dependencies...")

		result, err := operations.SyncDependencies(cmd.Context(), cfg, operations.SyncDependenciesRequest{
			Force:      remotePullForce,
			Lock:       remotePullLock,
			ApplyHooks: true,
		})
		if err != nil {
			return err
		}

		if result.Total == 0 {
			fmt.Println("No remote dependencies to pull.")
			return nil
		}

		fmt.Printf("\nPulled %d items:\n", result.Total)
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
	remoteCmd.AddCommand(remotePullCmd)
	remoteCmd.AddCommand(remoteTrustCmd)
	remoteCmd.AddCommand(remoteUntrustCmd)

	remoteAddCmd.Flags().StringVar(&remoteAddForge, "forge", "",
		"Forge to bind this remote to: github, git, or a configured forges: label (default: resolve from URL host)")

	remoteDefaultCmd.Flags().BoolVar(&remoteDefaultClear, "clear", false,
		"Clear the default remote")

	remotePullCmd.Flags().BoolVarP(&remotePullForce, "force", "f", false,
		"Re-pull even if already installed")
	remotePullLock = true // default
	remotePullCmd.Flags().BoolVar(&remotePullLock, "lock", true,
		"Update lockfile after pull")
}
