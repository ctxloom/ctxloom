package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/operations"
)

var browseRecursive bool

var remoteBundlesBrowseCmd = &cobra.Command{
	Use:   "browse <remote>",
	Short: "List available bundles in a remote",
	Long: `List bundles available in a remote repository.

Examples:
  scm remote bundles browse alice
  scm remote bundles browse alice --recursive`,
	Args: cobra.ExactArgs(1),
	RunE: runRemoteBrowse("bundle"),
}

var remoteProfilesBrowseCmd = &cobra.Command{
	Use:   "browse <remote>",
	Short: "List available profiles in a remote",
	Long: `List profiles available in a remote repository.

Examples:
  scm remote profiles browse alice
  scm remote profiles browse alice --recursive`,
	Args: cobra.ExactArgs(1),
	RunE: runRemoteBrowse("profile"),
}

// runRemoteBrowse returns a RunE function for browsing items of the specified type.
func runRemoteBrowse(itemType string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}

		result, err := operations.BrowseRemote(cmd.Context(), cfg, operations.BrowseRemoteRequest{
			Remote:    args[0],
			ItemType:  itemType,
			Recursive: browseRecursive,
		})
		if err != nil {
			return err
		}

		if result.Count == 0 {
			fmt.Printf("No %ss found in %s\n", itemType, result.Remote)
			return nil
		}

		// Display results
		title := strings.ToUpper(itemType[:1]) + itemType[1:] + "s"
		fmt.Printf("%s in %s (%s):\n\n", title, result.Remote, result.URL)

		// Sort entries by path
		items := result.Items
		sort.Slice(items, func(i, j int) bool {
			return items[i].Path < items[j].Path
		})

		for _, item := range items {
			fmt.Printf("  %s\n", item.PullRef)
		}

		fmt.Println()
		fmt.Printf("Pull with: scm remote %ss pull %s/<name>\n", itemType, result.Remote)

		return nil
	}
}

func init() {
	// Add browse to bundle and profile commands
	remoteBundlesCmd.AddCommand(remoteBundlesBrowseCmd)
	remoteProfilesCmd.AddCommand(remoteProfilesBrowseCmd)

	// Flags for browse commands
	for _, cmd := range []*cobra.Command{
		remoteBundlesBrowseCmd,
		remoteProfilesBrowseCmd,
	} {
		cmd.Flags().BoolVarP(&browseRecursive, "recursive", "r", true,
			"List items in subdirectories")
	}
}
