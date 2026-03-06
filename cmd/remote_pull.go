package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/operations"
)

var pullForce bool
var pullCascade bool
var pullNoCascade bool

// remoteProfilesCmd is the parent for profile-specific remote commands.
var remoteProfilesCmd = &cobra.Command{
	Use:   "profiles",
	Short: "Manage remote profiles",
	Long:  `Commands for searching, browsing, and pulling profiles from remote sources.`,
}

var remoteProfilesPullCmd = &cobra.Command{
	Use:   "pull <remote/path[@ref]>",
	Short: "Download a profile from a remote source",
	Long: `Download a profile from a remote source with security review.

The full content of the profile will be displayed before installation.
You must review and explicitly confirm before the profile is installed.

By default, all bundles referenced by the profile are also pulled (cascade).
Use --no-cascade to only pull the profile itself.

Examples:
  scm remote profiles pull alice/security-focused
  scm remote profiles pull corp/enterprise@v1.0.0
  scm remote profiles pull alice/go-developer --no-cascade`,
	Args: cobra.ExactArgs(1),
	RunE: runRemotePull("profile"),
}

// remoteBundlesCmd is the parent for bundle-specific remote commands.
var remoteBundlesCmd = &cobra.Command{
	Use:   "bundles",
	Short: "Manage remote bundles",
	Long:  `Commands for searching, browsing, and pulling bundles from remote sources.`,
}

var remoteBundlesPullCmd = &cobra.Command{
	Use:   "pull <remote/path[@ref]>",
	Short: "Download a bundle from a remote source",
	Long: `Download a bundle from a remote source with security review.

Bundles combine fragments, prompts, and optionally MCP server configurations.
The full content will be displayed before installation.
You must review and explicitly confirm before the bundle is installed.

WARNING: Bundles with MCP servers execute commands on your system.
Only install bundles from sources you trust.

Examples:
  scm remote bundles pull github:bundles/core-practices
  scm remote bundles pull github:bundles/go-development@v1.0.0`,
	Args: cobra.ExactArgs(1),
	RunE: runRemotePull("bundle"),
}

// runRemotePull returns a RunE function for pulling items of the specified type.
func runRemotePull(itemType string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}

		// Cascade is enabled by default for profiles, disabled for others
		cascade := pullCascade
		if itemType == "profile" && !pullNoCascade {
			cascade = true
		}

		result, err := operations.PullItem(cmd.Context(), cfg, operations.PullItemRequest{
			Reference: args[0],
			ItemType:  itemType,
			Force:     pullForce,
			Cascade:   cascade,
		})
		if err != nil {
			return err
		}

		action := "Installed"
		if result.Overwritten {
			action = "Updated"
		}

		fmt.Printf("%s %s → %s\n", action, args[0], result.LocalPath)
		shortSHA := result.SHA
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		fmt.Printf("SHA: %s\n", shortSHA)

		if len(result.CascadePulled) > 0 {
			fmt.Printf("Cascade pulled %d bundles\n", len(result.CascadePulled))
		}

		return nil
	}
}

func init() {
	// Add type-specific subcommands to remote
	remoteCmd.AddCommand(remoteBundlesCmd)
	remoteCmd.AddCommand(remoteProfilesCmd)

	// Add pull to each type
	remoteBundlesCmd.AddCommand(remoteBundlesPullCmd)
	remoteProfilesCmd.AddCommand(remoteProfilesPullCmd)

	// Flags for pull commands
	for _, cmd := range []*cobra.Command{
		remoteBundlesPullCmd,
		remoteProfilesPullCmd,
	} {
		cmd.Flags().BoolVarP(&pullForce, "force", "f", false,
			"Skip confirmation prompt (content still displayed)")
	}

	// Cascade flag for profiles (enabled by default)
	remoteProfilesPullCmd.Flags().BoolVar(&pullNoCascade, "no-cascade", false,
		"Don't pull referenced bundles")
	remoteProfilesPullCmd.Flags().BoolVar(&pullCascade, "cascade", true,
		"Pull all referenced bundles (default for profiles)")

	// Cascade flag for bundles (disabled by default, future use)
	remoteBundlesPullCmd.Flags().BoolVar(&pullCascade, "cascade", false,
		"Pull referenced dependencies")
}
